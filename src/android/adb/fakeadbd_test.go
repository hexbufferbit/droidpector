package adb

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeADBD is an in-process adbd speaking the real transport protocol over
// one end of a net.Pipe. It exists only for tests.
type fakeADBD struct {
	banner       string
	version      uint32
	maxPayload   uint32
	requireAuth  bool
	acceptNewKey bool
	shell        func(cmd string) (stdout, stderr string, code int)

	conn   net.Conn
	wmu    sync.Mutex
	mu     sync.Mutex
	nextID uint32
	strs   map[uint32]*fakeStream
	auth   []*rsa.PublicKey
	token  []byte
	offers []string // public keys offered by the host
	peer   uint32   // host max payload

	files map[string]fakeFile
	opens []string
	hung  chan struct{} // receives when a "hang" command starts
}

type fakeFile struct {
	data  []byte
	mode  uint32
	mtime uint32
}

type fakeStream struct {
	d             *fakeADBD
	local, remote uint32
	service       string
	in            chan []byte
	acks          chan struct{}
	closed        chan struct{}
	closeOnce     sync.Once
	buf           []byte
}

func newFake(opts ...func(*fakeADBD)) *fakeADBD {
	d := &fakeADBD{
		banner:     "device::ro.product.name=bliss_x86_64;ro.product.model=BlissOS;ro.product.device=x86_64;features=shell_v2,cmd,stat_v2,abb",
		version:    Version,
		maxPayload: 1 << 20,
		nextID:     100,
		strs:       map[uint32]*fakeStream{},
		files:      map[string]fakeFile{},
		hung:       make(chan struct{}, 16),
		shell: func(cmd string) (string, string, int) {
			return "", "sh: " + cmd + ": not found\n", 127
		},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// start connects a client to the fake and returns the client-side conn.
func (d *fakeADBD) start(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	d.conn = server
	go d.serve()
	t.Cleanup(func() { server.Close(); client.Close() })
	return client
}

func (d *fakeADBD) send(m Message) error {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	_, err := d.conn.Write(m.marshal(d.version < versionSkipChecksum || m.Command == CmdCNXN || m.Command == CmdAUTH))
	return err
}

func (d *fakeADBD) sendCNXN() {
	d.send(Message{Command: CmdCNXN, Arg0: d.version, Arg1: d.maxPayload, Data: []byte(d.banner)})
}

func (d *fakeADBD) sendToken() {
	d.token = make([]byte, tokenSize)
	rand.Read(d.token)
	d.send(Message{Command: CmdAUTH, Arg0: AuthToken, Data: d.token})
}

func (d *fakeADBD) serve() {
	defer d.shutdown()
	for {
		m, err := readMessage(d.conn, maxAcceptedPayload, checksumIfPresent)
		if err != nil {
			return
		}
		switch m.Command {
		case CmdCNXN:
			d.peer = m.Arg1
			if d.requireAuth {
				d.sendToken()
			} else {
				d.sendCNXN()
			}
		case CmdAUTH:
			switch m.Arg0 {
			case AuthSignature:
				ok := false
				for _, k := range d.auth {
					if rsa.VerifyPKCS1v15(k, crypto.SHA1, d.token, m.Data) == nil {
						ok = true
					}
				}
				if ok {
					d.sendCNXN()
				} else {
					d.sendToken()
				}
			case AuthRSAPublicKey:
				d.mu.Lock()
				d.offers = append(d.offers, string(m.Data))
				d.mu.Unlock()
				if d.acceptNewKey {
					if k, err := DecodeAndroidPublicKey(string(m.Data)); err == nil {
						d.auth = append(d.auth, k)
						d.sendCNXN()
					}
				}
			}
		case CmdOPEN:
			service := strings.TrimRight(string(m.Data), "\x00")
			d.mu.Lock()
			d.opens = append(d.opens, service)
			d.nextID++
			s := &fakeStream{d: d, local: d.nextID, remote: m.Arg0, service: service,
				in: make(chan []byte, 4096), acks: make(chan struct{}, 1), closed: make(chan struct{})}
			handler := d.handler(service)
			if handler != nil {
				d.strs[s.local] = s
			}
			d.mu.Unlock()
			if handler == nil {
				d.send(Message{Command: CmdCLSE, Arg0: 0, Arg1: m.Arg0})
				continue
			}
			d.send(Message{Command: CmdOKAY, Arg0: s.local, Arg1: s.remote})
			go func() {
				handler(s)
				s.close()
			}()
		case CmdWRTE:
			s := d.stream(m.Arg1)
			if s == nil {
				continue
			}
			select {
			case s.in <- m.Data:
			case <-s.closed:
				continue
			}
			d.send(Message{Command: CmdOKAY, Arg0: s.local, Arg1: s.remote})
		case CmdOKAY:
			if s := d.stream(m.Arg1); s != nil {
				select {
				case s.acks <- struct{}{}:
				default:
				}
			}
		case CmdCLSE:
			if s := d.stream(m.Arg1); s != nil {
				s.markClosed()
			}
		}
	}
}

func (d *fakeADBD) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.strs {
		s.markClosed()
	}
}

func (d *fakeADBD) stream(id uint32) *fakeStream {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.strs[id]
}

func (s *fakeStream) markClosed() { s.closeOnce.Do(func() { close(s.closed) }) }

func (s *fakeStream) close() {
	select {
	case <-s.closed:
	default:
		s.d.send(Message{Command: CmdCLSE, Arg0: s.local, Arg1: s.remote})
	}
	s.markClosed()
}

// Write sends data to the host honouring its payload size and flow control.
func (s *fakeStream) Write(p []byte) (int, error) {
	max := int(min(s.d.peer, s.d.maxPayload))
	n := 0
	for n < len(p) {
		c := min(len(p)-n, max)
		if err := s.d.send(Message{Command: CmdWRTE, Arg0: s.local, Arg1: s.remote, Data: p[n : n+c]}); err != nil {
			return n, err
		}
		select {
		case <-s.acks:
		case <-s.closed:
			return n, io.ErrClosedPipe
		}
		n += c
	}
	return n, nil
}

// Read returns host data; io.EOF once the host closed the stream.
func (s *fakeStream) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		select {
		case b := <-s.in:
			s.buf = b
		case <-s.closed:
			select {
			case b := <-s.in:
				s.buf = b
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (d *fakeADBD) handler(service string) func(*fakeStream) {
	switch {
	case strings.HasPrefix(service, "shell,v2,raw:"):
		return func(s *fakeStream) { d.shellV2(s, strings.TrimPrefix(service, "shell,v2,raw:")) }
	case strings.HasPrefix(service, "shell:"):
		return func(s *fakeStream) { d.shellLegacy(s, strings.TrimPrefix(service, "shell:")) }
	case service == "sync:":
		return d.sync
	case strings.HasPrefix(service, "exec:echo "):
		return func(s *fakeStream) { s.Write([]byte(strings.TrimPrefix(service, "exec:echo ") + "\n")) }
	}
	return nil
}

func (d *fakeADBD) shellV2(s *fakeStream, cmd string) {
	// Consume stdin until the host closes it.
	var hdr [5]byte
	if _, err := io.ReadFull(s, hdr[:]); err != nil || hdr[0] != shellCloseStdin {
		return
	}
	if cmd == "hang" {
		d.hung <- struct{}{}
		<-s.closed
		return
	}
	out, errOut, code := d.shell(cmd)
	packet := func(id byte, data []byte) {
		b := make([]byte, 5+len(data))
		b[0] = id
		binary.LittleEndian.PutUint32(b[1:], uint32(len(data)))
		copy(b[5:], data)
		s.Write(b)
	}
	// Split large stdout into several packets like adbd does.
	for o := []byte(out); len(o) > 0; {
		n := min(len(o), 4000)
		packet(shellStdout, o[:n])
		o = o[n:]
	}
	if errOut != "" {
		packet(shellStderr, []byte(errOut))
	}
	packet(shellExit, []byte{byte(code)})
}

func (d *fakeADBD) shellLegacy(s *fakeStream, full string) {
	cmd, rest, ok := strings.Cut(full, "\necho -n \"")
	if !ok {
		out, errOut, _ := d.shell(full)
		s.Write([]byte(out + errOut))
		return
	}
	marker := strings.TrimSuffix(rest, "$?\"")
	out, errOut, code := d.shell(cmd)
	s.Write([]byte(out + errOut + marker + strconv.Itoa(code)))
}

func (d *fakeADBD) sync(s *fakeStream) {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(s, hdr[:]); err != nil {
			return
		}
		n := binary.LittleEndian.Uint32(hdr[4:])
		id := string(hdr[:4])
		if id == "QUIT" {
			return
		}
		arg := make([]byte, n)
		if _, err := io.ReadFull(s, arg); err != nil {
			return
		}
		switch id {
		case "STAT":
			d.mu.Lock()
			f, ok := d.files[string(arg)]
			d.mu.Unlock()
			resp := make([]byte, 16)
			copy(resp, "STAT")
			if ok {
				binary.LittleEndian.PutUint32(resp[4:], f.mode)
				binary.LittleEndian.PutUint32(resp[8:], uint32(len(f.data)))
				binary.LittleEndian.PutUint32(resp[12:], f.mtime)
			}
			s.Write(resp)
		case "SEND":
			path, modeStr, _ := strings.Cut(string(arg), ",")
			mode, _ := strconv.Atoi(modeStr)
			var data bytes.Buffer
			var mtime uint32
			for {
				if _, err := io.ReadFull(s, hdr[:]); err != nil {
					return
				}
				n := binary.LittleEndian.Uint32(hdr[4:])
				if string(hdr[:4]) == "DONE" {
					mtime = n
					break
				}
				if string(hdr[:4]) != "DATA" || n > syncDataMax {
					s.Write(syncFail("bad DATA"))
					return
				}
				if _, err := io.CopyN(&data, s, int64(n)); err != nil {
					return
				}
			}
			if strings.HasPrefix(path, "/readonly/") {
				s.Write(syncFail("couldn't create file: Read-only file system"))
				continue
			}
			d.mu.Lock()
			d.files[path] = fakeFile{data: data.Bytes(), mode: uint32(mode), mtime: mtime}
			d.mu.Unlock()
			s.Write([]byte("OKAY\x00\x00\x00\x00"))
		case "RECV":
			d.mu.Lock()
			f, ok := d.files[string(arg)]
			d.mu.Unlock()
			if !ok {
				s.Write(syncFail("open failed: No such file or directory"))
				continue
			}
			var out bytes.Buffer
			for b := f.data; len(b) > 0; {
				c := min(len(b), syncDataMax)
				out.Write(syncRequest("DATA", b[:c]))
				b = b[c:]
			}
			out.Write([]byte("DONE\x00\x00\x00\x00"))
			s.Write(out.Bytes())
		default:
			s.Write(syncFail("unknown request " + id))
			return
		}
	}
}

func syncFail(msg string) []byte { return syncRequest("FAIL", []byte(msg)) }

var (
	keyOnce sync.Once
	keyVal  *rsa.PrivateKey
)

func testKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := GenerateKey(nil)
		if err != nil {
			panic(err)
		}
		keyVal = k
	})
	return keyVal
}

// connect performs the handshake against d and returns the client.
func connect(t *testing.T, d *fakeADBD, cfg Config) *Conn {
	t.Helper()
	nc := d.start(t)
	cfg.Dial = func(context.Context) (net.Conn, error) { return nc, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (d *fakeADBD) String() string { return fmt.Sprintf("fakeADBD(%s)", d.banner) }
