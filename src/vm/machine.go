package vm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/platform"
)

// Deps are the collaborators a Machine needs.
type Deps struct {
	Procs *platform.ProcessGroup
	Log   *slog.Logger
	// AttachNet serves the VM's only NIC (the gateway's ServeQEMU). It blocks
	// until the link ends.
	AttachNet func(ctx context.Context, conn net.Conn) error
	// OnEvent receives lifecycle notifications (may be nil).
	OnEvent func(Event)
}

// Options configure one VM run.
type Options struct {
	MemoryMB  int
	CPUs      int
	Accel     string // "auto" or a specific accelerator
	DataDisk  string
	SerialLog string
	LoadVM    string
	// StartTimeout bounds how long QEMU may take to connect its channels.
	StartTimeout time.Duration
}

// EventType enumerates machine events.
type EventType string

const (
	EventPanic    EventType = "guest_panicked"
	EventReset    EventType = "reset"
	EventShutdown EventType = "shutdown"
	EventExited   EventType = "exited"
)

// Event is a lifecycle notification.
type Event struct {
	Type       EventType `json:"type"`
	Detail     string    `json:"detail,omitempty"`
	Unexpected bool      `json:"unexpected"` // true for crashes
}

// Machine is one running QEMU instance.
type Machine struct {
	Profile Profile
	Accel   string

	deps    Deps
	cmd     *exec.Cmd
	qmp     *QMP
	console *Console
	vncAddr string
	vncPass string

	stderr   *ringBuffer
	exited   chan struct{}
	exitErr  error
	stopping sync.Once
	stopReq  chan struct{}
	cancel   context.CancelFunc
}

// candidateAccels returns accelerators to try in order (tcg last).
func candidateAccels(goos, hostArch string, p Profile, configured string, kvmUsable bool) []string {
	if configured != "" && configured != "auto" {
		if configured == "tcg" {
			return []string{"tcg"}
		}
		return []string{configured, "tcg"}
	}
	guestArch := "amd64"
	if p.Name == ProfileARM64 {
		guestArch = "arm64"
	}
	var out []string
	switch {
	case goos == "windows" && guestArch == "amd64" && hostArch == "amd64":
		out = append(out, "whpx")
	case goos == "linux" && guestArch == hostArch && kvmUsable:
		out = append(out, "kvm")
	case goos == "darwin" && guestArch == hostArch:
		out = append(out, "hvf")
	}
	return append(out, "tcg")
}

func kvmUsable() bool {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Start launches QEMU, trying hardware accelerators first and falling back
// to TCG when acceleration is unavailable.
func Start(ctx context.Context, p Profile, opts Options, deps Deps) (*Machine, error) {
	if err := p.Validate(); err != nil {
		return nil, &StartError{Code: CodeMissingRuntime, Title: "Android sandbox could not start: required runtime files are missing.",
			Causes: []string{"The installation is incomplete or files were removed by antivirus software. Reinstall droidpector."}, Details: err.Error()}
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 60 * time.Second
	}
	accels := candidateAccels(runtime.GOOS, runtime.GOARCH, p, opts.Accel, runtime.GOOS == "linux" && kvmUsable())
	var lastErr error
	for i, accel := range accels {
		m, err := launch(ctx, p, accel, opts, deps)
		if err == nil {
			if i > 0 {
				deps.Log.Warn("hardware acceleration unavailable; running with software emulation (slow)", "tried", accels[:i], "cause", lastErr)
			}
			return m, nil
		}
		lastErr = err
		var se *StartError
		if !errors.As(err, &se) || se.Code != CodeAccelUnavailable || i == len(accels)-1 {
			return nil, err
		}
		deps.Log.Warn("accelerator failed, trying next", "accel", accel, "err", se.Details)
	}
	return nil, lastErr
}

func listenLocal() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func randomPassword() string {
	b := make([]byte, 6)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b) // 8 chars: the VNC maximum
}

func launch(ctx context.Context, p Profile, accel string, opts Options, deps Deps) (*Machine, error) {
	var ls []net.Listener
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()
	mk := func() (net.Listener, error) {
		l, err := listenLocal()
		if err == nil {
			ls = append(ls, l)
		}
		return l, err
	}
	qmpL, err := mk()
	if err != nil {
		return nil, err
	}
	serL, err := mk()
	if err != nil {
		return nil, err
	}
	netL, err := mk()
	if err != nil {
		return nil, err
	}
	vncPort := 5910 // lower bound; QEMU chooses the first free display (see BuildArgs)
	args, err := BuildArgs(LaunchSpec{
		Profile: p, Accel: accel, MemoryMB: opts.MemoryMB, CPUs: opts.CPUs, DataDisk: opts.DataDisk,
		QMPAddr: qmpL.Addr().String(), SerialAddr: serL.Addr().String(), NetAddr: netL.Addr().String(),
		VNCPort: vncPort, SerialLog: opts.SerialLog, LoadVM: opts.LoadVM,
	})
	if err != nil {
		return nil, err
	}
	bin := p.QEMUBinary()
	cmd := exec.Command(bin, args...)
	cmd.Dir = p.Dir
	stderr := newRingBuffer(64 << 10)
	cmd.Stderr = io.MultiWriter(stderr, logWriter{deps.Log, "qemu stderr"})
	cmd.Stdout = logWriter{deps.Log, "qemu stdout"}
	deps.Log.Info("starting QEMU", "binary", bin, "accel", accel, "profile", p.Name, "args", strings.Join(args, " "))
	if err := deps.Procs.Start(cmd); err != nil {
		if errors.Is(err, exec.ErrNotFound) || os.IsNotExist(err) {
			return nil, &StartError{Code: CodeQEMUNotFound, Title: "Android sandbox could not start: the virtualization engine (QEMU) is missing.",
				Causes: []string{"Reinstall droidpector to restore its runtime files."}, Details: err.Error()}
		}
		return nil, &StartError{Code: CodeQEMUFailed, Title: "Android sandbox could not start.", Details: err.Error()}
	}
	mctx, cancel := context.WithCancel(context.Background())
	m := &Machine{Profile: p, Accel: accel, deps: deps, cmd: cmd, stderr: stderr, exited: make(chan struct{}), stopReq: make(chan struct{}),
		vncAddr: fmt.Sprintf("127.0.0.1:%d", vncPort), vncPass: randomPassword(), cancel: cancel}
	go func() {
		m.exitErr = cmd.Wait()
		close(m.exited)
	}()

	// Wait for QEMU to connect its three channels, or to exit early.
	type accepted struct {
		c   net.Conn
		err error
	}
	acceptOne := func(l net.Listener) <-chan accepted {
		ch := make(chan accepted, 1)
		go func() {
			c, err := l.Accept()
			ch <- accepted{c, err}
		}()
		return ch
	}
	chans := []<-chan accepted{acceptOne(qmpL), acceptOne(serL), acceptOne(netL)}
	conns := make([]net.Conn, 3)
	timeout := time.NewTimer(opts.StartTimeout)
	defer timeout.Stop()
	fail := func(e error) (*Machine, error) {
		cancel()
		for _, c := range conns {
			if c != nil {
				c.Close()
			}
		}
		m.kill()
		return nil, e
	}
	for i, ch := range chans {
		select {
		case a := <-ch:
			if a.err != nil {
				return fail(a.err)
			}
			conns[i] = a.c
		case <-m.exited:
			time.Sleep(100 * time.Millisecond) // let stderr drain
			return fail(classifyQEMUFailure(stderr.String(), m.exitErr, accel))
		case <-timeout.C:
			return fail(&StartError{Code: CodeQEMUFailed, Title: "Android sandbox could not start: the virtual machine did not respond.", Details: stderr.String()})
		case <-ctx.Done():
			return fail(ctx.Err())
		}
	}
	qctx, qcancel := context.WithTimeout(ctx, 20*time.Second)
	defer qcancel()
	q, err := NewQMP(qctx, conns[0])
	if err != nil {
		return fail(&StartError{Code: CodeQEMUFailed, Title: "Android sandbox could not start: the VM control channel failed.", Details: err.Error()})
	}
	m.qmp = q
	if err := q.Execute(qctx, "set_password", map[string]any{"protocol": "vnc", "password": m.vncPass}, nil); err != nil {
		return fail(&StartError{Code: CodeQEMUFailed, Title: "Android sandbox could not start: securing the display failed.", Details: err.Error()})
	}
	var vnc struct {
		Host    string `json:"host"`
		Service string `json:"service"`
	}
	if err := q.Execute(qctx, "query-vnc", nil, &vnc); err != nil || vnc.Service == "" {
		return fail(&StartError{Code: CodeQEMUFailed, Title: "Android sandbox could not start: the display server did not start.", Details: fmt.Sprintf("%v %+v", err, vnc)})
	}
	m.vncAddr = net.JoinHostPort("127.0.0.1", vnc.Service)
	m.console = NewConsole(conns[1])
	go func() {
		if err := deps.AttachNet(mctx, conns[2]); err != nil {
			deps.Log.Warn("virtual NIC link ended", "err", err)
		}
	}()
	go m.watch()
	deps.Log.Info("QEMU running", "pid", cmd.Process.Pid, "accel", accel, "vnc", m.vncAddr)
	return m, nil
}

// watch forwards QMP events and process exit as Events.
func (m *Machine) watch() {
	emit := func(e Event) {
		if m.deps.OnEvent != nil {
			m.deps.OnEvent(e)
		}
	}
	for {
		select {
		case ev, ok := <-m.qmp.Events():
			if !ok {
				<-m.exited
				m.emitExit(emit)
				return
			}
			switch ev.Name {
			case "GUEST_PANICKED":
				emit(Event{Type: EventPanic, Detail: string(ev.Data), Unexpected: true})
			case "RESET":
				emit(Event{Type: EventReset, Detail: string(ev.Data)})
			case "SHUTDOWN":
				emit(Event{Type: EventShutdown, Detail: string(ev.Data)})
			}
		case <-m.exited:
			m.emitExit(emit)
			return
		}
	}
}

func (m *Machine) emitExit(emit func(Event)) {
	m.cancel()
	requested := false
	select {
	case <-m.stopReq:
		requested = true
	default:
	}
	detail := "QEMU exited"
	if m.exitErr != nil {
		detail = m.exitErr.Error()
	}
	if s := strings.TrimSpace(m.stderr.String()); s != "" {
		detail += ": " + lastLines(s, 5)
	}
	if !requested {
		m.deps.Log.Error("virtual machine exited unexpectedly", "detail", detail)
	}
	emit(Event{Type: EventExited, Detail: detail, Unexpected: !requested})
}

// QMP returns the control client.
func (m *Machine) QMP() *QMP { return m.qmp }

// Console returns the serial console agent.
func (m *Machine) Console() *Console { return m.console }

// VNC returns the loopback display address and its per-boot password.
func (m *Machine) VNC() (addr, password string) { return m.vncAddr, m.vncPass }

// Exited is closed when the QEMU process ends.
func (m *Machine) Exited() <-chan struct{} { return m.exited }

// Stderr returns QEMU's recent diagnostic output.
func (m *Machine) Stderr() string { return m.stderr.String() }

// Stop ends the VM: QMP "quit" (disk caches flushed by QEMU), then a hard
// kill if QEMU does not exit within grace. The guest should be shut down
// cleanly beforehand by the caller when possible (Android via ADB).
func (m *Machine) Stop(grace time.Duration) {
	m.stopping.Do(func() { close(m.stopReq) })
	select {
	case <-m.exited:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	m.qmp.Execute(ctx, "quit", nil, nil)
	cancel()
	select {
	case <-m.exited:
	case <-time.After(grace):
		m.kill()
	}
}

func (m *Machine) kill() {
	m.stopping.Do(func() { close(m.stopReq) })
	if m.cmd.Process != nil {
		m.cmd.Process.Kill()
	}
	<-m.exited
	if m.console != nil {
		m.console.Close()
	}
	if m.qmp != nil {
		m.qmp.Close()
	}
}

// SaveSnapshot saves the complete VM state as tag.
func (m *Machine) SaveSnapshot(ctx context.Context, tag string) error {
	return m.qmp.SaveSnapshot(ctx, tag, DataNode)
}

// LoadSnapshot restores tag in the running VM.
func (m *Machine) LoadSnapshot(ctx context.Context, tag string) error {
	return m.qmp.LoadSnapshot(ctx, tag, DataNode)
}

// DeleteSnapshot removes tag.
func (m *Machine) DeleteSnapshot(ctx context.Context, tag string) error {
	return m.qmp.DeleteSnapshot(ctx, tag, DataNode)
}

// Snapshots lists the saved snapshots.
func (m *Machine) Snapshots(ctx context.Context) ([]Snapshot, error) { return m.qmp.ListSnapshots(ctx) }

// ---- helpers -----------------------------------------------------------------

type ringBuffer struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.b = append(r.b, p...)
	if len(r.b) > r.max {
		r.b = append([]byte(nil), r.b[len(r.b)-r.max:]...)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.b)
}

type logWriter struct {
	log *slog.Logger
	msg string
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, l := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			w.log.Info(w.msg, "line", l)
		}
	}
	return len(p), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
