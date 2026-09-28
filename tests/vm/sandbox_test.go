//go:build vm

// Package vm contains integration tests that boot the real Android runtime in
// QEMU. They need the runtime layout (APKINSPECTOR_RUNTIME_DIR, containing
// android/x86_64/runtime.json + images, and optionally qemu/) and the TestApp
// (APKINSPECTOR_TESTAPP, built by tools/testapp/build.sh). Run:
//
//	go test -tags vm -timeout 90m ./tests/vm/ -v
package vm

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/core"
	"github.com/droidpector/apkinspector/src/display"
	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/query"
	"github.com/droidpector/apkinspector/src/testserver"
)

const testHost = "test.apkinspector.internal"

type harness struct {
	t   *testing.T
	app *core.App
	srv *testserver.Server
}

func env(t *testing.T, name string) string {
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s not set", name)
	}
	abs, _ := filepath.Abs(v)
	return abs
}

func newHarness(t *testing.T, dataDir string) *harness {
	t.Helper()
	runtimeDir := env(t, "APKINSPECTOR_RUNTIME_DIR")
	srv, err := testserver.Start("", "", []string{testHost})
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(dataDir, "testserver-ca.pem")
	os.MkdirAll(dataDir, 0o700)
	os.WriteFile(caFile, srv.CAPEM, 0o600)
	paths := platform.NewPaths(dataDir, runtimeDir, dataDir)
	cfg := platform.DefaultConfig()
	cfg.HostMappings = []string{testHost + "=" + srv.HTTPSAddr}
	cfg.ExtraRootsPEM = caFile
	cfg.BootTimeoutSec = 3600
	if runtime.GOARCH == "arm64" { // x86 guest emulated on an ARM host
		cfg.CPUs = 4
	}
	logs, err := platform.OpenLoggers(paths.LogDir, slog.LevelDebug, testing.Verbose())
	if err != nil {
		t.Fatal(err)
	}
	app, err := core.NewApp("test", paths, cfg, logs)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, app: app, srv: srv}
	t.Cleanup(func() {
		app.Close()
		logs.Close()
		srv.Close()
	})
	return h
}

func (h *harness) waitState(want core.State, timeout time.Duration) core.Status {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := h.app.Sandbox.Status()
		if st.State == want {
			return st
		}
		time.Sleep(time.Second)
	}
	st := h.app.Sandbox.Status()
	h.t.Fatalf("state %s not reached; now %s (%s) error=%+v", want, st.State, st.Message, st.Error)
	return st
}

func (h *harness) waitEvent(what, filter string, timeout time.Duration) model.Summary {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := h.app.Sandbox.Status()
		p, err := h.app.Query.Query(context.Background(), query.Request{SessionID: st.SessionID, Filter: filter, Limit: 100})
		if err == nil {
			for _, r := range p.Rows {
				if r.State == model.StateComplete {
					return r
				}
			}
		}
		time.Sleep(time.Second)
	}
	h.dumpDiagnostics()
	h.t.Fatalf("no event for %s (%s)", what, filter)
	return model.Summary{}
}

// dumpDiagnostics logs everything that helps explain a missing event.
func (h *harness) dumpDiagnostics() {
	st := h.app.Sandbox.Status()
	h.t.Logf("status: %+v", st)
	if p, err := h.app.Query.Query(context.Background(), query.Request{SessionID: st.SessionID, Limit: 200}); err == nil {
		for _, r := range p.Rows {
			h.t.Logf("  captured: %s %s %s://%s%s %d pkg=%s err=%s", r.Kind, r.Method, r.Scheme, r.Host, r.Path, r.Status, r.Package, r.Error)
		}
	}
	if dev, err := h.app.Sandbox.Device(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, c := range []string{
			"uiautomator dump /sdcard/ui.xml >/dev/null && grep -o 'text=\"[^\"]*\"' /sdcard/ui.xml | head -20",
			"logcat -d -t 300 | grep -iE 'testapp|AndroidRuntime|ssl|cert' | tail -40",
			"ls /system/etc/security/cacerts | wc -l; grep cacerts /proc/mounts",
		} {
			out, _, _ := dev.Run(ctx, c)
			h.t.Logf("$ %s\n%s", c, out)
		}
	}
}

func TestSandboxEndToEnd(t *testing.T) {
	apkPath := env(t, "APKINSPECTOR_TESTAPP")
	dataDir := t.TempDir()
	h := newHarness(t, dataDir)
	ctx := context.Background()

	// Start VM → Android boots → ADB connects → CA installed.
	start := time.Now()
	if err := h.app.Sandbox.Start(ctx, ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := h.app.Sandbox.Status()
	t.Logf("sandbox ready in %v (accel=%s)", time.Since(start).Round(time.Second), st.Accelerator)
	if st.State != core.StateReady || !st.CaptureActive || !st.HTTPSInspect || st.SessionID == "" {
		t.Fatalf("status after start: %+v", st)
	}

	// Install APK → launch → network event appears.
	f, err := os.Open(apkPath)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := h.app.APKs.Add(f, "TestApp.apk")
	f.Close()
	if err != nil || !entry.Valid || entry.Info.Package != "com.apkinspector.testapp" {
		t.Fatalf("apk: %+v %v", entry, err)
	}
	if err := h.app.APKs.Install(ctx, entry.ID); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := h.app.APKs.Launch(ctx, "com.apkinspector.testapp"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	dev, _ := h.app.Sandbox.Device()
	out, code, err := dev.Run(ctx, "am start -S -W -n com.apkinspector.testapp/.MainActivity --es action get")
	if err != nil || code != 0 {
		t.Fatalf("trigger: %s %v", out, err)
	}
	ev := h.waitEvent("GET from TestApp", "path:/test/get host:"+testHost, 3*time.Minute)
	if ev.Status != 200 || ev.Scheme != "https" || ev.Protocol == "" {
		t.Fatalf("event: %+v", ev)
	}
	if ev.Package != "com.apkinspector.testapp" {
		t.Errorf("package attribution: got %q", ev.Package)
	}
	full, err := h.app.Store.Event(ctx, ev.ID)
	if err != nil || full.TLS == nil || !full.TLS.Intercepted || full.RequestHeaders.Get("X-TestApp") != "1" {
		t.Fatalf("detail: %+v %v", full, err)
	}

	// Click the POST button through the embedded display (the path the UI uses).
	h.tapText(ctx, "POST")
	post := h.waitEvent("POST via display click", "method:POST path:/test/post", 3*time.Minute)
	if post.Status != 201 {
		t.Fatalf("POST event: %+v", post)
	}

	// Replay from the host.
	rep, err := h.app.Replay(ctx, ev.ID)
	if err != nil || rep.Status != 200 || rep.Initiator != model.InitiatorReplay {
		t.Fatalf("replay: %+v %v", rep, err)
	}

	// Crash recovery: kill QEMU; the sandbox must detect, restart and capture again.
	killQEMU(t)
	// Recovery can be fast (quick-start snapshot), so assert on its outcome:
	// the sandbox is ready again and reports that it recovered from a crash.
	deadline := time.Now().Add(60 * time.Minute)
	for {
		st := h.app.Sandbox.Status()
		recovered := false
		for _, w := range st.Warnings {
			recovered = recovered || strings.Contains(w, "recovered from a crash")
		}
		if recovered && st.State == core.StateReady {
			break
		}
		if st.State == core.StateError || time.Now().After(deadline) {
			t.Fatalf("no recovery after QEMU was killed: %+v", st)
		}
		time.Sleep(time.Second)
	}
	dev, err = h.app.Sandbox.Device()
	if err != nil {
		t.Fatal(err)
	}
	if out, code, err := dev.Run(ctx, "am start -S -W -n com.apkinspector.testapp/.MainActivity --es action json"); err != nil || code != 0 {
		t.Fatalf("trigger after recovery: %s %v", out, err)
	}
	h.waitEvent("JSON after recovery", "path:/test/json", 3*time.Minute)

	// After a crash the VM cold boots from its current disk: the app survives.
	if ok, _ := dev.IsInstalled(ctx, "com.apkinspector.testapp"); !ok {
		t.Fatal("installed app lost after crash recovery")
	}

	// Stop (saves the quick-start state), then start again: resumes quickly
	// with the app still installed.
	if err := h.app.Sandbox.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := h.app.Sandbox.Start(ctx, ""); err != nil {
		t.Fatalf("second start: %v", err)
	}
	t.Logf("second start (quick-start state) in %v", time.Since(start).Round(time.Second))
	dev, err = h.app.Sandbox.Device()
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := dev.IsInstalled(ctx, "com.apkinspector.testapp"); !ok {
		t.Fatal("installed app lost across stop/start")
	}
	if out, code, err := dev.Run(ctx, "am start -S -W -n com.apkinspector.testapp/.MainActivity --es action image"); err != nil || code != 0 {
		t.Fatalf("trigger after quick start: %s %v", out, err)
	}
	h.waitEvent("image after quick start", "path:/test/image", 3*time.Minute)
}

var boundsRe = regexp.MustCompile(`text="([^"]*)"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"`)

// tapText finds a view by its text with uiautomator and clicks its centre by
// sending pointer events through the display streamer, exactly like the UI.
func (h *harness) tapText(ctx context.Context, text string) {
	h.t.Helper()
	dev, err := h.app.Sandbox.Device()
	if err != nil {
		h.t.Fatal(err)
	}
	var x, y int
	deadline := time.Now().Add(2 * time.Minute)
	for x == 0 && time.Now().Before(deadline) {
		out, _, err := dev.Run(ctx, "uiautomator dump /sdcard/ui.xml >/dev/null 2>&1; cat /sdcard/ui.xml")
		if err == nil {
			for _, m := range boundsRe.FindAllStringSubmatch(out, -1) {
				if strings.EqualFold(m[1], text) {
					x1, _ := strconv.Atoi(m[2])
					y1, _ := strconv.Atoi(m[3])
					x2, _ := strconv.Atoi(m[4])
					y2, _ := strconv.Atoi(m[5])
					x, y = (x1+x2)/2, (y1+y2)/2
				}
			}
		}
		if x == 0 {
			time.Sleep(2 * time.Second)
		}
	}
	if x == 0 {
		h.t.Fatalf("view %q not found on screen", text)
	}
	for i := 0; i < 50 && !h.app.Streamer.Connected(); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	for _, in := range []display.Input{{Type: "p", X: x, Y: y}, {Type: "p", X: x, Y: y, Buttons: 1}, {Type: "p", X: x, Y: y}} {
		if err := h.app.Streamer.Send(in); err != nil {
			h.t.Fatalf("display input: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func killQEMU(t *testing.T) {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("taskkill", "/F", "/IM", "qemu-system-x86_64.exe")
	} else {
		cmd = exec.Command("pkill", "-9", "-f", "droidpector-x86_64")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("killing QEMU: %v %s", err, out)
	}
}
