package core

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/android/adb"
	"github.com/droidpector/apkinspector/src/display"
	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/network"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/query"
	"github.com/droidpector/apkinspector/src/storage"
	"github.com/droidpector/apkinspector/src/vm"
)

// App is the fully wired application core (composition root result).
type App struct {
	Version  string
	Paths    platform.Paths
	Config   platform.Config
	Logs     *platform.Loggers
	Store    *storage.Store
	Writer   *storage.Writer
	Query    *query.Service
	Recorder *Recorder
	Sessions *Sessions
	Gateway  *network.Gateway
	Streamer *display.Streamer
	Sandbox  *Sandbox
	APKs     *APKs
	Hub      *Hub
	Profiles map[string]vm.Profile

	procs  *platform.ProcessGroup
	cancel context.CancelFunc
	once   sync.Once
}

// NewApp builds every service from paths and configuration.
func NewApp(version string, paths platform.Paths, cfg platform.Config, logs *platform.Loggers) (*App, error) {
	if err := paths.Ensure(); err != nil {
		return nil, err
	}
	a := &App{Version: version, Paths: paths, Config: cfg, Logs: logs, Hub: NewHub()}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	ok := false
	defer func() {
		if !ok {
			a.Close()
		}
	}()

	var err error
	if a.Store, err = storage.Open(paths.SessionDir); err != nil {
		return nil, fmt.Errorf("opening the capture database: %w", err)
	}
	if err := a.Store.Integrity(ctx); err != nil {
		logs.App.Error("database integrity check failed", "err", err)
	}
	a.Writer = storage.NewWriter(a.Store, storage.WriterOptions{QueueSize: cfg.WriteQueue}, logs.App)
	a.Query = query.NewService(a.Store, 3)
	a.Recorder = NewRecorder(a.Writer, a.Query, a.Hub.EventsChanged)
	a.Sessions = NewSessions(a.Store, a.Writer, a.Query, a.Recorder,
		storage.RetentionPolicy{MaxSessions: cfg.KeepSessions, MaxAge: time.Duration(cfg.KeepDays) * 24 * time.Hour}, version)

	netOpts := network.Options{
		Policy:       network.Policy{BlockLoopback: true, BlockLinkLocal: true, BlockPrivate: cfg.BlockPrivate, BlockQUIC: cfg.BlockQUIC, FilterAAAA: true},
		MaxBodyBytes: int64(cfg.MaxBodyMB) << 20,
	}
	for _, m := range cfg.HostMappings {
		host, target, found := strings.Cut(m, "=")
		if !found {
			return nil, fmt.Errorf("host mapping %q must look like host=ip:port", m)
		}
		netOpts.Mappings = append(netOpts.Mappings, network.HostMapping{Host: host, Target: target})
	}
	if cfg.ExtraRootsPEM != "" {
		roots, err := loadPEMCerts(cfg.ExtraRootsPEM)
		if err != nil {
			return nil, err
		}
		netOpts.ExtraRoots = roots
	}
	var sandboxRef *Sandbox
	netOpts.OnNewFlow = func(c netip.AddrPort) {
		if sb := sandboxRef; sb != nil {
			sb.NewFlow(c)
		}
	}
	if a.Gateway, err = network.New(netOpts, a.Recorder, a.Store.Blobs(), logs.Network); err != nil {
		return nil, fmt.Errorf("starting the sandbox network: %w", err)
	}
	a.Streamer = display.NewStreamer(85, 30)
	go a.Streamer.Run(ctx)

	if a.procs, err = platform.NewProcessGroup(); err != nil {
		return nil, err
	}
	a.Profiles, err = vm.LoadProfiles(filepath.Join(paths.RuntimeDir, "android"), qemuDir(paths.RuntimeDir))
	if err != nil {
		logs.App.Error("Android runtime not found", "err", err)
		a.Profiles = map[string]vm.Profile{}
	}
	key, err := loadOrCreateADBKey(paths.KeysDir)
	if err != nil {
		return nil, err
	}
	a.Sandbox = NewSandbox(SandboxConfig{
		Profiles: a.Profiles, SandboxDir: paths.SandboxDir, LogDir: paths.LogDir,
		MemoryMB: cfg.MemoryMB, CPUs: cfg.CPUs, Accel: cfg.Accelerator, BootTimeout: time.Duration(cfg.BootTimeoutSec) * time.Second,
		AutoRestart: cfg.AutoRestart, UseBootSnapshot: cfg.UseBootSnapshot, InspectHTTPS: cfg.InspectHTTPS, AppOnly: cfg.AppOnly, ADBKey: key,
	}, logs.App, logs.VM, a.procs, a.Gateway, a.Sessions, a.Recorder, a.Streamer, a.Hub.StatusChanged)
	sandboxRef = a.Sandbox
	a.APKs = NewAPKs(filepath.Join(paths.TempDir, "apks"), a.Sandbox, a.Sessions)
	go a.Hub.Run(ctx, a.statsFor)
	ok = true
	return a, nil
}

// qemuDir returns the bundled QEMU directory, or "" to use PATH (development).
func qemuDir(runtimeDir string) string {
	d := filepath.Join(runtimeDir, "qemu")
	if _, err := os.Stat(d); err == nil {
		return d
	}
	return ""
}

func (a *App) statsFor(sessionID string) (query.Stats, error) {
	return a.Query.Stats(context.Background(), sessionID)
}

// Close stops the sandbox and flushes storage.
func (a *App) Close() {
	a.once.Do(func() {
		if a.Sandbox != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			a.Sandbox.Stop(ctx)
			cancel()
		}
		if a.APKs != nil {
			a.APKs.Cleanup()
		}
		if a.cancel != nil {
			a.cancel()
		}
		if a.Gateway != nil {
			a.Gateway.Close()
		}
		if a.Writer != nil {
			a.Writer.Close()
		}
		if a.Store != nil {
			a.Store.Close()
		}
		if a.procs != nil {
			a.procs.Close()
		}
	})
}

// Replay re-issues a captured request as a new event.
func (a *App) Replay(ctx context.Context, eventID string) (*model.Event, error) {
	e, err := a.Event(ctx, eventID)
	if err != nil {
		return nil, err
	}
	var body []byte
	if e.RequestBody != nil {
		if body, err = a.Store.ReadBody(e.RequestBody); err != nil {
			return nil, &UserError{Code: "body_missing", Title: "The request body is no longer stored, so the request cannot be replayed.", Details: err.Error()}
		}
	}
	if a.Recorder.Session() == "" {
		return nil, &UserError{Code: "no_session", Title: "Start a capture session to replay requests (the replay is recorded as a new request)."}
	}
	return a.Gateway.Replay(ctx, network.ReplayRequest{Original: e, Body: body})
}

// ErrNotFound re-exports storage.ErrNotFound for the API layer.
var ErrNotFound = storage.ErrNotFound

func loadPEMCerts(path string) ([]*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading extra root certificates: %w", err)
	}
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no certificates found in %s", path)
	}
	return out, nil
}

// loadOrCreateADBKey keeps the host's ADB key protected at rest (DPAPI on Windows).
func loadOrCreateADBKey(dir string) (*rsa.PrivateKey, error) {
	path := filepath.Join(dir, "adbkey.bin")
	if b, err := os.ReadFile(path); err == nil {
		if plain, err := platform.UnprotectSecret(b); err == nil {
			if k, err := adb.DecodePrivateKeyPEM(plain); err == nil {
				return k, nil
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	k, err := adb.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	pemKey, err := adb.EncodePrivateKeyPEM(k)
	if err != nil {
		return nil, err
	}
	enc, err := platform.ProtectSecret(pemKey)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// Event loads a full event. The list is served live from the query index
// while the writer commits asynchronously, so pending writes are flushed
// first: the detail view is never older than the row the user clicked.
func (a *App) Event(ctx context.Context, id string) (*model.Event, error) {
	fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	a.Writer.Flush(fctx)
	cancel()
	return a.Store.Event(ctx, id)
}
