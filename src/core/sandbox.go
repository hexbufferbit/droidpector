package core

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/android/adb"
	"github.com/droidpector/apkinspector/src/android/agent"
	"github.com/droidpector/apkinspector/src/android/device"
	"github.com/droidpector/apkinspector/src/display"
	"github.com/droidpector/apkinspector/src/network"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/vm"
)

// State is the sandbox lifecycle state shown in the UI.
type State string

const (
	StateStopped      State = "stopped"
	StatePreparing    State = "preparing"
	StateStarting     State = "starting"
	StateBooting      State = "booting"
	StateProvisioning State = "provisioning"
	StateReady        State = "ready"
	StateInstalling   State = "installing"
	StateLaunching    State = "launching"
	StateStopping     State = "stopping"
	StateRecovering   State = "recovering"
	StateError        State = "error"
)

// ErrorInfo is a user-facing error with causes and diagnostic details.
type ErrorInfo struct {
	Title   string   `json:"title"`
	Causes  []string `json:"causes,omitempty"`
	Details string   `json:"details,omitempty"`
	Code    string   `json:"code,omitempty"`
}

// Status is the complete sandbox status published to the UI.
type Status struct {
	State         State      `json:"state"`
	Message       string     `json:"message"`
	Runtime       string     `json:"runtime,omitempty"`
	Android       string     `json:"android,omitempty"`
	Accelerator   string     `json:"accelerator,omitempty"`
	Accelerated   bool       `json:"accelerated"`
	Warnings      []string   `json:"warnings,omitempty"`
	Error         *ErrorInfo `json:"error,omitempty"`
	CaptureActive bool       `json:"captureActive"`
	AppOnly       bool       `json:"appOnlyTraffic"` // only the app under test may reach the network
	BlockedFlows  int64      `json:"blockedFlows"`   // packets the sandbox firewall rejected this boot
	Orientation   int        `json:"orientation"`    // display rotation in quarter turns (0 = portrait)
	TouchInput    bool       `json:"touchInput"`     // pointer input is delivered as real touches (in-guest agent)
	HTTPSInspect  bool       `json:"httpsInspection"`
	CAFingerprint string     `json:"caFingerprint,omitempty"`
	SessionID     string     `json:"sessionId,omitempty"`
	App           *AppState  `json:"app,omitempty"`
	DisplayReady  bool       `json:"displayReady"`
	Since         time.Time  `json:"since"`
}

// AppState describes the APK under test.
type AppState struct {
	Package string `json:"package"`
	Label   string `json:"label,omitempty"`
	Version string `json:"version,omitempty"`
	File    string `json:"file,omitempty"`
	Running bool   `json:"running"`
}

// SandboxConfig configures the orchestrator.
type SandboxConfig struct {
	Profiles        map[string]vm.Profile
	SandboxDir      string
	LogDir          string
	MemoryMB        int
	CPUs            int
	Accel           string
	BootTimeout     time.Duration
	AutoRestart     bool
	UseBootSnapshot bool
	InspectHTTPS    bool
	AppOnly         bool // initial state of the app-only firewall
	ADBKey          *rsa.PrivateKey
}

// Sandbox orchestrates VM, Android provisioning, capture and display.
type Sandbox struct {
	cfg      SandboxConfig
	log      *slog.Logger
	vmLog    *slog.Logger
	procs    *platform.ProcessGroup
	gw       *network.Gateway
	sessions *Sessions
	rec      *Recorder
	streamer *display.Streamer
	onStatus func(Status)

	opMu sync.Mutex // serializes lifecycle operations

	mu        sync.Mutex
	status    Status
	machine   *vm.Machine
	adbConn   *adb.Conn
	dev       *device.Device
	vnc       *display.Client
	profile   vm.Profile
	runCancel context.CancelFunc
	crashes   []time.Time
	app       *AppState
	owners    *ownerTable
	ownerPoke chan struct{}

	appOnly bool           // sandbox firewall: only allowed apps may reach the network
	allowed map[string]int // package → uid of the apps under test

	life    context.Context // ends when the application shuts down
	endLife context.CancelFunc
}

// NewSandbox wires the orchestrator.
func NewSandbox(cfg SandboxConfig, log, vmLog *slog.Logger, procs *platform.ProcessGroup, gw *network.Gateway,
	sessions *Sessions, rec *Recorder, streamer *display.Streamer, onStatus func(Status)) *Sandbox {
	if cfg.BootTimeout <= 0 {
		cfg.BootTimeout = 10 * time.Minute
	}
	s := &Sandbox{cfg: cfg, log: log, vmLog: vmLog, procs: procs, gw: gw, sessions: sessions, rec: rec, streamer: streamer,
		onStatus: onStatus, owners: newOwnerTable(), ownerPoke: make(chan struct{}, 1), appOnly: cfg.AppOnly, allowed: map[string]int{}}
	s.status = Status{State: StateStopped, Message: "Sandbox stopped", Since: time.Now(), AppOnly: cfg.AppOnly}
	s.life, s.endLife = context.WithCancel(context.Background())
	rec.SetOwnerLookup(s.owners.lookup)
	return s
}

// Shutdown aborts a start in progress because the application is closing:
// Stop then runs at once instead of waiting for Android to finish booting
// (which can take minutes). The sandbox cannot be started afterwards.
func (s *Sandbox) Shutdown() { s.endLife() }

// withLife returns ctx, additionally cancelled by Shutdown.
func (s *Sandbox) withLife(ctx context.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.life, cancel)
	if s.life.Err() != nil {
		cancel() // already shut down (AfterFunc would cancel asynchronously)
	}
	return c, func() { stop(); cancel() }
}

// Status returns the current status.
func (s *Sandbox) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	if s.app != nil {
		a := *s.app
		st.App = &a
	}
	st.DisplayReady = s.vnc != nil
	return st
}

func (s *Sandbox) update(f func(st *Status)) {
	s.mu.Lock()
	prev := s.status.State
	f(&s.status)
	if s.status.State != prev {
		s.status.Since = time.Now()
	}
	s.mu.Unlock()
	st := s.Status()
	s.log.Info("sandbox status", "state", st.State, "message", st.Message)
	if s.onStatus != nil {
		s.onStatus(st)
	}
}

func (s *Sandbox) setState(state State, msg string) {
	s.update(func(st *Status) {
		st.State, st.Message = state, msg
		if state != StateError {
			st.Error = nil
		}
	})
}

func (s *Sandbox) fail(err error) error {
	info := toErrorInfo(err)
	s.update(func(st *Status) {
		st.State, st.Message, st.Error = StateError, info.Title, info
		st.CaptureActive = false
	})
	return err
}

func toErrorInfo(err error) *ErrorInfo {
	var se *vm.StartError
	if errors.As(err, &se) {
		return &ErrorInfo{Title: se.Title, Causes: se.Causes, Details: se.Details, Code: se.Code}
	}
	var ue *UserError
	if errors.As(err, &ue) {
		return &ErrorInfo{Title: ue.Title, Causes: ue.Causes, Details: ue.Details, Code: ue.Code}
	}
	return &ErrorInfo{Title: "The operation failed: " + err.Error(), Details: err.Error()}
}

// UserError is an actionable error for the UI.
type UserError struct {
	Title   string
	Causes  []string
	Details string
	Code    string
}

func (e *UserError) Error() string {
	if e.Details != "" {
		return e.Title + ": " + e.Details
	}
	return e.Title
}

// Device returns the connected Android device or an error when not ready.
func (s *Sandbox) Device() (*device.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev == nil {
		return nil, &UserError{Title: "The Android sandbox is not running.", Causes: []string{"Start the sandbox first."}, Code: "not_running"}
	}
	return s.dev, nil
}

// Profile returns the running (or last used) runtime profile.
func (s *Sandbox) Profile() vm.Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
}

// Running reports whether a VM is running.
func (s *Sandbox) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.machine != nil
}

// Start boots the sandbox with the given runtime profile ("" = x86_64). It
// returns when Android is ready for APK installation.
func (s *Sandbox) Start(ctx context.Context, profileName string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.start(ctx, profileName, true)
}

func (s *Sandbox) start(ctx context.Context, profileName string, newSession bool) error {
	ctx, endStart := s.withLife(ctx)
	defer endStart()
	if profileName == "" {
		profileName = vm.ProfileX86_64
	}
	if s.Running() {
		if s.Profile().Name == profileName {
			return nil
		}
		s.stop(ctx, false, true)
	}
	s.setState(StatePreparing, "Preparing Sandbox...")
	p, ok := s.cfg.Profiles[profileName]
	if !ok {
		return s.fail(&UserError{Code: "runtime_missing", Title: fmt.Sprintf("The %s Android runtime is not installed.", profileName),
			Causes: []string{"This APK needs a different CPU architecture than the installed runtime provides.", "Reinstall droidpector with the additional runtime component."}})
	}
	disk, err := vm.PrepareDataDisk(s.cfg.SandboxDir, p)
	if err != nil {
		return s.fail(&UserError{Code: "disk", Title: "Android sandbox could not start: the sandbox disk could not be created.", Details: err.Error(),
			Causes: []string{"The disk is full.", "The data folder is not writable."}})
	}
	if newSession || s.sessions.Active() == nil {
		if _, err := s.sessions.Start(ctx, p.Name); err != nil {
			return s.fail(err)
		}
	}
	s.update(func(st *Status) {
		st.Runtime, st.Android, st.SessionID = p.Name, p.Android, s.rec.Session()
		st.Warnings = nil
	})
	s.checkSubnet()

	snapshot := ""
	if s.cfg.UseBootSnapshot && s.hasBootSnapshot(p) {
		snapshot = bootSnapshotTag
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m, err := s.launch(ctx, runCtx, p, disk, snapshot)
	if err != nil && snapshot != "" {
		// A damaged or incompatible quick-start snapshot must never block the user.
		s.log.Warn("quick-start snapshot failed; cold booting", "err", err)
		s.clearBootSnapshot(p)
		snapshot = ""
		m, err = s.launch(ctx, runCtx, p, disk, "")
	}
	if err != nil {
		cancel()
		return s.fail(err)
	}
	if snapshot != "" {
		// The running VM diverges from the saved state immediately; the
		// snapshot is valid again only after the next graceful stop saves it.
		s.clearBootSnapshot(p)
	}
	s.mu.Lock()
	s.machine, s.profile, s.runCancel = m, p, cancel
	s.mu.Unlock()
	s.update(func(st *Status) {
		st.Accelerator, st.Accelerated = m.Accel, m.Accel != "tcg"
		if m.Accel == "tcg" {
			st.Warnings = append(st.Warnings, "Hardware acceleration is unavailable, so Android runs in software emulation and will be slow. Enable \"Windows Hypervisor Platform\" in Windows Features and restart Windows for full speed.")
		}
	})
	go s.keepDisplay(runCtx, m)

	s.setState(StateBooting, "Starting Android...")
	bctx, bcancel := context.WithTimeout(ctx, s.cfg.BootTimeout)
	defer bcancel()
	go s.consoleBootstrap(bctx, m)
	if err := s.connectDevice(bctx, m); err != nil {
		s.stop(context.Background(), false, false)
		return s.fail(&UserError{Code: vm.CodeBootTimeout, Title: "Android did not finish starting.",
			Causes:  []string{"Software emulation is too slow on this computer (enable hardware acceleration).", "The Android runtime files are damaged (reinstall)."},
			Details: err.Error() + "\n" + m.Stderr()})
	}
	s.setState(StateProvisioning, "Preparing Android...")
	if err := s.provision(bctx); err != nil {
		s.log.Warn("provisioning incomplete", "err", err)
	}
	go s.pollOwners(runCtx)
	s.update(func(st *Status) {
		st.State, st.Message, st.CaptureActive = StateReady, "Network Capture Active", true
	})
	return nil
}

func (s *Sandbox) launch(ctx, runCtx context.Context, p vm.Profile, disk, snapshot string) (*vm.Machine, error) {
	s.setState(StateStarting, "Starting Android...")
	return vm.Start(ctx, p, vm.Options{
		MemoryMB: s.cfg.MemoryMB, CPUs: s.cfg.CPUs, Accel: s.cfg.Accel, DataDisk: disk, LoadVM: snapshot,
		SerialLog: filepath.Join(s.cfg.LogDir, "android-console.log"),
	}, vm.Deps{
		Procs: s.procs, Log: s.vmLog,
		AttachNet: func(_ context.Context, c net.Conn) error { return s.gw.Stack().ServeQEMU(runCtx, c) },
		OnEvent:   s.onVMEvent,
	})
}

// keepDisplay (re)connects the display for the lifetime of the machine so
// the user sees Android from the first boot frame.
func (s *Sandbox) keepDisplay(ctx context.Context, m *vm.Machine) {
	addr, pass := m.VNC()
	for ctx.Err() == nil {
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		c, err := display.Dial(dctx, addr, pass, s.streamer.Damage, s.streamer.Resize)
		cancel()
		if err == nil {
			s.mu.Lock()
			s.vnc = c
			s.mu.Unlock()
			s.streamer.Attach(c)
			s.update(func(*Status) {})
			select {
			case <-c.Done():
			case <-ctx.Done():
				c.Close()
			}
			s.streamer.Attach(nil)
			s.mu.Lock()
			s.vnc = nil
			s.mu.Unlock()
			s.update(func(*Status) {})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// consoleBootstrap ensures adbd listens on TCP using the root serial console
// (images differ in their default; the property persists on the data disk).
func (s *Sandbox) consoleBootstrap(ctx context.Context, m *vm.Machine) {
	con := m.Console()
	// The initrd briefly offers its own shell on the same console; wait until
	// Android's shell answers (getprop works) before configuring adbd.
	for {
		if err := con.WaitShell(ctx); err != nil {
			return
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		out, _, err := con.Run(pctx, "getprop ro.build.version.sdk")
		cancel()
		if err == nil && isDigits(strings.TrimSpace(out)) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _, err := con.Run(cctx, "setprop persist.adb.tcp.port 5555; if [ \"$(getprop service.adb.tcp.port)\" != 5555 ]; then setprop service.adb.tcp.port 5555; setprop ctl.restart adbd; fi; getprop init.svc.adbd")
	s.vmLog.Info("console bootstrap", "adbd", strings.TrimSpace(out), "err", err)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// connectDevice connects ADB through the sandbox network, waits for boot and
// makes adbd run as root where the image allows it.
func (s *Sandbox) connectDevice(ctx context.Context, m *vm.Machine) error {
	conn, dev, err := s.connectADB(ctx, m)
	if err != nil {
		return err
	}
	link := adbLink{conn, dev}
	// Inspection needs root (system CA store, clock, firewall). On userdebug
	// images adbd restarts itself as root when asked. It answers, then exits
	// and is restarted by init — so a quick reconnect can still reach the old,
	// non-root adbd, whose connection then dies mid-command. Reconnect until
	// the new adbd reports uid 0.
	if !isRootShell(ctx, dev) {
		link, err = becomeRoot(ctx, link, rootSteps[adbLink]{
			request: func(l adbLink) (refused bool) {
				msg, rerr := l.conn.Root(ctx)
				s.vmLog.Info("adb root requested", "message", msg, "err", rerr)
				return errors.Is(rerr, adb.ErrRootRefused)
			},
			drop: func(l adbLink) { l.conn.Close() },
			connect: func() (adbLink, error) {
				c, d, err := s.connectADB(ctx, m)
				return adbLink{c, d}, err
			},
			isRoot:   func(l adbLink) bool { return isRootShell(ctx, l.dev) },
			interval: 2 * time.Second, rerequest: 20 * time.Second, giveUp: 90 * time.Second,
		})
		if err != nil {
			return err
		}
	}
	conn, dev = link.conn, link.dev
	if !isRootShell(ctx, dev) {
		s.log.Warn("adbd is not running as root; HTTPS payload inspection, clock sync and the app-only firewall are unavailable")
	}
	s.mu.Lock()
	s.adbConn, s.dev = conn, dev
	s.mu.Unlock()
	s.log.Info("Android is available", "device", conn.Info().State)
	return nil
}

type adbLink struct {
	conn *adb.Conn
	dev  *device.Device
}

// rootSteps are the operations becomeRoot drives (injected for tests).
type rootSteps[L any] struct {
	request   func(L) bool      // ask adbd (through L) to restart as root; true = refused for good
	drop      func(L)           // close a connection
	connect   func() (L, error) // open a new connection (waits for boot)
	isRoot    func(L) bool
	interval  time.Duration // between reconnects
	rerequest time.Duration // ask again when adbd did not restart within this
	giveUp    time.Duration // then continue without root
}

// becomeRoot asks adbd to restart as root and reconnects until the
// connection is served by a root adbd. The request is repeated if adbd has
// not restarted in time (it may have been lost); after giveUp, or when adbd
// refuses outright, the connection is returned as is.
func becomeRoot[L any](ctx context.Context, cur L, st rootSteps[L]) (L, error) {
	start := time.Now()
	if st.request(cur) {
		return cur, nil // production build: adbd stays as it is, and so does the connection
	}
	requested := time.Now()
	st.drop(cur)
	for {
		select {
		case <-ctx.Done():
			var zero L
			return zero, ctx.Err()
		case <-time.After(st.interval):
		}
		next, err := st.connect()
		if err != nil {
			return next, err
		}
		if st.isRoot(next) || time.Since(start) >= st.giveUp {
			return next, nil
		}
		if time.Since(requested) >= st.rerequest {
			st.request(next)
			requested = time.Now()
		}
		st.drop(next) // the previous adbd, still shutting down (or the request was lost)
	}
}

func isRootShell(ctx context.Context, dev *device.Device) bool {
	out, _, err := dev.Run(ctx, "id -u")
	return err == nil && strings.TrimSpace(out) == "0"
}

// connectADB connects to adbd over the sandbox network and waits until
// Android has booted.
func (s *Sandbox) connectADB(ctx context.Context, m *vm.Machine) (*adb.Conn, *device.Device, error) {
	var lastErr error
	for {
		select {
		case <-m.Exited():
			return nil, nil, fmt.Errorf("the virtual machine stopped during boot: %s", m.Stderr())
		default:
		}
		actx, cancel := context.WithTimeout(ctx, 15*time.Second)
		conn, err := adb.Connect(actx, adb.Config{
			Key:  s.cfg.ADBKey,
			Dial: func(ctx context.Context) (net.Conn, error) { return s.gw.Stack().DialGuest(ctx, 5555) },
		})
		cancel()
		if err == nil {
			dev := device.New(conn)
			if err := dev.WaitBootCompleted(ctx, 2*time.Second); err != nil {
				conn.Close()
				return nil, nil, err
			}
			return conn, dev, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("could not connect to Android (ADB): %v", lastErr)
		case <-time.After(2 * time.Second):
		}
	}
}

// provisionBase applies persistent settings suited to an inspection sandbox.
func (s *Sandbox) provisionBase(ctx context.Context) {
	dev, err := s.Device()
	if err != nil {
		return
	}
	cmds := []string{
		"settings put global private_dns_mode off",      // DNS stays observable
		"settings put global package_verifier_enable 0", // no Play Protect prompts on install
		"settings put global verifier_verify_adb_installs 0",
		"settings put system screen_off_timeout 2147483647",
		"svc power stayon true",
		// No captive-portal probes: they are system traffic (blocked by the
		// app-only firewall) and would otherwise mark the network as limited.
		"settings put global captive_portal_mode 0",
		"settings put global captive_portal_detection_enabled 0",
		fmt.Sprintf("wm density %d", s.Profile().EffectiveDisplay().Density), // phone-like UI scale
	}
	for _, c := range cmds {
		if _, _, err := dev.Run(ctx, c); err != nil {
			s.log.Warn("provisioning command failed", "cmd", c, "err", err)
		}
	}
	// Images with several launchers ask "Select a Home app" at every boot;
	// choose one so the user lands on a usable home screen.
	if home, err := dev.SetDefaultHome(ctx); err != nil {
		s.log.Warn("could not choose a default launcher", "err", err)
	} else if home != "" {
		s.log.Info("default launcher", "component", home)
	}
}

// provision runs after every boot or restore: clock, unlock, HTTPS CA.
func (s *Sandbox) provision(ctx context.Context) error {
	dev, err := s.Device()
	if err != nil {
		return err
	}
	if err := dev.SetTime(ctx, time.Now()); err != nil {
		s.log.Warn("could not synchronize the Android clock", "err", err)
		s.warn("The Android clock could not be synchronized; HTTPS connections may fail certificate validation.")
	}
	s.provisionBase(ctx)
	dev.Run(ctx, "wm dismiss-keyguard; input keyevent 82")
	if !s.cfg.InspectHTTPS {
		s.gw.SetCA(nil)
		s.update(func(st *Status) { st.HTTPSInspect, st.CAFingerprint = false, "" })
		return nil
	}
	ca, err := network.NewCA(time.Now())
	if err != nil {
		return err
	}
	if err := dev.InstallCA(ctx, ca.CertPEM()); err != nil {
		s.gw.SetCA(nil)
		s.warn("HTTPS payload inspection is unavailable: the sandbox certificate could not be installed (" + err.Error() + "). HTTPS connections are still listed with their metadata.")
		s.update(func(st *Status) { st.HTTPSInspect = false })
		return err
	}
	s.gw.SetCA(ca)
	s.update(func(st *Status) { st.HTTPSInspect, st.CAFingerprint = true, ca.Fingerprint() })
	s.applyFirewall(ctx)
	if n, err := dev.Rotation(ctx); err == nil {
		s.update(func(st *Status) { st.Orientation = n })
	}
	s.provisionTouch(ctx, dev)
	return nil
}

// provisionTouch installs and starts the in-guest touch agent and routes
// pointer input through it. Without it (agent not built in, /dev/uinput
// missing) the emulated mouse is used, which only works in portrait.
func (s *Sandbox) provisionTouch(ctx context.Context, dev *device.Device) {
	bin := agent.Binary("amd64")
	if bin == nil {
		s.log.Warn("touch agent not built into this executable; using the emulated mouse (portrait only)")
		return
	}
	disp := s.Profile().EffectiveDisplay()
	// Reuse a running agent (snapshot restore) or (re)install and start it.
	if _, err := s.dialTouch(ctx); err != nil {
		if err := dev.Push(ctx, bytes.NewReader(bin), int64(len(bin)), agent.RemotePath, 0o755, time.Now()); err != nil {
			s.log.Warn("could not install the touch agent", "err", err)
			return
		}
		cmd := fmt.Sprintf("chmod 755 %s; (nohup %s -listen :%d -width %d -height %d >/data/local/tmp/droidpector-agent.log 2>&1 &); sleep 1; cat /data/local/tmp/droidpector-agent.log",
			agent.RemotePath, agent.RemotePath, agent.Port, disp.Width, disp.Height)
		out, _, err := dev.Run(ctx, cmd)
		s.log.Info("touch agent started", "output", strings.TrimSpace(out), "err", err)
	}
	var client *agent.Client
	var err error
	for i := 0; i < 10 && client == nil; i++ {
		if client, err = s.dialTouch(ctx); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	if client == nil {
		s.log.Warn("touch agent unreachable; using the emulated mouse (portrait only)", "err", err)
		s.warn("Touch input is unavailable in this sandbox; clicks work in portrait only.")
		return
	}
	s.streamer.SetTouch(client)
	s.update(func(st *Status) { st.TouchInput = true })
	s.log.Info("touch input active", "panel", fmt.Sprintf("%dx%d", client.W, client.H))
}

func (s *Sandbox) dialTouch(ctx context.Context) (*agent.Client, error) {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return agent.Dial(dctx, func(ctx context.Context) (net.Conn, error) { return s.gw.Stack().DialGuest(ctx, agent.Port) })
}

// Rotate sets the Android display orientation (quarter turns, 0 = portrait).
// Android renders the rotated UI into the same framebuffer; the display
// stream reports the orientation so the UI can show it upright.
func (s *Sandbox) Rotate(ctx context.Context, orientation int) error {
	dev, err := s.Device()
	if err != nil {
		return err
	}
	if err := dev.SetRotation(ctx, orientation); err != nil {
		return &UserError{Code: "rotate", Title: "The display could not be rotated.", Details: err.Error()}
	}
	s.update(func(st *Status) { st.Orientation = orientation })
	return nil
}

// applyFirewall programs the in-guest firewall from the current app-only
// setting and the allowed packages. Failures are reported as warnings; the
// sandbox keeps working without the restriction.
func (s *Sandbox) applyFirewall(ctx context.Context) {
	dev, err := s.Device()
	if err != nil {
		return
	}
	s.mu.Lock()
	enabled := s.appOnly
	uids := make([]int, 0, len(s.allowed))
	for _, uid := range s.allowed {
		uids = append(uids, uid)
	}
	s.mu.Unlock()
	if !enabled {
		if err := dev.ClearAppFirewall(ctx); err != nil {
			s.log.Warn("could not remove the sandbox firewall", "err", err)
		}
		return
	}
	if err := dev.SetAppFirewall(ctx, device.FirewallRules{AllowUIDs: uids, AllowDNS: true}); err != nil {
		s.log.Warn("could not apply the sandbox firewall", "err", err)
		s.warn("Android system traffic could not be blocked (" + err.Error() + "); all sandbox traffic is captured.")
		return
	}
	s.log.Info("sandbox firewall applied", "allowedUids", uids)
}

// SetAppOnly turns the app-only firewall on or off (persisting for this run).
func (s *Sandbox) SetAppOnly(ctx context.Context, enabled bool) {
	s.mu.Lock()
	s.appOnly = enabled
	s.mu.Unlock()
	s.update(func(st *Status) { st.AppOnly = enabled })
	if s.Running() {
		s.applyFirewall(ctx)
	}
}

// AllowPackage lets an installed package (the app under test) through the
// firewall; the rules are refreshed immediately.
func (s *Sandbox) AllowPackage(ctx context.Context, pkg string) error {
	dev, err := s.Device()
	if err != nil {
		return err
	}
	uid, err := dev.PackageUID(ctx, pkg)
	if err != nil {
		return fmt.Errorf("looking up the app's user id: %w", err)
	}
	s.mu.Lock()
	s.allowed[pkg] = uid
	s.mu.Unlock()
	s.applyFirewall(ctx)
	return nil
}

// DisallowPackage removes a package from the firewall allow list.
func (s *Sandbox) DisallowPackage(ctx context.Context, pkg string) {
	s.mu.Lock()
	_, had := s.allowed[pkg]
	delete(s.allowed, pkg)
	s.mu.Unlock()
	if had && s.Running() {
		s.applyFirewall(ctx)
	}
}

func (s *Sandbox) warn(msg string) {
	s.update(func(st *Status) {
		for _, w := range st.Warnings {
			if w == msg {
				return
			}
		}
		st.Warnings = append(st.Warnings, msg)
	})
}

// Stop shuts the sandbox down and ends the session.
func (s *Sandbox) Stop(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.stop(ctx, true, true)
	return nil
}

// stop shuts the VM down. With saveState (graceful stops) the complete VM
// state is saved as the quick-start snapshot first, like the Android
// emulator's quick boot: the next start resumes in seconds with every app
// and file intact. Crashed or failing VMs are never saved: they cold boot
// from their (current) disk next time.
func (s *Sandbox) stop(ctx context.Context, endSession, saveState bool) {
	s.mu.Lock()
	m, conn, cancel := s.machine, s.adbConn, s.runCancel
	s.machine, s.adbConn, s.dev, s.runCancel = nil, nil, nil, nil
	if s.app != nil {
		s.app.Running = false
	}
	s.mu.Unlock()
	if m == nil {
		if endSession {
			s.sessions.End(ctx)
		}
		s.setState(StateStopped, "Sandbox stopped")
		return
	}
	s.setState(StateStopping, "Stopping sandbox...")
	if conn != nil {
		sctx, c := context.WithTimeout(ctx, 5*time.Second)
		conn.Shell(sctx, "sync")
		c()
		conn.Close()
	}
	if saveState && s.cfg.UseBootSnapshot {
		s.saveQuickStart(ctx, m)
	}
	if cancel != nil {
		cancel()
	}
	s.streamer.SetTouch(nil)
	s.update(func(st *Status) { st.TouchInput = false })
	m.Stop(10 * time.Second)
	s.gw.SetCA(nil)
	s.owners.reset()
	if endSession {
		s.sessions.End(ctx)
	}
	s.update(func(st *Status) {
		st.State, st.Message, st.CaptureActive, st.HTTPSInspect, st.CAFingerprint = StateStopped, "Sandbox stopped", false, false, ""
		st.BlockedFlows = 0
		if endSession {
			st.SessionID = ""
		}
	})
}

// Restart stops and starts the sandbox with the same runtime, keeping the session.
func (s *Sandbox) Restart(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	name := s.Profile().Name
	s.stop(ctx, false, true)
	return s.start(ctx, name, false)
}

// Reset deletes all apps, data and snapshots of the sandbox and restarts it.
func (s *Sandbox) Reset(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	p := s.Profile()
	if p.Name == "" {
		p = s.cfg.Profiles[vm.ProfileX86_64]
	}
	s.stop(ctx, false, false)
	s.clearBootSnapshot(p)
	if _, err := vm.ResetDataDisk(s.cfg.SandboxDir, p); err != nil {
		return s.fail(&UserError{Code: "reset", Title: "The sandbox could not be reset.", Details: err.Error()})
	}
	s.mu.Lock()
	s.app = nil
	s.mu.Unlock()
	return s.start(ctx, p.Name, false)
}

// ---- crash detection and recovery ----------------------------------------------

const (
	maxCrashes  = 3
	crashWindow = 10 * time.Minute
)

// shouldRecover applies the restart budget: at most maxCrashes within crashWindow.
func shouldRecover(history []time.Time, now time.Time) ([]time.Time, bool) {
	var recent []time.Time
	for _, t := range history {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	return recent, len(recent) <= maxCrashes
}

func (s *Sandbox) onVMEvent(e vm.Event) {
	s.vmLog.Info("vm event", "type", e.Type, "detail", e.Detail, "unexpected", e.Unexpected)
	if !e.Unexpected {
		return
	}
	go s.recover(e)
}

func (s *Sandbox) recover(e vm.Event) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	m := s.machine
	p := s.profile
	var ok bool
	s.crashes, ok = shouldRecover(s.crashes, time.Now())
	app := s.app
	s.mu.Unlock()
	if m == nil {
		return // already stopped deliberately
	}
	reason := "Android sandbox stopped unexpectedly"
	if e.Type == vm.EventPanic {
		reason = "Android crashed (kernel panic)"
	}
	s.log.Error("sandbox crash detected", "reason", reason, "detail", e.Detail)
	s.setState(StateRecovering, reason+". Restarting...")
	s.stop(context.Background(), false, false) // cleanup: kill QEMU, drop ADB/display, keep the session
	if !s.cfg.AutoRestart || !ok {
		s.fail(&UserError{Code: "crashed", Title: reason + ".",
			Causes:  []string{"The sandbox crashed repeatedly; automatic restart was stopped.", "Use Start to try again, or Save diagnostic bundle and report the problem."},
			Details: e.Detail})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.BootTimeout+time.Minute)
	defer cancel()
	if err := s.start(ctx, p.Name, false); err != nil {
		return
	}
	if app != nil && app.Running && app.Package != "" {
		if dev, err := s.Device(); err == nil {
			if _, err := dev.Launch(ctx, app.Package); err == nil {
				s.mu.Lock()
				s.app.Running = true
				s.mu.Unlock()
				s.update(func(*Status) {})
			}
		}
	}
	s.warn("The sandbox recovered from a crash at " + time.Now().Format("15:04:05") + ".")
}

// ---- snapshots ---------------------------------------------------------------------

const bootSnapshotTag = "quickstart"

var tagRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

func (s *Sandbox) bootMarker(p vm.Profile) string {
	return filepath.Join(s.cfg.SandboxDir, p.Name, "quickstart.json")
}

func (s *Sandbox) hasBootSnapshot(p vm.Profile) bool {
	b, err := os.ReadFile(s.bootMarker(p))
	if err != nil {
		return false
	}
	var m struct {
		MemoryMB int    `json:"memoryMB"`
		ISO      string `json:"iso"`
		Subnet   string `json:"subnet"`
	}
	// A snapshot is only valid for the same memory size, runtime image and
	// network plan (the restored guest keeps its IP configuration). Markers
	// from before the subnet was recorded were taken on the default plan.
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	if m.Subnet == "" {
		m.Subnet = network.DefaultAddressing().Subnet.String()
	}
	return m.MemoryMB == s.cfg.MemoryMB && m.ISO == p.ISO && m.Subnet == s.subnet().String()
}

func (s *Sandbox) subnet() netip.Prefix { return s.gw.Stack().Addressing().Subnet }

// checkSubnet warns when a network the host reaches (typically a VPN that
// connected after droidpector started) overlaps the sandbox subnet.
func (s *Sandbox) checkSubnet() {
	routes, err := network.HostRoutes()
	if err != nil {
		return
	}
	ch, err := network.ChooseAddressing(s.subnet().String(), routes)
	if err == nil && ch.Warning() != "" {
		s.log.Warn("sandbox subnet conflict", "subnet", s.subnet(), "routes", fmt.Sprint(ch.Conflicts))
		s.warn(ch.Warning())
	}
}

func (s *Sandbox) markBootSnapshot(p vm.Profile) {
	b, _ := json.Marshal(map[string]any{"memoryMB": s.cfg.MemoryMB, "iso": p.ISO, "subnet": s.subnet().String(), "created": time.Now()})
	os.WriteFile(s.bootMarker(p), b, 0o600)
}

func (s *Sandbox) clearBootSnapshot(p vm.Profile) { os.Remove(s.bootMarker(p)) }

func (s *Sandbox) saveQuickStart(ctx context.Context, m *vm.Machine) {
	select {
	case <-m.Exited():
		return
	default:
	}
	s.setState(StateStopping, "Saving the sandbox for a fast next start...")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	m.DeleteSnapshot(sctx, bootSnapshotTag) // replace the previous state, if any
	if err := m.SaveSnapshot(sctx, bootSnapshotTag); err != nil {
		s.log.Warn("could not save the quick-start state; the next start will cold boot", "err", err)
		return
	}
	s.markBootSnapshot(m.Profile)
}

func (s *Sandbox) runningMachine() (*vm.Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.machine == nil {
		return nil, &UserError{Title: "The Android sandbox is not running.", Code: "not_running"}
	}
	return s.machine, nil
}

func checkTag(tag string) error {
	if !tagRe.MatchString(tag) || tag == bootSnapshotTag {
		return &UserError{Code: "bad_tag", Title: "Invalid snapshot name.", Causes: []string{"Use 1-40 letters, digits, '-' or '_' (\"" + bootSnapshotTag + "\" is reserved)."}}
	}
	return nil
}

// SaveSnapshot saves the running sandbox's complete state.
func (s *Sandbox) SaveSnapshot(ctx context.Context, tag string) error {
	if err := checkTag(tag); err != nil {
		return err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	m, err := s.runningMachine()
	if err != nil {
		return err
	}
	return m.SaveSnapshot(ctx, tag)
}

// RestoreSnapshot restores a snapshot and re-provisions the restored system
// (clock, ADB connection, fresh HTTPS CA).
func (s *Sandbox) RestoreSnapshot(ctx context.Context, tag string) error {
	if err := checkTag(tag); err != nil {
		return err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	ctx, endRestore := s.withLife(ctx)
	defer endRestore()
	m, err := s.runningMachine()
	if err != nil {
		return err
	}
	s.setState(StateProvisioning, "Restoring snapshot...")
	s.mu.Lock()
	if s.adbConn != nil {
		s.adbConn.Close()
	}
	s.adbConn, s.dev = nil, nil
	s.mu.Unlock()
	if err := m.LoadSnapshot(ctx, tag); err != nil {
		return s.fail(&UserError{Code: "snapshot", Title: "The snapshot could not be restored.", Details: err.Error()})
	}
	bctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.connectDevice(bctx, m); err != nil {
		return s.fail(&UserError{Code: "snapshot", Title: "Android did not respond after restoring the snapshot.", Details: err.Error()})
	}
	s.provision(bctx)
	s.update(func(st *Status) { st.State, st.Message, st.CaptureActive = StateReady, "Network Capture Active", true })
	return nil
}

// DeleteSnapshot removes a snapshot.
func (s *Sandbox) DeleteSnapshot(ctx context.Context, tag string) error {
	if err := checkTag(tag); err != nil {
		return err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	m, err := s.runningMachine()
	if err != nil {
		return err
	}
	return m.DeleteSnapshot(ctx, tag)
}

// Snapshots lists user snapshots.
func (s *Sandbox) Snapshots(ctx context.Context) ([]vm.Snapshot, error) {
	m, err := s.runningMachine()
	if err != nil {
		return nil, err
	}
	all, err := m.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	out := []vm.Snapshot{}
	for _, sn := range all {
		if sn.Tag != bootSnapshotTag {
			out = append(out, sn)
		}
	}
	return out, nil
}

// ---- app attribution ----------------------------------------------------------------

// ownerTable maps guest source ports to package names.
type ownerTable struct {
	mu    sync.RWMutex
	ports map[int]string
}

func newOwnerTable() *ownerTable { return &ownerTable{ports: map[int]string{}} }

func (o *ownerTable) lookup(port int) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.ports[port]
}

func (o *ownerTable) set(ports map[int]string) {
	o.mu.Lock()
	o.ports = ports
	o.mu.Unlock()
}

// merge adds fresh owners while keeping recently seen ports, so a socket
// that closed between polls can still be attributed.
func (o *ownerTable) merge(ports map[int]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.ports) > 8192 {
		o.ports = map[int]string{}
	}
	for p, pkg := range ports {
		o.ports[p] = pkg
	}
}

func (o *ownerTable) reset() { o.set(map[int]string{}) }

// NewFlow requests an immediate attribution refresh (gateway OnNewFlow hook).
func (s *Sandbox) NewFlow(netip.AddrPort) {
	select {
	case s.ownerPoke <- struct{}{}:
	default:
	}
}

// pollOwners refreshes socket ownership so events can be attributed to apps:
// every second, and immediately (debounced) when the guest opens a connection.
func (s *Sandbox) pollOwners(ctx context.Context) {
	var uidPkg map[int]string
	var pkgsAt, blockedAt time.Time
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.ownerPoke:
			time.Sleep(30 * time.Millisecond) // let the connection finish its handshake
		}
		dev, err := s.Device()
		if err != nil {
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if uidPkg == nil || time.Since(pkgsAt) > 30*time.Second {
			if pk, err := dev.ListPackagesWithUID(qctx); err == nil {
				uidPkg = map[int]string{}
				for p, u := range pk {
					if prev, ok := uidPkg[u]; !ok || p < prev {
						uidPkg[u] = p // shared UIDs: pick a stable name
					}
				}
				pkgsAt = time.Now()
			}
		}
		owners, err := dev.ConnectionOwners(qctx)
		cancel()
		if err != nil || uidPkg == nil {
			continue
		}
		ports := make(map[int]string, len(owners))
		for port, uid := range owners {
			if p, ok := uidPkg[uid]; ok {
				ports[port] = p
			} else if uid < 10000 {
				ports[port] = "android (" + device.AppUID(uid) + ")"
			}
		}
		s.owners.merge(ports)
		s.rec.Reattribute()
		if s.status.AppOnly && time.Since(blockedAt) > 5*time.Second {
			bctx, bcancel := context.WithTimeout(ctx, 5*time.Second)
			if n, err := dev.FirewallBlockedCount(bctx); err == nil {
				blockedAt = time.Now()
				s.mu.Lock()
				changed := n != s.status.BlockedFlows
				s.mu.Unlock()
				if changed {
					s.update(func(st *Status) { st.BlockedFlows = n })
				}
			}
			bcancel()
		}
	}
}

// setApp records the APK under test and publishes the status.
func (s *Sandbox) setApp(a *AppState) {
	s.mu.Lock()
	s.app = a
	s.mu.Unlock()
	s.update(func(*Status) {})
}
