package display

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"sync"
	"time"
)

// Wire format of display messages sent to UI clients (binary WebSocket frames):
//
//	type 1 (size):  [1][w u16][h u16]
//	type 2 (rect):  [2][x u16][y u16][w u16][h u16][JPEG bytes]
const (
	MsgSize = 1
	MsgRect = 2
)

// ErrNoDisplay is returned for input while no display is connected.
var ErrNoDisplay = errors.New("the Android display is not connected")

// Subscriber receives display messages.
type Subscriber struct {
	C        chan []byte
	needFull bool
}

// Streamer fans the framebuffer out to UI clients, coalescing damage into at
// most one update per frame interval per client. Slow clients never block
// the VNC connection: they are marked for a full refresh instead.
type Streamer struct {
	mu      sync.Mutex
	client  *Client
	subs    map[*Subscriber]struct{}
	dirty   image.Rectangle
	quality int
	fps     int
}

// NewStreamer creates a streamer (JPEG quality 1-100, frames per second).
func NewStreamer(quality, fps int) *Streamer {
	if quality <= 0 || quality > 100 {
		quality = 85
	}
	if fps <= 0 {
		fps = 30
	}
	return &Streamer{subs: map[*Subscriber]struct{}{}, quality: quality, fps: fps}
}

// Damage marks a region as changed (Client damage callback).
func (s *Streamer) Damage(r image.Rectangle) {
	s.mu.Lock()
	s.dirty = s.dirty.Union(r)
	s.mu.Unlock()
}

// Resize notifies clients of a new size and forces full refreshes.
func (s *Streamer) Resize(w, h int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := sizeMsg(w, h)
	for sub := range s.subs {
		sub.needFull = true
		trySend(sub, msg)
	}
}

// Attach switches to a (re)connected VNC client; nil detaches.
func (s *Streamer) Attach(c *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = c
	for sub := range s.subs {
		sub.needFull = true
	}
}

// Connected reports whether a display client is attached.
func (s *Streamer) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client != nil
}

// Subscribe registers a UI client; it first receives the size and a full frame.
func (s *Streamer) Subscribe() *Subscriber {
	sub := &Subscriber{C: make(chan []byte, 8), needFull: true}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	if s.client != nil {
		w, h := s.client.fb.Size()
		trySend(sub, sizeMsg(w, h))
	}
	s.mu.Unlock()
	return sub
}

// Unsubscribe removes a client.
func (s *Streamer) Unsubscribe(sub *Subscriber) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
}

// Run emits coalesced updates until ctx ends.
func (s *Streamer) Run(ctx context.Context) {
	t := time.NewTicker(time.Second / time.Duration(s.fps))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

func (s *Streamer) tick() {
	s.mu.Lock()
	c := s.client
	dirty := s.dirty
	s.dirty = image.Rectangle{}
	var full, partial []*Subscriber
	for sub := range s.subs {
		if sub.needFull {
			full = append(full, sub)
		} else if !dirty.Empty() {
			partial = append(partial, sub)
		}
	}
	s.mu.Unlock()
	if c == nil || len(full)+len(partial) == 0 {
		return
	}
	fb := c.Framebuffer()
	if len(partial) > 0 {
		if msg := s.encode(fb, dirty); msg != nil {
			s.deliver(partial, msg)
		}
	}
	if len(full) > 0 {
		w, h := fb.Size()
		if msg := s.encode(fb, image.Rect(0, 0, w, h)); msg != nil {
			s.mu.Lock()
			for _, sub := range full {
				if _, ok := s.subs[sub]; ok && trySend(sub, msg) {
					sub.needFull = false
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *Streamer) deliver(subs []*Subscriber, msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range subs {
		if _, ok := s.subs[sub]; ok && !trySend(sub, msg) {
			sub.needFull = true // dropped: resynchronize with a full frame later
		}
	}
}

func (s *Streamer) encode(fb *Framebuffer, r image.Rectangle) []byte {
	img := fb.Snapshot(r)
	if img == nil || img.Rect.Empty() {
		return nil
	}
	var b bytes.Buffer
	b.Write([]byte{MsgRect})
	for _, v := range []int{img.Rect.Min.X, img.Rect.Min.Y, img.Rect.Dx(), img.Rect.Dy()} {
		binary.Write(&b, binary.BigEndian, uint16(v))
	}
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: s.quality}); err != nil {
		return nil
	}
	return b.Bytes()
}

func sizeMsg(w, h int) []byte {
	return []byte{MsgSize, byte(w >> 8), byte(w), byte(h >> 8), byte(h)}
}

func trySend(sub *Subscriber, msg []byte) bool {
	select {
	case sub.C <- msg:
		return true
	default:
		return false
	}
}

// Input is a UI input event.
type Input struct {
	Type    string `json:"t"` // "p" pointer, "k" key
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Buttons uint8  `json:"b"`
	Keysym  uint32 `json:"k"`
	Down    bool   `json:"d"`
}

// Send forwards an input event to the VM.
func (s *Streamer) Send(in Input) error {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return ErrNoDisplay
	}
	switch in.Type {
	case "p":
		return c.Pointer(in.X, in.Y, in.Buttons)
	case "k":
		return c.Key(in.Keysym, in.Down)
	}
	return errors.New("unknown input event type")
}
