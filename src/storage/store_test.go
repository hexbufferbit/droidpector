package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

var ctx = context.Background()

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newSession(t *testing.T, s *Store) *model.Session {
	t.Helper()
	sess := &model.Session{APK: "example.apk", Package: "com.example.app", Runtime: "x86_64"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func httpEvent(sessionID string, seq int64, host string) *model.Event {
	return &model.Event{
		ID: model.NewID(), SessionID: sessionID, Seq: seq, Kind: model.KindHTTP, Category: model.CatAPI,
		State: model.StateComplete, StartedAt: time.Now().Truncate(time.Microsecond), Method: "POST", Scheme: "https",
		Host: host, Port: 443, Path: "/v1/login", Status: 200, StatusText: "OK", MIME: "application/json",
		RequestSize: 10, ResponseSize: 20, Protocol: "HTTP/1.1",
		RequestHeaders: model.Headers{{Name: "Content-Type", Value: "application/json"}},
		Timing:         &model.Timing{Send: 1, Wait: 2, Receive: 3, DNS: -1, Connect: -1, TLS: -1, Blocked: -1},
		TLS:            &model.TLSInfo{SNI: host, Intercepted: true},
	}
}

func TestBlobStoreRoundTripDedupAndCompression(t *testing.T) {
	b, err := OpenBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	small := []byte("tiny")
	big := bytes.Repeat([]byte(`{"k":"v"},`), 10000) // compressible
	h1, err := b.Put(small)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := b.Put(big)
	h2b, _ := b.Put(big)
	if h2 != h2b {
		t.Fatal("identical content must share a hash")
	}
	for h, want := range map[string][]byte{h1: small, h2: big} {
		got, err := b.Get(h)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("Get(%s) failed: %v", h[:8], err)
		}
	}
	if _, err := os.Stat(b.path(h2, true)); err != nil {
		t.Fatal("compressible body should be stored compressed")
	}
	if _, err := os.Stat(b.path(h1, false)); err != nil {
		t.Fatal("small body should be stored raw")
	}
	if b.DiskUsage() >= int64(len(big)) {
		t.Fatal("compression had no effect")
	}
	if _, err := b.Get(strings.Repeat("0", 64)); !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("missing blob: %v", err)
	}
	if _, err := b.Get("../../etc/passwd"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	// Corruption is detected.
	os.WriteFile(b.path(h1, false), []byte("tampered"), 0o600)
	if _, err := b.Get(h1); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampering not detected: %v", err)
	}
}

func TestBlobSweep(t *testing.T) {
	b, _ := OpenBlobStore(t.TempDir())
	defer b.Close()
	keep, _ := b.Put([]byte("keep"))
	drop, _ := b.Put([]byte("drop"))
	fresh, _ := b.Put([]byte("fresh"))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(b.path(keep, false), old, old)
	os.Chtimes(b.path(drop, false), old, old)
	n, _, err := b.Sweep(map[string]bool{keep: true}, 10*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if _, err := b.Get(drop); !errors.Is(err, ErrBlobNotFound) {
		t.Fatal("unreferenced blob survived")
	}
	for _, h := range []string{keep, fresh} {
		if _, err := b.Get(h); err != nil {
			t.Fatalf("blob %s wrongly swept", h[:6])
		}
	}
}

func TestSessionsCRUDAndStats(t *testing.T) {
	s := openTemp(t)
	a := newSession(t, s)
	b := newSession(t, s)
	if a.Number != 1 || b.Number != 2 {
		t.Fatalf("session numbers %d %d", a.Number, b.Number)
	}
	evs := []*model.Event{httpEvent(a.ID, 1, "api.example.com"), httpEvent(a.ID, 2, "api.example.com"), httpEvent(a.ID, 3, "cdn.example.com")}
	dns := &model.Event{ID: model.NewID(), SessionID: a.ID, Seq: 4, Kind: model.KindDNS, Category: model.CatDNS, State: model.StateComplete, Host: "x.example.com", StartedAt: time.Now()}
	if err := s.PutEvents(ctx, append(evs, dns)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Session(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 4 || got.Domains != 2 || got.Bytes != 90 || got.APK != "example.apk" {
		t.Fatalf("stats: %+v", got)
	}
	end := time.Now()
	got.EndedAt, got.Saved, got.Name = &end, true, "login flow"
	if err := s.UpdateSession(ctx, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Session(ctx, a.ID)
	if got2.EndedAt == nil || !got2.Saved || got2.Name != "login flow" {
		t.Fatalf("update lost: %+v", got2)
	}
	list, _ := s.Sessions(ctx)
	if len(list) != 2 || list[0].ID != b.ID {
		t.Fatal("sessions must be newest first")
	}
	if err := s.ClearSession(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Session(ctx, a.ID); got.Requests != 0 {
		t.Fatal("clear failed")
	}
	if err := s.DeleteSession(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete failed")
	}
	if err := s.DeleteSession(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("double delete should be ErrNotFound")
	}
	if err := s.UpdateSession(ctx, &model.Session{ID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("update of unknown session should be ErrNotFound")
	}
}

func TestEventRoundTripAndUpsert(t *testing.T) {
	s := openTemp(t)
	sess := newSession(t, s)
	body := []byte(`{"ok":true}`)
	h, _ := s.Blobs().Put(body)
	e := httpEvent(sess.ID, 1, "api.example.com")
	e.State = model.StatePending
	if err := s.PutEvents(ctx, []*model.Event{e}); err != nil {
		t.Fatal(err)
	}
	// Completion upserts the same ID.
	done := *e
	done.State = model.StateComplete
	done.Status = 201
	done.ResponseBody = &model.BodyRef{Hash: h, Size: int64(len(body)), Stored: int64(len(body))}
	done.ResponseHeaders = model.Headers{{Name: "Content-Type", Value: "application/json"}}
	if err := s.PutEvents(ctx, []*model.Event{&done}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Event(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateComplete || got.Status != 201 || got.StatusText != "OK" || got.TLS == nil || !got.TLS.Intercepted ||
		got.ResponseHeaders.Get("content-type") != "application/json" || got.Timing.Wait != 2 || !got.StartedAt.Equal(e.StartedAt) {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	b, err := s.ReadBody(got.ResponseBody)
	if err != nil || !bytes.Equal(b, body) {
		t.Fatalf("body: %q %v", b, err)
	}
	sums, _ := s.Summaries(ctx, sess.ID)
	if len(sums) != 1 || sums[0].Status != 201 {
		t.Fatalf("summaries: %+v", sums)
	}
	if n, _ := s.MaxSeq(ctx, sess.ID); n != 1 {
		t.Fatal("MaxSeq")
	}
	if _, err := s.Event(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing event")
	}
	refs, _ := s.ReferencedBlobs(ctx)
	if !refs[h] {
		t.Fatal("referenced blob not reported")
	}
}

func TestWebSocketFrames(t *testing.T) {
	s := openTemp(t)
	sess := newSession(t, s)
	ws := &model.Event{ID: model.NewID(), SessionID: sess.ID, Seq: 1, Kind: model.KindWebSocket, Category: model.CatWebSocket, State: model.StateComplete, StartedAt: time.Now(), Host: "rt"}
	s.PutEvents(ctx, []*model.Event{ws})
	w := NewWriter(s, WriterOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer w.Close()
	for i := 0; i < 5; i++ {
		w.SubmitFrame(ws.ID, model.WSFrame{Seq: int64(i), Time: time.Now(), Outgoing: i%2 == 0, Opcode: 1, Length: 2, Data: []byte("hi")})
	}
	w.SubmitFrame("unknown-event", model.WSFrame{Seq: 1, Time: time.Now()}) // must not poison the batch
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Event(ctx, ws.ID)
	if len(got.Frames) != 5 || !got.Frames[0].Outgoing || string(got.Frames[4].Data) != "hi" {
		t.Fatalf("frames: %+v", got.Frames)
	}
	if st := w.Stats(); st.Failed != 0 {
		t.Fatalf("writer failed: %+v", st)
	}
	page, _ := s.Frames(ctx, ws.ID, 3, 10)
	if len(page) != 2 {
		t.Fatal("frame paging")
	}
}

func TestWriterBatchesCoalescesAndIsConcurrent(t *testing.T) {
	s := openTemp(t)
	sess := newSession(t, s)
	w := NewWriter(s, WriterOptions{BatchSize: 64}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var wg sync.WaitGroup
	const producers, per = 8, 250
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				e := httpEvent(sess.ID, int64(p*per+i+1), "h")
				pending := *e
				pending.State = model.StatePending
				w.Submit(&pending)
				w.Submit(e)
			}
		}(p)
	}
	wg.Wait()
	w.Close()
	st := w.Stats()
	if st.Dropped != 0 || st.Failed != 0 {
		t.Fatalf("stats: %+v", st)
	}
	sums, _ := s.Summaries(ctx, sess.ID)
	if len(sums) != producers*per {
		t.Fatalf("persisted %d events", len(sums))
	}
	for _, sm := range sums {
		if sm.State != model.StateComplete {
			t.Fatal("latest snapshot must win")
		}
	}
	if w.Submit(httpEvent(sess.ID, 99999, "late")) {
		t.Fatal("submit after close must be rejected")
	}
}

func TestWriterBackpressureDropsInsteadOfBlocking(t *testing.T) {
	s := openTemp(t)
	sess := newSession(t, s)
	// Hold a write transaction so the writer cannot commit.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('lock','1')`); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(s, WriterOptions{QueueSize: 4, BatchSize: 1, EnqueueTimeout: 5 * time.Millisecond}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	start := time.Now()
	accepted := 0
	for i := 0; i < 50; i++ {
		if w.Submit(httpEvent(sess.ID, int64(i+1), "h")) {
			accepted++
		}
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("producers were blocked beyond the enqueue timeout")
	}
	if w.Stats().Dropped == 0 || accepted == 50 {
		t.Fatalf("expected drops under backpressure: %+v", w.Stats())
	}
	tx.Rollback()
	w.Close()
}

func TestRetention(t *testing.T) {
	s := openTemp(t)
	now := time.Now()
	var ids []string
	for i := 0; i < 6; i++ {
		sess := &model.Session{StartedAt: now.Add(time.Duration(i-10) * 24 * time.Hour)}
		if i == 1 {
			sess.Saved = true
		}
		s.CreateSession(ctx, sess)
		ids = append(ids, sess.ID)
	}
	active := ids[0] // oldest but active: never deleted
	removed, err := s.ApplyRetention(ctx, RetentionPolicy{MaxSessions: 2}, active, now)
	if err != nil {
		t.Fatal(err)
	}
	// Unsaved, non-active: ids 2..5; keep newest two (5,4) → remove 3,2.
	if len(removed) != 2 || removed[0] != ids[3] || removed[1] != ids[2] {
		t.Fatalf("removed %v", removed)
	}
	removed, _ = s.ApplyRetention(ctx, RetentionPolicy{MaxAge: 5*24*time.Hour + time.Hour}, active, now)
	if len(removed) != 1 || removed[0] != ids[4] {
		t.Fatalf("age retention removed %v", removed)
	}
	list, _ := s.Sessions(ctx)
	if len(list) != 3 {
		t.Fatalf("remaining %d", len(list))
	}
}

func TestEventsIteratorAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	sess := newSession(t, s)
	for i := 1; i <= 3; i++ {
		s.PutEvents(ctx, []*model.Event{httpEvent(sess.ID, int64(i), "h")})
	}
	n := 0
	for e, err := range s.Events(ctx, sess.ID) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if e.Seq != int64(n) {
			t.Fatal("order")
		}
	}
	if n != 3 {
		t.Fatal(n)
	}
	s.Close()
	// Re-open: migrations are idempotent and data persists.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.Integrity(ctx); err != nil {
		t.Fatal(err)
	}
	if sums, _ := s2.Summaries(ctx, sess.ID); len(sums) != 3 {
		t.Fatal("data lost on reopen")
	}
}

func TestNewerSchemaRejected(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.db.Exec(`UPDATE meta SET value='99' WHERE key='schema_version'`)
	s.Close()
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("expected newer-schema error, got %v", err)
	}
}
