// Package core is the application/control layer. It orchestrates the VM,
// Android device, network gateway, storage and query services behind a small
// API consumed by the IPC layer. The UI never talks to QEMU, ADB or the
// gateway directly.
package core

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/query"
	"github.com/droidpector/apkinspector/src/storage"
)

// Recorder is the network.Sink: it stamps events with the active session,
// a stable sequence number and the owning app, then hands them to the async
// storage writer and the live query index. It never blocks capture.
type Recorder struct {
	writer *storage.Writer
	query  *query.Service
	notify func(sessionID string)

	mu        sync.RWMutex
	sessionID string
	seq       atomic.Int64
	seqs      map[string]int64 // event ID → seq for events still being updated

	ownerMu sync.RWMutex
	owners  func(clientPort int) string

	unattrMu sync.Mutex
	unattr   map[string]unattributed // event ID → latest snapshot awaiting an owner
}

type unattributed struct {
	e    *model.Event
	port int
	seen time.Time
}

// NewRecorder creates a recorder. notify is called (cheaply) on every change.
func NewRecorder(w *storage.Writer, q *query.Service, notify func(sessionID string)) *Recorder {
	return &Recorder{writer: w, query: q, notify: notify, seqs: map[string]int64{}, unattr: map[string]unattributed{}}
}

// SetSession directs subsequent events to sessionID ("" pauses recording).
// startSeq continues numbering for resumed sessions.
func (r *Recorder) SetSession(sessionID string, startSeq int64) {
	r.mu.Lock()
	r.sessionID = sessionID
	r.seq.Store(startSeq)
	clear(r.seqs)
	r.mu.Unlock()
}

// Session returns the active session ID.
func (r *Recorder) Session() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessionID
}

// SetOwnerLookup installs the package attribution function (guest source
// port → package name); nil disables attribution.
func (r *Recorder) SetOwnerLookup(f func(clientPort int) string) {
	r.ownerMu.Lock()
	r.owners = f
	r.ownerMu.Unlock()
}

func (r *Recorder) ownerOf(e *model.Event) string {
	if e.Conn == nil || e.Conn.ClientAddr == "" {
		return ""
	}
	r.ownerMu.RLock()
	f := r.owners
	r.ownerMu.RUnlock()
	if f == nil {
		return ""
	}
	_, port := model.SplitHostPort(e.Conn.ClientAddr, 0)
	if port == 0 {
		return ""
	}
	return f(port)
}

// Emit implements network.Sink.
func (r *Recorder) Emit(e *model.Event) {
	r.mu.Lock()
	sid := r.sessionID
	if sid == "" {
		r.mu.Unlock()
		return // no active session: nothing is recorded
	}
	seq, ok := r.seqs[e.ID]
	if !ok {
		seq = r.seq.Add(1)
	}
	if e.State == model.StatePending {
		r.seqs[e.ID] = seq
	} else {
		delete(r.seqs, e.ID)
	}
	r.mu.Unlock()

	e.SessionID, e.Seq = sid, seq
	if e.Package == "" && e.Initiator != model.InitiatorReplay {
		e.Package = r.ownerOf(e)
		r.rememberUnattributed(e)
	}
	r.writer.Submit(e)
	r.query.Apply(sid, e.Summary())
	if r.notify != nil {
		r.notify(sid)
	}
}

const maxUnattributed = 10000

func (r *Recorder) rememberUnattributed(e *model.Event) {
	r.unattrMu.Lock()
	defer r.unattrMu.Unlock()
	if e.Package != "" {
		delete(r.unattr, e.ID)
		return
	}
	if e.Conn == nil {
		return
	}
	_, port := model.SplitHostPort(e.Conn.ClientAddr, 0)
	if port == 0 || len(r.unattr) >= maxUnattributed {
		return
	}
	r.unattr[e.ID] = unattributed{e: e, port: port, seen: time.Now()}
}

// Reattribute assigns owners that became known after an event was recorded
// (the socket table is polled, so very short connections can finish first).
// Events older than 30 s are given up.
func (r *Recorder) Reattribute() {
	r.ownerMu.RLock()
	f := r.owners
	r.ownerMu.RUnlock()
	if f == nil {
		return
	}
	var fixed []*model.Event
	r.unattrMu.Lock()
	for id, u := range r.unattr {
		if pkg := f(u.port); pkg != "" {
			cp := *u.e
			cp.Package = pkg
			fixed = append(fixed, &cp)
			delete(r.unattr, id)
		} else if time.Since(u.seen) > 30*time.Second {
			delete(r.unattr, id)
		}
	}
	r.unattrMu.Unlock()
	for _, e := range fixed {
		if e.SessionID != r.Session() {
			continue
		}
		r.writer.Submit(e)
		r.query.Apply(e.SessionID, e.Summary())
		if r.notify != nil {
			r.notify(e.SessionID)
		}
	}
}

// Frame implements network.Sink.
func (r *Recorder) Frame(eventID string, f model.WSFrame) {
	if r.Session() == "" {
		return
	}
	r.writer.SubmitFrame(eventID, f)
}
