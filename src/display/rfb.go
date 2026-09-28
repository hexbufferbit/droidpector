// Package display connects to the VM's VNC (RFB 3.8) server on loopback,
// maintains the Android framebuffer, and streams dirty regions to UI clients
// while relaying their pointer and keyboard input.
package display

import (
	"bufio"
	"context"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"sync"
	"time"
)

// Framebuffer holds the current screen in RGBA.
type Framebuffer struct {
	mu  sync.RWMutex
	img *image.RGBA
}

// Snapshot copies a region of the framebuffer.
func (f *Framebuffer) Snapshot(r image.Rectangle) *image.RGBA {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.img == nil {
		return nil
	}
	r = r.Intersect(f.img.Rect)
	out := image.NewRGBA(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(out.Pix[out.PixOffset(r.Min.X, y):out.PixOffset(r.Max.X, y)], f.img.Pix[f.img.PixOffset(r.Min.X, y):f.img.PixOffset(r.Max.X, y)])
	}
	return out
}

// Size returns the framebuffer size.
func (f *Framebuffer) Size() (int, int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.img == nil {
		return 0, 0
	}
	return f.img.Rect.Dx(), f.img.Rect.Dy()
}

// Client is an RFB client.
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	wmu    sync.Mutex
	fb     Framebuffer
	name   string
	damage func(image.Rectangle) // called for every updated region
	resize func(w, h int)
	done   chan struct{}
	err    error
}

// ErrAuth is returned when the VNC password is rejected.
var ErrAuth = errors.New("the display rejected the password")

// Dial connects and authenticates.
func Dial(ctx context.Context, addr, password string, damage func(image.Rectangle), resize func(w, h int)) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to the Android display: %w", err)
	}
	c, err := NewClient(ctx, conn, password, damage, resize)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// NewClient performs the RFB handshake on conn and starts the update loop.
func NewClient(ctx context.Context, conn net.Conn, password string, damage func(image.Rectangle), resize func(w, h int)) (*Client, error) {
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(15 * time.Second))
	}
	c := &Client{conn: conn, r: bufio.NewReaderSize(conn, 1<<20), damage: damage, resize: resize, done: make(chan struct{})}
	if err := c.handshake(password); err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	go c.loop()
	return c, nil
}

func (c *Client) handshake(password string) error {
	ver := make([]byte, 12)
	if _, err := io.ReadFull(c.r, ver); err != nil {
		return fmt.Errorf("reading display protocol version: %w", err)
	}
	if string(ver[:4]) != "RFB " {
		return fmt.Errorf("the display endpoint is not a VNC server")
	}
	if _, err := c.conn.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	var n [1]byte
	if _, err := io.ReadFull(c.r, n[:]); err != nil {
		return err
	}
	if n[0] == 0 {
		return c.readFailure()
	}
	types := make([]byte, n[0])
	if _, err := io.ReadFull(c.r, types); err != nil {
		return err
	}
	chosen := byte(0)
	for _, t := range types {
		if t == 2 && password != "" || t == 1 && password == "" {
			chosen = t
		}
	}
	if chosen == 0 {
		return fmt.Errorf("the display offers no supported security type (%v)", types)
	}
	c.conn.Write([]byte{chosen})
	if chosen == 2 {
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(c.r, challenge); err != nil {
			return err
		}
		resp, err := vncAuthResponse(password, challenge)
		if err != nil {
			return err
		}
		c.conn.Write(resp)
	}
	var res [4]byte
	if _, err := io.ReadFull(c.r, res[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(res[:]) != 0 {
		return fmt.Errorf("%w: %v", ErrAuth, c.readFailure())
	}
	// ClientInit: shared session.
	c.conn.Write([]byte{1})
	hdr := make([]byte, 24)
	if _, err := io.ReadFull(c.r, hdr); err != nil {
		return err
	}
	w, h := int(binary.BigEndian.Uint16(hdr[0:2])), int(binary.BigEndian.Uint16(hdr[2:4]))
	nameLen := binary.BigEndian.Uint32(hdr[20:24])
	if nameLen > 4096 {
		return fmt.Errorf("invalid display name length")
	}
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(c.r, name); err != nil {
		return err
	}
	c.name = string(name)
	c.setSize(w, h)
	// SetPixelFormat: 32bpp, depth 24, little endian, true colour, R<<0 G<<8 B<<16
	// so pixels map straight to RGBA bytes.
	pf := []byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 0, 8, 16, 0, 0, 0}
	// SetEncodings: Raw, CopyRect, DesktopSize pseudo-encoding.
	enc := []byte{2, 0, 0, 3}
	for _, e := range []int32{1, 0, -223} {
		enc = binary.BigEndian.AppendUint32(enc, uint32(e))
	}
	if _, err := c.conn.Write(append(pf, enc...)); err != nil {
		return err
	}
	return c.requestUpdate(false)
}

func (c *Client) readFailure() error {
	var l [4]byte
	if _, err := io.ReadFull(c.r, l[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(l[:])
	if n > 4096 {
		n = 4096
	}
	msg := make([]byte, n)
	io.ReadFull(c.r, msg)
	return fmt.Errorf("display refused the connection: %s", msg)
}

// vncAuthResponse encrypts the challenge with DES using the password (max 8
// bytes) with each byte bit-reversed, as RFB specifies.
func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	copy(key, password)
	for i, b := range key {
		var r byte
		for j := 0; j < 8; j++ {
			if b&(1<<j) != 0 {
				r |= 1 << (7 - j)
			}
		}
		key[i] = r
	}
	block, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	block.Encrypt(out[:8], challenge[:8])
	block.Encrypt(out[8:], challenge[8:])
	return out, nil
}

func (c *Client) setSize(w, h int) {
	c.fb.mu.Lock()
	old := c.fb.img
	c.fb.img = image.NewRGBA(image.Rect(0, 0, w, h))
	if old != nil {
		r := old.Rect.Intersect(c.fb.img.Rect)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			copy(c.fb.img.Pix[c.fb.img.PixOffset(0, y):c.fb.img.PixOffset(r.Max.X, y)], old.Pix[old.PixOffset(0, y):old.PixOffset(r.Max.X, y)])
		}
	}
	c.fb.mu.Unlock()
	if c.resize != nil {
		c.resize(w, h)
	}
}

func (c *Client) requestUpdate(incremental bool) error {
	w, h := c.fb.Size()
	msg := []byte{3, 0, 0, 0, 0, 0, byte(w >> 8), byte(w), byte(h >> 8), byte(h)}
	if incremental {
		msg[1] = 1
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.conn.Write(msg)
	return err
}

// Framebuffer returns the live framebuffer.
func (c *Client) Framebuffer() *Framebuffer { return &c.fb }

// Name returns the desktop name.
func (c *Client) Name() string { return c.name }

// Done is closed when the connection ends; Err returns the reason.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns why the client stopped.
func (c *Client) Err() error { return c.err }

// Close disconnects.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) loop() {
	defer close(c.done)
	for {
		t, err := c.r.ReadByte()
		if err != nil {
			c.err = err
			return
		}
		switch t {
		case 0: // FramebufferUpdate
			if err := c.readUpdate(); err != nil {
				c.err = err
				c.conn.Close()
				return
			}
			if err := c.requestUpdate(true); err != nil {
				c.err = err
				return
			}
		case 1: // SetColourMapEntries (unused with true colour)
			hdr := make([]byte, 5)
			if _, err := io.ReadFull(c.r, hdr); err != nil {
				c.err = err
				return
			}
			io.CopyN(io.Discard, c.r, int64(binary.BigEndian.Uint16(hdr[3:5]))*6)
		case 2: // Bell
		case 3: // ServerCutText
			hdr := make([]byte, 7)
			if _, err := io.ReadFull(c.r, hdr); err != nil {
				c.err = err
				return
			}
			io.CopyN(io.Discard, c.r, int64(binary.BigEndian.Uint32(hdr[3:7])))
		default:
			c.err = fmt.Errorf("unsupported display message type %d", t)
			c.conn.Close()
			return
		}
	}
}

func (c *Client) readUpdate() error {
	hdr := make([]byte, 3)
	if _, err := io.ReadFull(c.r, hdr); err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint16(hdr[1:3]))
	for i := 0; i < n; i++ {
		rh := make([]byte, 12)
		if _, err := io.ReadFull(c.r, rh); err != nil {
			return err
		}
		x, y := int(binary.BigEndian.Uint16(rh[0:2])), int(binary.BigEndian.Uint16(rh[2:4]))
		w, h := int(binary.BigEndian.Uint16(rh[4:6])), int(binary.BigEndian.Uint16(rh[6:8]))
		enc := int32(binary.BigEndian.Uint32(rh[8:12]))
		switch enc {
		case 0: // Raw
			if err := c.readRaw(x, y, w, h); err != nil {
				return err
			}
		case 1: // CopyRect
			src := make([]byte, 4)
			if _, err := io.ReadFull(c.r, src); err != nil {
				return err
			}
			c.copyRect(int(binary.BigEndian.Uint16(src[0:2])), int(binary.BigEndian.Uint16(src[2:4])), x, y, w, h)
		case -223: // DesktopSize
			c.setSize(w, h)
			if c.damage != nil {
				c.damage(image.Rect(0, 0, w, h))
			}
			continue
		default:
			return fmt.Errorf("unsupported display encoding %d", enc)
		}
		if c.damage != nil {
			c.damage(image.Rect(x, y, x+w, y+h))
		}
	}
	return nil
}

func (c *Client) readRaw(x, y, w, h int) error {
	row := make([]byte, w*4)
	c.fb.mu.Lock()
	defer c.fb.mu.Unlock()
	img := c.fb.img
	for j := 0; j < h; j++ {
		if _, err := io.ReadFull(c.r, row); err != nil {
			return err
		}
		yy := y + j
		if yy >= img.Rect.Dy() || x >= img.Rect.Dx() {
			continue
		}
		n := min(w, img.Rect.Dx()-x)
		off := img.PixOffset(x, yy)
		dst := img.Pix[off : off+n*4]
		copy(dst, row[:n*4])
		for k := 3; k < len(dst); k += 4 {
			dst[k] = 255 // opaque
		}
	}
	return nil
}

func (c *Client) copyRect(sx, sy, dx, dy, w, h int) {
	c.fb.mu.Lock()
	defer c.fb.mu.Unlock()
	img := c.fb.img
	src := image.Rect(sx, sy, sx+w, sy+h).Intersect(img.Rect)
	tmp := image.NewRGBA(src)
	for y := src.Min.Y; y < src.Max.Y; y++ {
		copy(tmp.Pix[tmp.PixOffset(src.Min.X, y):tmp.PixOffset(src.Max.X, y)], img.Pix[img.PixOffset(src.Min.X, y):img.PixOffset(src.Max.X, y)])
	}
	for y := 0; y < src.Dy(); y++ {
		ty := dy + y
		if ty >= img.Rect.Dy() {
			break
		}
		n := min(src.Dx(), img.Rect.Dx()-dx)
		if n <= 0 {
			break
		}
		copy(img.Pix[img.PixOffset(dx, ty):img.PixOffset(dx+n, ty)], tmp.Pix[tmp.PixOffset(src.Min.X, src.Min.Y+y):tmp.PixOffset(src.Min.X+n, src.Min.Y+y)])
	}
}

// Pointer sends a pointer event: buttons is the RFB mask (1 left, 2 middle,
// 4 right, 8 wheel up, 16 wheel down).
func (c *Client) Pointer(x, y int, buttons uint8) error {
	w, h := c.fb.Size()
	x, y = max(0, min(x, w-1)), max(0, min(y, h-1))
	msg := []byte{5, buttons, byte(x >> 8), byte(x), byte(y >> 8), byte(y)}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.conn.Write(msg)
	return err
}

// Key sends an X11 keysym press or release.
func (c *Client) Key(keysym uint32, down bool) error {
	msg := []byte{4, 0, 0, 0, 0, 0, 0, 0}
	if down {
		msg[1] = 1
	}
	binary.BigEndian.PutUint32(msg[4:], keysym)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.conn.Write(msg)
	return err
}
