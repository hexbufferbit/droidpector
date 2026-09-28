package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/export"
	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/query"
	"github.com/droidpector/apkinspector/src/storage"
)

// Sessions manages the session lifecycle: one session per sandbox run.
type Sessions struct {
	store     *storage.Store
	writer    *storage.Writer
	query     *query.Service
	rec       *Recorder
	retention storage.RetentionPolicy
	version   string

	mu     sync.Mutex
	active *model.Session
}

// NewSessions creates the session manager.
func NewSessions(store *storage.Store, writer *storage.Writer, q *query.Service, rec *Recorder, retention storage.RetentionPolicy, version string) *Sessions {
	return &Sessions{store: store, writer: writer, query: q, rec: rec, retention: retention, version: version}
}

// ErrNoSession is returned when an operation needs an active session.
var ErrNoSession = errors.New("no capture session is active; start the sandbox first")

// Start begins a new session and routes capture into it. Any active session
// is ended first.
func (s *Sessions) Start(ctx context.Context, runtime string) (*model.Session, error) {
	s.End(ctx)
	sess := &model.Session{Runtime: runtime, StartedAt: time.Now()}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("creating a capture session: %w", err)
	}
	if err := s.query.Track(ctx, sess.ID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.active = sess
	s.mu.Unlock()
	s.rec.SetSession(sess.ID, 0)
	go s.store.ApplyRetention(context.WithoutCancel(ctx), s.retention, sess.ID, time.Now())
	return sess, nil
}

// End stops capturing into the active session and records its end time.
func (s *Sessions) End(ctx context.Context) {
	s.mu.Lock()
	sess := s.active
	s.active = nil
	s.mu.Unlock()
	if sess == nil {
		return
	}
	s.rec.SetSession("", 0)
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	s.writer.Flush(fctx)
	now := time.Now()
	sess.EndedAt = &now
	s.store.UpdateSession(fctx, sess)
}

// Active returns the active session (nil if none).
func (s *Sessions) Active() *model.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return nil
	}
	cp := *s.active
	return &cp
}

// SetAPK records the APK under test on the active session.
func (s *Sessions) SetAPK(ctx context.Context, fileName, pkg string) {
	s.mu.Lock()
	sess := s.active
	if sess != nil {
		sess.APK, sess.Package = fileName, pkg
	}
	s.mu.Unlock()
	if sess != nil {
		s.store.UpdateSession(ctx, sess)
	}
}

// Get returns a session with live statistics.
func (s *Sessions) Get(ctx context.Context, id string) (*model.Session, error) {
	sess, err := s.store.Session(ctx, id)
	if err != nil {
		return nil, err
	}
	if st, err := s.query.Stats(ctx, id); err == nil {
		sess.Requests, sess.Domains, sess.Bytes = st.Requests, st.Domains, st.Bytes
	}
	return sess, nil
}

// List returns all sessions, newest first.
func (s *Sessions) List(ctx context.Context) ([]*model.Session, error) {
	list, err := s.store.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	if a := s.Active(); a != nil {
		for _, sess := range list {
			if sess.ID == a.ID {
				if st, err := s.query.Stats(ctx, a.ID); err == nil {
					sess.Requests, sess.Domains, sess.Bytes = st.Requests, st.Domains, st.Bytes
				}
			}
		}
	}
	return list, nil
}

// Clear removes all events of a session (the session itself stays).
func (s *Sessions) Clear(ctx context.Context, id string) error {
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.writer.Flush(fctx)
	if err := s.store.ClearSession(ctx, id); err != nil {
		return err
	}
	s.query.Invalidate(id)
	if a := s.Active(); a != nil && a.ID == id {
		s.query.Track(ctx, id)
	}
	return nil
}

// Delete removes a session (not the active one).
func (s *Sessions) Delete(ctx context.Context, id string) error {
	if a := s.Active(); a != nil && a.ID == id {
		return errors.New("the active session cannot be deleted; stop the sandbox or clear it instead")
	}
	if err := s.store.DeleteSession(ctx, id); err != nil {
		return err
	}
	s.query.Invalidate(id)
	go s.store.SweepBlobs(context.WithoutCancel(ctx), 10*time.Minute)
	return nil
}

// Save marks a session as kept (exempt from retention) and names it.
func (s *Sessions) Save(ctx context.Context, id, name string) (*model.Session, error) {
	sess, err := s.store.Session(ctx, id)
	if err != nil {
		return nil, err
	}
	sess.Saved = true
	if name != "" {
		sess.Name = name
	}
	s.mu.Lock()
	if s.active != nil && s.active.ID == id {
		s.active.Saved, s.active.Name = true, sess.Name
	}
	s.mu.Unlock()
	return sess, s.store.UpdateSession(ctx, sess)
}

// ExportHAR streams a session as HAR 1.2.
func (s *Sessions) ExportHAR(ctx context.Context, id string, w io.Writer) (int, error) {
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	s.writer.Flush(fctx)
	cancel()
	if _, err := s.store.Session(ctx, id); err != nil {
		return 0, err
	}
	return export.WriteHAR(w, export.HAROptions{Creator: "droidpector", Version: s.version, IncludeBodies: true},
		s.store.Events(ctx, id), s.store)
}
