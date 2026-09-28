// Package agent embeds the in-guest touch agent (cmd/droidpector-agent, a
// static linux/amd64 binary built by `make agent`) and speaks its protocol.
package agent

import (
	"bufio"
	"context"
	"embed"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

//go:embed bin/*
var bin embed.FS

// Port is the guest TCP port the agent listens on.
const Port = 5560

// Binary returns the agent executable for the guest architecture, or nil when
// it was not built into this executable (touch input is then unavailable).
func Binary(goarch string) []byte {
	b, err := bin.ReadFile("bin/droidpector-agent-linux-" + goarch)
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

// RemotePath is where the agent is installed in the guest.
const RemotePath = "/data/local/tmp/droidpector-agent"

// Client sends touch events to a running agent. It is safe for concurrent use.
type Client struct {
	mu   sync.Mutex
	conn net.Conn
	w    *bufio.Writer
	W, H int
}

// Dial connects and reads the agent's greeting.
func Dial(ctx context.Context, dial func(context.Context) (net.Conn, error)) (*Client, error) {
	c, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("touch agent greeting: %w", err)
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "OK %dx%d", &w, &h); err != nil {
		c.Close()
		return nil, fmt.Errorf("unexpected touch agent greeting %q", strings.TrimSpace(line))
	}
	c.SetDeadline(time.Time{})
	return &Client{conn: c, w: bufio.NewWriterSize(c, 256), W: w, H: h}, nil
}

func (c *Client) send(frames ...[5]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range frames {
		c.w.Write(f[:])
	}
	c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return c.w.Flush()
}

func frame(op byte, x, y int) [5]byte {
	var f [5]byte
	f[0] = op
	binary.BigEndian.PutUint16(f[1:], uint16(max(0, min(x, 65535))))
	binary.BigEndian.PutUint16(f[3:], uint16(max(0, min(y, 65535))))
	return f
}

// Down, Move and Up are one finger in natural-panel pixels.
func (c *Client) Down(x, y int) error { return c.send(frame(0, x, y)) }

// Move moves the finger.
func (c *Client) Move(x, y int) error { return c.send(frame(1, x, y)) }

// Up lifts the finger.
func (c *Client) Up() error { return c.send(frame(2, 0, 0)) }

// Scroll performs a swipe of (dx, dy) starting at (x, y).
func (c *Client) Scroll(x, y, dx, dy int) error {
	var d [5]byte
	d[0] = 4
	binary.BigEndian.PutUint16(d[1:], uint16(int16(dx)))
	binary.BigEndian.PutUint16(d[3:], uint16(int16(dy)))
	return c.send(frame(3, x, y), d)
}

// Close closes the connection (the agent lifts any finger still down).
func (c *Client) Close() error { return c.conn.Close() }

// Serve is the agent side of the protocol (used by the agent binary and by
// tests): it decodes frames from r and drives ts.
func Serve(r io.Reader, w io.Writer, ts Touchscreen, width, height int) {
	fmt.Fprintf(w, "OK %dx%d\n", width, height)
	br := bufio.NewReader(r)
	var f [5]byte
	for {
		if _, err := io.ReadFull(br, f[:]); err != nil {
			ts.Up()
			return
		}
		x, y := int(binary.BigEndian.Uint16(f[1:3])), int(binary.BigEndian.Uint16(f[3:5]))
		switch f[0] {
		case 0:
			ts.Down(x, y)
		case 1:
			ts.Move(x, y)
		case 2:
			ts.Up()
		case 3:
			var d [5]byte
			if _, err := io.ReadFull(br, d[:]); err != nil || d[0] != 4 {
				ts.Up()
				return
			}
			dx, dy := int(int16(binary.BigEndian.Uint16(d[1:3]))), int(int16(binary.BigEndian.Uint16(d[3:5])))
			ts.Swipe(x, y, x+dx, y+dy, 120*time.Millisecond)
		}
	}
}

// Touchscreen is what Serve drives (the uinput device in the guest).
type Touchscreen interface {
	Down(x, y int)
	Move(x, y int)
	Up()
	Swipe(x0, y0, x1, y1 int, d time.Duration)
}
