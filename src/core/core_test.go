package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/query"
	"github.com/droidpector/apkinspector/src/storage"
	"github.com/droidpector/apkinspector/src/vm"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fixture struct {
	store    *storage.Store
	writer   *storage.Writer
	query    *query.Service
	rec      *Recorder
	sessions *Sessions
	notified sync.Map
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: st}
	f.writer = storage.NewWriter(st, storage.WriterOptions{}, quietLog())
	f.query = query.NewService(st, 3)
	f.rec = NewRecorder(f.writer, f.query, func(sid string) { f.notified.Store(sid, true) })
	f.sessions = NewSessions(st, f.writer, f.query, f.rec, storage.RetentionPolicy{MaxSessions: 10}, "test")
	t.Cleanup(func() {
		f.writer.Close()
		st.Close()
	})
	return f
}

func ev(id string, state model.State, port int) *model.Event {
	return &model.Event{ID: id, Kind: model.KindHTTP, Category: model.CatAPI, State: state, StartedAt: time.Now(),
		Method: "GET", Scheme: "https", Host: "api.example.com", Port: 443, Path: "/x",
		Conn: &model.Conn{ClientAddr: "10.0.2.15:" + itoa(port)}}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRecorderStampsSessionSeqAndPackage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Without a session nothing is recorded.
	f.rec.Emit(ev("orphan", model.StateComplete, 1))
	sess, err := f.sessions.Start(ctx, "x86_64")
	if err != nil {
		t.Fatal(err)
	}
	f.rec.SetOwnerLookup(func(port int) string {
		if port == 40000 {
			return "com.example.app"
		}
		return ""
	})
	f.rec.Emit(ev("a", model.StatePending, 40000))
	f.rec.Emit(ev("b", model.StateComplete, 40001))
	f.rec.Emit(ev("a", model.StateComplete, 40000)) // completion keeps seq 1
	replay := ev("r", model.StateComplete, 40000)
	replay.Initiator = model.InitiatorReplay
	f.rec.Emit(replay)
	f.writer.Flush(ctx)

	p, err := f.query.Query(ctx, query.Request{SessionID: sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 || p.Rows[0].ID != "a" || p.Rows[0].Seq != 1 || p.Rows[0].State != model.StateComplete || p.Rows[1].Seq != 2 {
		t.Fatalf("rows: %+v", p.Rows)
	}
	if p.Rows[0].Package != "com.example.app" || p.Rows[1].Package != "" || p.Rows[2].Package != "" {
		t.Fatalf("attribution: %q %q %q", p.Rows[0].Package, p.Rows[1].Package, p.Rows[2].Package)
	}
	if _, ok := f.notified.Load(sess.ID); !ok {
		t.Fatal("hub not notified")
	}
	if rows, _ := f.store.Summaries(ctx, sess.ID); len(rows) != 3 {
		t.Fatalf("persisted %d", len(rows))
	}
}

func TestSessionsLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s1, _ := f.sessions.Start(ctx, "x86_64")
	f.sessions.SetAPK(ctx, "example.apk", "com.example.app")
	f.rec.Emit(ev("e1", model.StateComplete, 1))
	s2, _ := f.sessions.Start(ctx, "x86_64") // ends s1
	if f.sessions.Active().ID != s2.ID {
		t.Fatal("new session not active")
	}
	got, _ := f.sessions.Get(ctx, s1.ID)
	if got.EndedAt == nil || got.APK != "example.apk" || got.Requests != 1 {
		t.Fatalf("ended session: %+v", got)
	}
	if err := f.sessions.Delete(ctx, s2.ID); err == nil {
		t.Fatal("active session must not be deletable")
	}
	saved, err := f.sessions.Save(ctx, s1.ID, "login flow")
	if err != nil || !saved.Saved || saved.Name != "login flow" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	var buf bytes.Buffer
	n, err := f.sessions.ExportHAR(ctx, s1.ID, &buf)
	if err != nil || n != 1 || !json.Valid(buf.Bytes()) {
		t.Fatalf("har: %d %v", n, err)
	}
	if err := f.sessions.Clear(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.sessions.Get(ctx, s1.ID); got.Requests != 0 {
		t.Fatal("clear failed")
	}
	f.sessions.End(ctx)
	if f.sessions.Active() != nil || f.rec.Session() != "" {
		t.Fatal("end failed")
	}
	if err := f.sessions.Delete(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := f.sessions.List(ctx)
	if len(list) != 1 {
		t.Fatalf("list %d", len(list))
	}
}

func TestShouldRecoverBudget(t *testing.T) {
	now := time.Now()
	var h []time.Time
	var ok bool
	for i := 0; i < maxCrashes; i++ {
		h, ok = shouldRecover(h, now.Add(time.Duration(i)*time.Minute))
		if !ok {
			t.Fatalf("crash %d should be recovered", i+1)
		}
	}
	if _, ok = shouldRecover(h, now.Add(5*time.Minute)); ok {
		t.Fatal("4th crash within the window must stop auto-restart")
	}
	if h2, ok := shouldRecover(h, now.Add(time.Hour)); !ok || len(h2) != 1 {
		t.Fatal("old crashes must expire")
	}
}

func TestHubCoalescesEventNotifications(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx, func(string) (query.Stats, error) { return query.Stats{Requests: 7}, nil })
	h.StatusChanged(Status{State: StateReady, Message: "Network Capture Active"})
	ch := h.Subscribe() // late subscriber still receives the latest status
	defer h.Unsubscribe(ch)
	var m Message
	json.Unmarshal(<-ch, &m)
	if m.Type != "status" || m.Status.State != StateReady {
		t.Fatalf("first message: %+v", m)
	}
	for i := 0; i < 1000; i++ {
		h.EventsChanged("s1")
	}
	select {
	case b := <-ch:
		json.Unmarshal(b, &m)
		if m.Type != "events" || m.SessionID != "s1" || m.Stats.Requests != 7 {
			t.Fatalf("events message: %s", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no events notification")
	}
	select {
	case b := <-ch:
		t.Fatalf("1000 changes must coalesce into one message, got another: %s", b)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestToErrorInfo(t *testing.T) {
	se := &vm.StartError{Code: vm.CodeAccelUnavailable, Title: "Hardware acceleration is not available.", Causes: []string{"x"}, Details: "d"}
	if info := toErrorInfo(se); info.Code != vm.CodeAccelUnavailable || len(info.Causes) != 1 {
		t.Fatal(info)
	}
	if info := toErrorInfo(&UserError{Title: "T", Code: "c"}); info.Title != "T" {
		t.Fatal(info)
	}
	if info := toErrorInfo(errors.New("boom")); !strings.Contains(info.Title, "boom") || strings.Contains(strings.ToLower(info.Title), "unknown") {
		t.Fatal(info)
	}
}

func testSandbox(t *testing.T, f *fixture) *Sandbox {
	procs, _ := platform.NewProcessGroup()
	return NewSandbox(SandboxConfig{Profiles: map[string]vm.Profile{vm.ProfileX86_64: {Name: vm.ProfileX86_64, Translation: true}}, AppOnly: true},
		quietLog(), quietLog(), procs, nil, f.sessions, f.rec, nil, nil)
}

func TestAPKsAddValidates(t *testing.T) {
	f := newFixture(t)
	apks := NewAPKs(t.TempDir(), testSandbox(t, f), f.sessions)
	if _, err := apks.Add(strings.NewReader("x"), "notes.txt"); err == nil {
		t.Fatal("non-APK extension accepted")
	}
	e, err := apks.Add(strings.NewReader("not a zip"), `C:\Users\x\evil.apk`)
	if err != nil {
		t.Fatal(err)
	}
	if e.Valid || len(e.Problems) == 0 || e.FileName != "evil.apk" {
		t.Fatalf("invalid APK accepted: %+v", e)
	}
	if err := apks.Install(context.Background(), e.ID); err == nil {
		t.Fatal("installing an invalid APK must fail")
	}
	if _, err := apks.Get("nope"); err == nil {
		t.Fatal("unknown id")
	}
	// A real APK built by tools/apkbuild (if present) is analysed correctly.
	if b, err := os.ReadFile(filepath.Join("..", "..", "build", "testapp", "TestApp.apk")); err == nil {
		e, err := apks.Add(bytes.NewReader(b), "TestApp.apk")
		if err != nil || !e.Valid || e.Info.Package != "com.apkinspector.testapp" || e.Runtime.Runtime != vm.ProfileX86_64 {
			t.Fatalf("TestApp: %+v %v", e, err)
		}
	}
	apks.Cleanup()
}

func TestSandboxRefusesOperationsWhenStopped(t *testing.T) {
	f := newFixture(t)
	s := testSandbox(t, f)
	if _, err := s.Device(); err == nil {
		t.Fatal("device must be unavailable")
	}
	if err := s.SaveSnapshot(context.Background(), "ok_name"); err == nil {
		t.Fatal("snapshot without VM")
	}
	for _, bad := range []string{"", "has space", "quickstart", strings.Repeat("a", 41), "../x"} {
		if err := checkTag(bad); err == nil {
			t.Errorf("tag %q accepted", bad)
		}
	}
	if st := s.Status(); st.State != StateStopped {
		t.Fatal(st.State)
	}
	if err := s.Start(context.Background(), "arm64"); err == nil || s.Status().Error == nil || s.Status().Error.Code != "runtime_missing" {
		t.Fatalf("missing runtime must produce a clear error: %v %+v", err, s.Status().Error)
	}
}

func TestAppOnlyToggleWithoutVM(t *testing.T) {
	f := newFixture(t)
	s := testSandbox(t, f)
	if !s.Status().AppOnly {
		t.Fatal("app-only should default to the configured value")
	}
	s.SetAppOnly(context.Background(), false)
	if s.Status().AppOnly {
		t.Fatal("toggle off not reflected in status")
	}
	s.SetAppOnly(context.Background(), true)
	if !s.Status().AppOnly {
		t.Fatal("toggle on not reflected in status")
	}
	if err := s.AllowPackage(context.Background(), "com.x"); err == nil {
		t.Fatal("allowing a package without a running device must fail clearly")
	}
	s.DisallowPackage(context.Background(), "com.x") // no-op when unknown
}
