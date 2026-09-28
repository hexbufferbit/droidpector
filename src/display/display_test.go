package display

import (
	"bufio"
	"bytes"
	"context"
	"crypto/des"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeVNC is a minimal RFB 3.8 server: 4x2 framebuffer, VNC auth.
type fakeVNC struct {
	password string
	mu       sync.Mutex
	inputs   [][]byte
	conn     net.Conn
	gotReq   chan struct{}
}

func (f *fakeVNC) serve(t *testing.T, conn net.Conn) {
	f.conn = conn
	f.gotReq = make(chan struct{}, 16)
	go func() {
		defer conn.Close()
		r := bufio.NewReader(conn)
		io.WriteString(conn, "RFB 003.008\n")
		ver := make([]byte, 12)
		io.ReadFull(r, ver)
		conn.Write([]byte{1, 2}) // one type: VNC auth
		var sel [1]byte
		io.ReadFull(r, sel[:])
		challenge := bytes.Repeat([]byte{7}, 16)
		conn.Write(challenge)
		resp := make([]byte, 16)
		io.ReadFull(r, resp)
		want, _ := vncAuthResponse(f.password, challenge)
		if !bytes.Equal(resp, want) {
			conn.Write([]byte{0, 0, 0, 1, 0, 0, 0, 4})
			io.WriteString(conn, "fail")
			return
		}
		conn.Write([]byte{0, 0, 0, 0})
		io.ReadFull(r, sel[:]) // ClientInit
		init := make([]byte, 24)
		binary.BigEndian.PutUint16(init[0:], 4)
		binary.BigEndian.PutUint16(init[2:], 2)
		binary.BigEndian.PutUint32(init[20:], 4)
		conn.Write(init)
		io.WriteString(conn, "QEMU")
		for {
			t, err := r.ReadByte()
			if err != nil {
				return
			}
			var n int
			switch t {
			case 0:
				n = 19
			case 2:
				hdr := make([]byte, 3)
				io.ReadFull(r, hdr)
				io.CopyN(io.Discard, r, int64(binary.BigEndian.Uint16(hdr[1:]))*4)
				continue
			case 3:
				n = 9
				f.gotReq <- struct{}{}
			case 4:
				n = 7
			case 5:
				n = 5
			}
			b := make([]byte, n)
			io.ReadFull(r, b)
			if t == 4 || t == 5 {
				f.mu.Lock()
				f.inputs = append(f.inputs, append([]byte{t}, b...))
				f.mu.Unlock()
			}
		}
	}()
}

// sendRaw sends one Raw rectangle of solid colour (RGBX little-endian).
func (f *fakeVNC) sendRaw(x, y, w, h int, r, g, b byte) {
	msg := []byte{0, 0, 0, 1}
	rh := make([]byte, 12)
	binary.BigEndian.PutUint16(rh[0:], uint16(x))
	binary.BigEndian.PutUint16(rh[2:], uint16(y))
	binary.BigEndian.PutUint16(rh[4:], uint16(w))
	binary.BigEndian.PutUint16(rh[6:], uint16(h))
	msg = append(msg, rh...)
	for i := 0; i < w*h; i++ {
		msg = append(msg, r, g, b, 0)
	}
	f.conn.Write(msg)
}

func (f *fakeVNC) sendCopy(sx, sy, dx, dy, w, h int) {
	msg := []byte{0, 0, 0, 1}
	rh := make([]byte, 16)
	binary.BigEndian.PutUint16(rh[0:], uint16(dx))
	binary.BigEndian.PutUint16(rh[2:], uint16(dy))
	binary.BigEndian.PutUint16(rh[4:], uint16(w))
	binary.BigEndian.PutUint16(rh[6:], uint16(h))
	binary.BigEndian.PutUint32(rh[8:], 1)
	binary.BigEndian.PutUint16(rh[12:], uint16(sx))
	binary.BigEndian.PutUint16(rh[14:], uint16(sy))
	f.conn.Write(append(msg, rh...))
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestVNCAuthResponseKnownVector(t *testing.T) {
	// Key bytes are bit-reversed: "pass" → 0x0e 0x86 0xce 0xce 0 0 0 0.
	resp, _ := vncAuthResponse("pass", make([]byte, 16))
	block, _ := des.NewCipher([]byte{0x0e, 0x86, 0xce, 0xce, 0, 0, 0, 0})
	want := make([]byte, 8)
	block.Encrypt(want, make([]byte, 8))
	if !bytes.Equal(resp[:8], want) {
		t.Fatalf("%x != %x", resp[:8], want)
	}
}

func TestClientDecodesUpdatesAndSendsInput(t *testing.T) {
	a, b := net.Pipe()
	srv := &fakeVNC{password: "s3cret"}
	srv.serve(t, b)
	var mu sync.Mutex
	var damaged []image.Rectangle
	c, err := NewClient(context.Background(), a, "s3cret", func(r image.Rectangle) {
		mu.Lock()
		damaged = append(damaged, r)
		mu.Unlock()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if w, h := c.Framebuffer().Size(); w != 4 || h != 2 || c.Name() != "QEMU" {
		t.Fatalf("size %dx%d name %q", w, h, c.Name())
	}
	<-srv.gotReq
	srv.sendRaw(0, 0, 2, 1, 255, 0, 0)
	srv.sendCopy(0, 0, 2, 1, 2, 1)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(damaged) == 2 })
	img := c.Framebuffer().Snapshot(image.Rect(0, 0, 4, 2))
	if px := img.RGBAAt(0, 0); px.R != 255 || px.A != 255 {
		t.Fatalf("raw pixel %+v", px)
	}
	if px := img.RGBAAt(3, 1); px.R != 255 {
		t.Fatalf("copyrect pixel %+v", px)
	}
	if px := img.RGBAAt(0, 1); px.R != 0 {
		t.Fatal("untouched pixel changed")
	}
	c.Pointer(10, -5, 1) // clamped to the framebuffer
	c.Key(0xff0d, true)
	waitFor(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.inputs) == 2 })
	p := srv.inputs[0]
	if p[0] != 5 || p[1] != 1 || binary.BigEndian.Uint16(p[2:]) != 3 || binary.BigEndian.Uint16(p[4:]) != 0 {
		t.Fatalf("pointer %v", p)
	}
	k := srv.inputs[1]
	if k[0] != 4 || k[1] != 1 || binary.BigEndian.Uint32(k[4:]) != 0xff0d {
		t.Fatalf("key %v", k)
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	a, b := net.Pipe()
	(&fakeVNC{password: "right"}).serve(t, b)
	if _, err := NewClient(context.Background(), a, "wrong", nil, nil); !errors.Is(err, ErrAuth) {
		t.Fatalf("got %v", err)
	}
}

func TestStreamerCoalescesAndResyncsSlowClients(t *testing.T) {
	a, b := net.Pipe()
	srv := &fakeVNC{password: "pw"}
	srv.serve(t, b)
	s := NewStreamer(90, 1000)
	c, err := NewClient(context.Background(), a, "pw", s.Damage, s.Resize)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s.Attach(c)
	sub := s.Subscribe()
	if msg := <-sub.C; msg[0] != MsgSize || binary.BigEndian.Uint16(msg[1:]) != 4 {
		t.Fatalf("first message must be the size: %v", msg)
	}
	s.tick() // full frame for the new subscriber
	full := <-sub.C
	if full[0] != MsgRect || binary.BigEndian.Uint16(full[5:]) != 4 || binary.BigEndian.Uint16(full[7:]) != 2 {
		t.Fatalf("full frame header %v", full[:9])
	}
	if _, err := jpeg.Decode(bytes.NewReader(full[9:])); err != nil {
		t.Fatalf("frame is not a JPEG: %v", err)
	}
	s.Damage(image.Rect(1, 0, 2, 1))
	s.Damage(image.Rect(3, 1, 4, 2))
	s.tick()
	part := <-sub.C
	if x, y, w, h := binary.BigEndian.Uint16(part[1:]), binary.BigEndian.Uint16(part[3:]), binary.BigEndian.Uint16(part[5:]), binary.BigEndian.Uint16(part[7:]); x != 1 || y != 0 || w != 3 || h != 2 {
		t.Fatalf("coalesced rect %d,%d %dx%d", x, y, w, h)
	}
	// Fill the subscriber's buffer: further updates are dropped and a full
	// refresh is scheduled instead of blocking.
	for i := 0; i < cap(sub.C)+3; i++ {
		s.Damage(image.Rect(0, 0, 1, 1))
		s.tick()
	}
	if !sub.needFull {
		t.Fatal("slow subscriber should be marked for a full refresh")
	}
	s.Unsubscribe(sub)
	if err := s.Send(Input{Type: "p", X: 1, Y: 1, Buttons: 0}); err != nil {
		t.Fatal(err)
	}
	s.Attach(nil)
	if err := s.Send(Input{Type: "k"}); !errors.Is(err, ErrNoDisplay) {
		t.Fatal("input without display must fail clearly")
	}
}
