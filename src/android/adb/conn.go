// Package adb is a client for the ADB transport protocol that talks directly
// to adbd over a caller-supplied connection (ADR-007): no adb.exe, no ADB
// server. It implements the CNXN/AUTH handshake, multiplexed streams with
// flow control, shell (v2 and legacy), exec and the sync file protocol.
package adb

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors reported by the client.
var (
	// ErrAuthRequired means adbd did not accept any of our credentials: the
	// public key was offered and must be authorized on the device (for
	// example by accepting the "Allow USB debugging?" dialog or by installing
	// it in /data/misc/adb/adb_keys).
	ErrAuthRequired = errors.New("adb: the device has not authorized this computer's ADB key")
	// ErrConnectionLost means the transport failed; all streams are closed.
	ErrConnectionLost = errors.New("adb: connection to the device was lost")
	// ErrClosed is returned for operations on a closed Conn or Stream.
	ErrClosed = errors.New("adb: use of closed connection or stream")
	// ErrProtocol wraps malformed or unexpected packets from the device.
	ErrProtocol = errors.New("adb: protocol error")
	// ErrServiceRejected means adbd refused to open a service.
	ErrServiceRejected = errors.New("adb: service rejected by device")
)

// Config configures Connect.
type Config struct {
	// Dial opens the byte stream to adbd (e.g. a TCP connection to the
	// guest's port 5555 through the user-mode network stack).
	Dial func(ctx context.Context) (net.Conn, error)
	// Key authenticates this host. If nil and the device requires
	// authentication, Connect fails with ErrAuthRequired.
	Key *rsa.PrivateKey
	// KeyComment is appended to the public key offered to the device.
	// Defaults to DefaultKeyComment.
	KeyComment string
	// AuthTimeout bounds how long to wait for the device to accept a newly
	// offered public key before failing with ErrAuthRequired. Default 30s.
	AuthTimeout time.Duration
	// MaxPayload is proposed in CNXN. Default DefaultMaxPayload.
	MaxPayload uint32
	// Features advertised to the device. Default: shell_v2, cmd.
	Features []string
}

// DeviceInfo is what the device announced in its CNXN banner.
type DeviceInfo struct {
	State      string            // "device", "recovery", "sideload", ...
	Props      map[string]string // ro.product.name, ro.product.model, ...
	Features   map[string]bool
	Version    uint32 // negotiated protocol version
	MaxPayload uint32 // negotiated maximum payload
}

// HasFeature reports whether the device advertised feature f.
func (d DeviceInfo) HasFeature(f string) bool { return d.Features[f] }

// FeatureList returns the advertised features, sorted.
func (d DeviceInfo) FeatureList() []string {
	out := make([]string, 0, len(d.Features))
	for f := range d.Features {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// parseBanner parses "device::ro.product.name=x;...;features=a,b".
func parseBanner(b string) (state string, props map[string]string, features map[string]bool) {
	b = strings.TrimRight(b, "\x00")
	state, rest, _ := strings.Cut(b, ":")
	rest = strings.TrimPrefix(rest, ":") // "::" separator; the serial between is empty
	if i := strings.Index(rest, ":"); i >= 0 && !strings.Contains(rest[:i], "=") {
		rest = rest[i+1:] // legacy "device:serial:props"
	}
	props = map[string]string{}
	features = map[string]bool{}
	for _, kv := range strings.Split(rest, ";") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if k == "features" {
			for _, f := range strings.Split(v, ",") {
				if f = strings.TrimSpace(f); f != "" {
					features[f] = true
				}
			}
			continue
		}
		props[k] = v
	}
	return state, props, features
}

// Conn is an authenticated ADB transport multiplexing many streams. It is
// safe for concurrent use.
type Conn struct {
	nc     net.Conn
	info   DeviceInfo
	csMode checksumMode
	sendCS bool

	wmu sync.Mutex // serializes packet writes

	mu      sync.Mutex
	streams map[uint32]*Stream
	nextID  uint32
	err     error // set once the transport is closed/failed

	done       chan struct{} // closed when the transport is closed/failed
	readerDone chan struct{}
}

// Connect dials adbd and performs the CNXN/AUTH handshake.
func Connect(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.Dial == nil {
		return nil, errors.New("adb: Config.Dial is required")
	}
	nc, err := cfg.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("adb: connecting to device: %w", err)
	}
	c, err := NewConn(ctx, nc, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// NewConn performs the handshake over an established connection. On error
// the caller remains responsible for closing nc.
func NewConn(ctx context.Context, nc net.Conn, cfg Config) (*Conn, error) {
	info, err := handshake(ctx, nc, cfg)
	if err != nil {
		return nil, err
	}
	c := &Conn{
		nc:         nc,
		info:       info,
		streams:    map[uint32]*Stream{},
		nextID:     1,
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
		csMode:     checksumIgnore,
	}
	if info.Version < versionSkipChecksum {
		c.csMode, c.sendCS = checksumRequired, true
	}
	go c.readLoop()
	return c, nil
}

func handshake(ctx context.Context, nc net.Conn, cfg Config) (DeviceInfo, error) {
	maxPayload := cfg.MaxPayload
	if maxPayload == 0 {
		maxPayload = DefaultMaxPayload
	}
	authTimeout := cfg.AuthTimeout
	if authTimeout <= 0 {
		authTimeout = 30 * time.Second
	}
	features := cfg.Features
	if features == nil {
		features = []string{"shell_v2", "cmd"}
	}
	comment := cfg.KeyComment
	if comment == "" {
		comment = DefaultKeyComment
	}

	// Abort blocking I/O when ctx ends.
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		stop()
		nc.SetDeadline(time.Time{})
	}()
	ctxDeadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		nc.SetDeadline(ctxDeadline)
	}

	write := func(m Message) error {
		_, err := nc.Write(m.marshal(true))
		return err
	}
	fail := func(err error, what string) error {
		if ctx.Err() != nil {
			return fmt.Errorf("adb: %s: %w", what, ctx.Err())
		}
		return fmt.Errorf("adb: %s: %w", what, err)
	}
	banner := "host::features=" + strings.Join(features, ",")
	if err := write(Message{Command: CmdCNXN, Arg0: Version, Arg1: maxPayload, Data: []byte(banner)}); err != nil {
		return DeviceInfo{}, fail(err, "sending CNXN")
	}
	const (
		authNone = iota
		authSentSignature
		authSentPublicKey
	)
	state := authNone
	for {
		m, err := readMessage(nc, maxAcceptedPayload, checksumIfPresent)
		if err != nil {
			var ne net.Error
			if state == authSentPublicKey && ctx.Err() == nil && errors.As(err, &ne) && ne.Timeout() {
				return DeviceInfo{}, fmt.Errorf("%w (no response within %s after offering the public key)", ErrAuthRequired, authTimeout)
			}
			return DeviceInfo{}, fail(err, "handshake")
		}
		switch m.Command {
		case CmdCNXN:
			if m.Arg0 < versionMin {
				return DeviceInfo{}, fmt.Errorf("%w: device protocol version %#x is too old", ErrProtocol, m.Arg0)
			}
			info := DeviceInfo{Version: min(m.Arg0, Version), MaxPayload: min(m.Arg1, maxPayload)}
			if info.MaxPayload == 0 {
				info.MaxPayload = legacyMaxPayload
			}
			info.State, info.Props, info.Features = parseBanner(string(m.Data))
			return info, nil
		case CmdAUTH:
			if m.Arg0 != AuthToken {
				continue
			}
			if cfg.Key == nil {
				return DeviceInfo{}, fmt.Errorf("%w (the device requires authentication but no key is configured)", ErrAuthRequired)
			}
			switch state {
			case authNone:
				sig, err := SignToken(cfg.Key, m.Data)
				if err != nil {
					return DeviceInfo{}, err
				}
				if err := write(Message{Command: CmdAUTH, Arg0: AuthSignature, Data: sig}); err != nil {
					return DeviceInfo{}, fail(err, "sending AUTH signature")
				}
				state = authSentSignature
			case authSentSignature:
				pub, err := EncodeAndroidPublicKey(&cfg.Key.PublicKey, comment)
				if err != nil {
					return DeviceInfo{}, err
				}
				if err := write(Message{Command: CmdAUTH, Arg0: AuthRSAPublicKey, Data: append([]byte(pub), 0)}); err != nil {
					return DeviceInfo{}, fail(err, "sending AUTH public key")
				}
				state = authSentPublicKey
				dl := time.Now().Add(authTimeout)
				if hasDeadline && ctxDeadline.Before(dl) {
					dl = ctxDeadline
				}
				nc.SetDeadline(dl)
			default:
				return DeviceInfo{}, fmt.Errorf("%w (the device rejected the offered public key)", ErrAuthRequired)
			}
		case CmdSTLS:
			return DeviceInfo{}, fmt.Errorf("%w: device requested TLS (wireless debugging pairing), which is not supported", ErrProtocol)
		default:
			// Ignore anything else until the connection is established.
		}
	}
}

// Info returns the device information announced during the handshake.
func (c *Conn) Info() DeviceInfo { return c.info }

// Done is closed when the transport has been closed or lost.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns why the transport ended (nil while it is alive).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close closes the transport and all streams.
func (c *Conn) Close() error {
	c.fail(ErrClosed)
	<-c.readerDone
	return nil
}

func (c *Conn) send(m Message) error {
	b := m.marshal(c.sendCS)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return c.transportErr()
	default:
	}
	if _, err := c.nc.Write(b); err != nil {
		c.fail(fmt.Errorf("%w: %v", ErrConnectionLost, err))
		return c.transportErr()
	}
	return nil
}

func (c *Conn) transportErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return ErrClosed
	}
	return c.err
}

// fail tears the transport down once, failing every stream with err.
func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	streams := c.streams
	c.streams = map[uint32]*Stream{}
	close(c.done)
	c.mu.Unlock()
	c.nc.Close()
	for _, s := range streams {
		s.terminate(err)
	}
}

func (c *Conn) readLoop() {
	defer close(c.readerDone)
	for {
		m, err := readMessage(c.nc, maxAcceptedPayload, c.csMode)
		if err != nil {
			c.fail(fmt.Errorf("%w: %v", ErrConnectionLost, err))
			return
		}
		c.dispatch(m)
	}
}

// dispatch handles one packet. It must never block on the network: the
// reader goroutine has to keep draining the transport.
func (c *Conn) dispatch(m Message) {
	switch m.Command {
	case CmdOKAY:
		s := c.lookup(m.Arg1)
		if s == nil {
			return
		}
		if s.handleOkay(m.Arg0) {
			// The open was abandoned locally; close the remote end.
			c.forget(s)
			go c.send(Message{Command: CmdCLSE, Arg0: s.local, Arg1: m.Arg0})
		}
	case CmdWRTE:
		s := c.lookup(m.Arg1)
		if s == nil {
			// Unknown stream: tell the device so it stops writing.
			go c.send(Message{Command: CmdCLSE, Arg0: 0, Arg1: m.Arg0})
			return
		}
		s.handleWrite(m.Data)
	case CmdCLSE:
		s := c.lookup(m.Arg1)
		if s == nil {
			return
		}
		c.forget(s)
		s.handleClose()
	case CmdCNXN:
		c.fail(fmt.Errorf("%w: device restarted the connection", ErrConnectionLost))
	default:
		// AUTH/SYNC/unknown packets after the handshake are ignored.
	}
}

func (c *Conn) lookup(id uint32) *Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[id]
}

func (c *Conn) forget(s *Stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streams[s.local] == s {
		delete(c.streams, s.local)
	}
}

// Open opens a service stream, e.g. "shell,v2,raw:ls" or "sync:".
func (c *Conn) Open(ctx context.Context, service string) (*Stream, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	id := c.nextID
	for c.streams[id] != nil || id == 0 {
		id++
	}
	c.nextID = id + 1
	s := newStream(c, id, service)
	c.streams[id] = s
	c.mu.Unlock()

	if err := c.send(Message{Command: CmdOPEN, Arg0: id, Data: append([]byte(service), 0)}); err != nil {
		c.forget(s)
		return nil, err
	}
	select {
	case <-s.opened:
		if s.openErr != nil {
			return nil, s.openErr
		}
		return s, nil
	case <-ctx.Done():
		if s.abandonOpen() {
			// OKAY already arrived between the select and abandon: close normally.
			s.Close()
		}
		return nil, fmt.Errorf("adb: opening %q: %w", service, ctx.Err())
	}
}

// Stream is one multiplexed ADB stream. Read and Write may be used from
// different goroutines concurrently; Close unblocks both.
type Stream struct {
	c       *Conn
	local   uint32
	service string

	opened  chan struct{}
	openErr error

	mu        sync.Mutex
	remote    uint32
	isOpen    bool
	abandoned bool
	queue     [][]byte // received chunks awaiting consumption
	cur       []byte   // chunk being consumed
	curAck    bool     // cur must be acknowledged when drained
	rclosed   bool     // remote sent CLSE
	lclosed   bool     // Close called
	err       error    // transport error
	changed   chan struct{}

	wmu sync.Mutex    // one writer at a time
	ack chan struct{} // OKAY for our last WRTE
}

func newStream(c *Conn, id uint32, service string) *Stream {
	return &Stream{c: c, local: id, service: service, opened: make(chan struct{}), changed: make(chan struct{}), ack: make(chan struct{}, 1)}
}

// Service returns the service name the stream was opened with.
func (s *Stream) Service() string { return s.service }

// broadcast wakes all waiters; s.mu must be held.
func (s *Stream) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// handleOkay processes OKAY; it returns true if the stream was abandoned
// while opening and must be closed remotely.
func (s *Stream) handleOkay(remote uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isOpen {
		if s.abandoned {
			return true
		}
		s.isOpen, s.remote = true, remote
		close(s.opened)
		return false
	}
	select {
	case s.ack <- struct{}{}:
	default:
	}
	return false
}

// abandonOpen marks a pending open as cancelled. It returns true when the
// stream had already been opened.
func (s *Stream) abandonOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isOpen {
		return true
	}
	s.abandoned = true
	return false
}

func (s *Stream) handleWrite(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isOpen || s.lclosed || s.rclosed {
		return
	}
	s.queue = append(s.queue, data)
	s.broadcast()
}

func (s *Stream) handleClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isOpen {
		if !s.abandoned {
			s.openErr = fmt.Errorf("%w: %q", ErrServiceRejected, s.service)
			close(s.opened)
		}
		return
	}
	s.rclosed = true
	s.broadcast()
}

func (s *Stream) terminate(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isOpen {
		if !s.abandoned {
			s.openErr = err
			s.abandoned = true
			close(s.opened)
		}
		return
	}
	if s.err == nil {
		s.err = err
	}
	s.broadcast()
}

// Read reads data sent by the device. It returns io.EOF after the device
// closed the stream and all data was consumed.
func (s *Stream) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.cur) == 0 && s.curAck {
			// Acknowledge the drained chunk so the device sends the next one.
			s.curAck = false
			remote, closed := s.remote, s.lclosed || s.rclosed || s.err != nil
			s.mu.Unlock()
			if !closed {
				if err := s.c.send(Message{Command: CmdOKAY, Arg0: s.local, Arg1: remote}); err != nil {
					return 0, err
				}
			}
			continue
		}
		if len(s.cur) == 0 && len(s.queue) > 0 {
			s.cur, s.queue = s.queue[0], s.queue[1:]
			s.curAck = true
			if len(s.cur) == 0 {
				s.mu.Unlock()
				continue // empty WRTE: acknowledge it at the top of the loop
			}
		}
		if len(s.cur) > 0 {
			if len(p) == 0 {
				s.mu.Unlock()
				return 0, nil
			}
			n := copy(p, s.cur)
			s.cur = s.cur[n:]
			if len(s.cur) == 0 && s.curAck {
				// Acknowledge eagerly so the device can send the next chunk
				// while the caller processes this one.
				s.curAck = false
				remote, closed := s.remote, s.lclosed || s.rclosed || s.err != nil
				s.mu.Unlock()
				if !closed {
					if err := s.c.send(Message{Command: CmdOKAY, Arg0: s.local, Arg1: remote}); err != nil {
						return n, err
					}
				}
				return n, nil
			}
			s.mu.Unlock()
			return n, nil
		}
		switch {
		case s.lclosed:
			s.mu.Unlock()
			return 0, ErrClosed
		case s.rclosed:
			s.mu.Unlock()
			return 0, io.EOF
		case s.err != nil:
			err := s.err
			s.mu.Unlock()
			return 0, err
		}
		ch := s.changed
		s.mu.Unlock()
		<-ch
	}
}

// Write sends p to the device, splitting it into payload-sized packets and
// waiting for the device's acknowledgement of each (flow control).
func (s *Stream) Write(p []byte) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	max := int(s.c.info.MaxPayload)
	written := 0
	for written < len(p) {
		if err := s.writable(); err != nil {
			return written, err
		}
		n := min(len(p)-written, max)
		s.mu.Lock()
		remote := s.remote
		s.mu.Unlock()
		// Drop a stale ack (cannot normally happen; one WRTE is in flight at most).
		select {
		case <-s.ack:
		default:
		}
		if err := s.c.send(Message{Command: CmdWRTE, Arg0: s.local, Arg1: remote, Data: p[written : written+n]}); err != nil {
			return written, err
		}
		if err := s.waitAck(); err != nil {
			return written, err
		}
		written += n
	}
	return written, nil
}

func (s *Stream) writable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.lclosed:
		return ErrClosed
	case s.err != nil:
		return s.err
	case s.rclosed:
		return fmt.Errorf("adb: stream %q closed by device: %w", s.service, io.ErrClosedPipe)
	}
	return nil
}

func (s *Stream) waitAck() error {
	for {
		s.mu.Lock()
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-s.ack:
			return nil
		case <-ch:
			if err := s.writable(); err != nil {
				return err
			}
		}
	}
}

// Close closes the stream. It is safe to call multiple times and
// concurrently with Read/Write, which then return ErrClosed.
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.lclosed {
		s.mu.Unlock()
		return nil
	}
	s.lclosed = true
	notify := s.isOpen && !s.rclosed && s.err == nil
	remote := s.remote
	s.queue, s.cur = nil, nil
	s.broadcast()
	s.mu.Unlock()
	s.c.forget(s)
	if notify {
		if err := s.c.send(Message{Command: CmdCLSE, Arg0: s.local, Arg1: remote}); err != nil && !errors.Is(err, ErrClosed) {
			return err
		}
	}
	return nil
}
