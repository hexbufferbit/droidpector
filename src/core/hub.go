package core

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/query"
)

// Message is a push notification to UI clients.
type Message struct {
	Type      string       `json:"type"` // "status", "events"
	Status    *Status      `json:"status,omitempty"`
	SessionID string       `json:"sessionId,omitempty"`
	Stats     *query.Stats `json:"stats,omitempty"`
}

// Hub broadcasts status changes immediately and event-list changes
// coalesced (at most every 100 ms), so thousands of requests per second never
// flood the UI.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	dirty  map[string]bool
	wake   chan struct{}
	lastSt []byte
}

// NewHub creates a hub.
func NewHub() *Hub {
	return &Hub{subs: map[chan []byte]struct{}{}, dirty: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// Subscribe returns a channel of JSON messages; the latest status is sent first.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	if h.lastSt != nil {
		ch <- h.lastSt
	}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) broadcast(b []byte) {
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // slow client: it will resync from the next message
		}
	}
}

// StatusChanged publishes a sandbox status.
func (h *Hub) StatusChanged(st Status) {
	b, _ := json.Marshal(Message{Type: "status", Status: &st})
	h.mu.Lock()
	h.lastSt = b
	h.broadcast(b)
	h.mu.Unlock()
}

// EventsChanged marks a session's event list as changed.
func (h *Hub) EventsChanged(sessionID string) {
	h.mu.Lock()
	h.dirty[sessionID] = true
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Run emits coalesced event notifications until ctx ends.
func (h *Hub) Run(ctx context.Context, stats func(string) (query.Stats, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
		h.mu.Lock()
		dirty := h.dirty
		h.dirty = map[string]bool{}
		h.mu.Unlock()
		for sid := range dirty {
			m := Message{Type: "events", SessionID: sid}
			if st, err := stats(sid); err == nil {
				m.Stats = &st
			}
			b, _ := json.Marshal(m)
			h.mu.Lock()
			h.broadcast(b)
			h.mu.Unlock()
		}
	}
}
