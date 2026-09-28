package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/android/apk"
	"github.com/droidpector/apkinspector/src/android/device"
	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/vm"
)

// MaxAPKSize bounds uploads.
const MaxAPKSize = 4 << 30

// APKEntry is an uploaded APK and its analysis.
type APKEntry struct {
	ID       string              `json:"id"`
	FileName string              `json:"fileName"`
	Size     int64               `json:"size"`
	Uploaded time.Time           `json:"uploaded"`
	Info     *apk.Info           `json:"info,omitempty"`
	Runtime  apk.RuntimeDecision `json:"runtime"`
	Valid    bool                `json:"valid"`
	Problems []apk.Issue         `json:"problems,omitempty"`
	path     string
}

// APKs is the APK Manager.
type APKs struct {
	dir      string
	sandbox  *Sandbox
	sessions *Sessions

	mu      sync.Mutex
	entries map[string]*APKEntry
}

// NewAPKs creates the APK manager storing uploads in dir (a temp folder that
// is emptied on every start).
func NewAPKs(dir string, sandbox *Sandbox, sessions *Sessions) *APKs {
	return &APKs{dir: dir, sandbox: sandbox, sessions: sessions, entries: map[string]*APKEntry{}}
}

// Add stores and analyzes an APK read from r.
func (a *APKs) Add(r io.Reader, fileName string) (*APKEntry, error) {
	base := filepath.Base(strings.ReplaceAll(fileName, `\`, "/"))
	if base == "" || base == "." || base == "/" {
		base = "app.apk"
	}
	if ext := strings.ToLower(filepath.Ext(base)); ext != ".apk" {
		return nil, &UserError{Code: "not_apk", Title: "Only .apk files can be installed.",
			Causes: []string{fmt.Sprintf("%q is not an APK. Split bundles (.apks/.xapk/.aab) must be converted to a universal APK first.", base)}}
	}
	id := model.NewID()
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(a.dir, id+".apk")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("storing the APK: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxAPKSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("receiving the APK: %w", err)
	}
	if n > MaxAPKSize {
		os.Remove(path)
		return nil, &UserError{Code: "too_large", Title: "The APK is larger than 4 GB and cannot be installed."}
	}
	e := &APKEntry{ID: id, FileName: base, Size: n, Uploaded: time.Now(), path: path}
	info, err := apk.Inspect(path)
	var ve *apk.ValidationError
	switch {
	case errors.As(err, &ve):
		e.Problems = ve.Issues
	case err != nil:
		e.Problems = []apk.Issue{{Severity: apk.SeverityError, Code: "unreadable", Message: "The APK could not be read: " + err.Error()}}
	default:
		e.Info = info
		e.Problems = info.Issues
		e.Valid = true
		for _, is := range info.Issues {
			if is.Severity == apk.SeverityError {
				e.Valid = false
			}
		}
		x86 := a.sandbox.cfg.Profiles[vm.ProfileX86_64]
		e.Runtime = info.RequiredRuntime(x86.Translation)
	}
	a.mu.Lock()
	a.entries[id] = e
	a.mu.Unlock()
	return e, nil
}

// Get returns an uploaded APK.
func (a *APKs) Get(id string) (*APKEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[id]
	if !ok {
		return nil, &UserError{Code: "unknown_apk", Title: "This APK is no longer available; add it again."}
	}
	return e, nil
}

// List returns uploaded APKs, newest first.
func (a *APKs) List() []*APKEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*APKEntry, 0, len(a.entries))
	for _, e := range a.entries {
		out = append(out, e)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Uploaded.After(out[j-1].Uploaded); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Run is the one-click flow: start (or switch) the sandbox as needed,
// install the APK and launch it.
func (a *APKs) Run(ctx context.Context, id string) error {
	e, err := a.Get(id)
	if err != nil {
		return err
	}
	if err := a.Install(ctx, id); err != nil {
		return err
	}
	return a.Launch(ctx, e.Info.Package)
}

// Install installs an uploaded APK, starting or switching the sandbox runtime
// when the APK's CPU architecture requires it.
func (a *APKs) Install(ctx context.Context, id string) error {
	e, err := a.Get(id)
	if err != nil {
		return err
	}
	if !e.Valid || e.Info == nil {
		msg := "The APK is invalid."
		var causes []string
		for _, p := range e.Problems {
			if p.Severity == apk.SeverityError {
				causes = append(causes, p.Message)
			}
		}
		return a.sandbox.fail(&UserError{Code: "invalid_apk", Title: msg, Causes: causes})
	}
	s := a.sandbox
	if !s.Running() || s.Profile().Name != e.Runtime.Runtime {
		if s.Running() {
			s.setState(StatePreparing, "Switching to the "+e.Runtime.Runtime+" runtime...")
		}
		if err := s.Start(ctx, e.Runtime.Runtime); err != nil {
			return err
		}
	}
	dev, err := s.Device()
	if err != nil {
		return s.fail(err)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.setState(StateInstalling, "Installing APK...")
	ictx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := dev.InstallAPK(ictx, e.path, device.InstallOptions{}); err != nil {
		var ie *device.InstallError
		if errors.As(err, &ie) {
			causes := []string{ie.Message()}
			if ie.Code == "INSTALL_FAILED_NO_MATCHING_ABIS" {
				causes = append(causes, e.Runtime.Reason)
			}
			return s.fail(&UserError{Code: ie.Code, Title: "The APK could not be installed.", Causes: causes, Details: ie.Raw})
		}
		return s.fail(&UserError{Code: "install_failed", Title: "The APK could not be installed.", Details: err.Error()})
	}
	a.sessions.SetAPK(ctx, e.FileName, e.Info.Package)
	s.setApp(&AppState{Package: e.Info.Package, Label: e.Info.Label, Version: e.Info.VersionName, File: e.FileName})
	s.update(func(st *Status) { st.State, st.Message = StateReady, "Network Capture Active" })
	return nil
}

// Launch starts an installed app.
func (a *APKs) Launch(ctx context.Context, pkg string) error {
	s := a.sandbox
	dev, err := s.Device()
	if err != nil {
		return err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.setState(StateLaunching, "Launching...")
	if _, err := dev.Launch(ctx, pkg); err != nil {
		s.update(func(st *Status) { st.State, st.Message = StateReady, "Network Capture Active" })
		return &UserError{Code: "launch_failed", Title: "The app could not be launched.", Details: err.Error(),
			Causes: []string{"The app has no launcher activity.", "The app crashed immediately after starting (see Android logs)."}}
	}
	s.mu.Lock()
	if s.app == nil || s.app.Package != pkg {
		s.app = &AppState{Package: pkg}
	}
	s.app.Running = true
	s.mu.Unlock()
	s.update(func(st *Status) { st.State, st.Message = StateReady, "Network Capture Active" })
	return nil
}

// Stop force-stops an app.
func (a *APKs) Stop(ctx context.Context, pkg string) error {
	dev, err := a.sandbox.Device()
	if err != nil {
		return err
	}
	if err := dev.ForceStop(ctx, pkg); err != nil {
		return err
	}
	a.markRunning(pkg, false)
	return nil
}

// ClearData wipes an app's data.
func (a *APKs) ClearData(ctx context.Context, pkg string) error {
	dev, err := a.sandbox.Device()
	if err != nil {
		return err
	}
	if err := dev.ClearData(ctx, pkg); err != nil {
		return err
	}
	a.markRunning(pkg, false)
	return nil
}

// Uninstall removes an app.
func (a *APKs) Uninstall(ctx context.Context, pkg string) error {
	dev, err := a.sandbox.Device()
	if err != nil {
		return err
	}
	if err := dev.Uninstall(ctx, pkg); err != nil {
		return err
	}
	s := a.sandbox
	s.mu.Lock()
	if s.app != nil && s.app.Package == pkg {
		s.app = nil
	}
	s.mu.Unlock()
	s.update(func(*Status) {})
	return nil
}

// Reinstall uninstalls (dropping data) and installs the APK again.
func (a *APKs) Reinstall(ctx context.Context, id string) error {
	e, err := a.Get(id)
	if err != nil {
		return err
	}
	if e.Info != nil && a.sandbox.Running() {
		if dev, err := a.sandbox.Device(); err == nil {
			if ok, _ := dev.IsInstalled(ctx, e.Info.Package); ok {
				if err := dev.Uninstall(ctx, e.Info.Package); err != nil {
					return err
				}
			}
		}
	}
	return a.Install(ctx, id)
}

func (a *APKs) markRunning(pkg string, running bool) {
	s := a.sandbox
	s.mu.Lock()
	if s.app != nil && s.app.Package == pkg {
		s.app.Running = running
	}
	s.mu.Unlock()
	s.update(func(*Status) {})
}

// Cleanup deletes uploaded files.
func (a *APKs) Cleanup() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.entries {
		os.Remove(e.path)
	}
}
