// Package device implements high-level Android device operations (boot
// detection, package install/launch/management, text input, clock, socket
// ownership, CA installation) on top of a minimal Shell interface that the
// adb client satisfies.
package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Shell is the device transport used by Device. *adb.Conn satisfies it.
type Shell interface {
	// Shell runs cmd with /system/bin/sh and returns its output and exit code.
	Shell(ctx context.Context, cmd string) (stdout, stderr []byte, exitCode int, err error)
	// Push uploads size bytes from r to remotePath.
	Push(ctx context.Context, r io.Reader, size int64, remotePath string, mode fs.FileMode, mtime time.Time) error
}

// Errors returned by Device.
var (
	// ErrNotInstalled means the package is not installed.
	ErrNotInstalled = errors.New("package is not installed")
	// ErrRootRequired means an operation needs root and neither the shell
	// runs as root nor "su 0" is available.
	ErrRootRequired = errors.New("this operation requires root access on the device (adbd as root or su)")
	// ErrInvalidPackage is returned for malformed package names.
	ErrInvalidPackage = errors.New("invalid package name")
)

// CommandError reports a shell command that failed.
type CommandError struct {
	Cmd      string
	ExitCode int
	Output   string // stdout followed by stderr, trimmed
}

func (e *CommandError) Error() string {
	out := e.Output
	if len(out) > 512 {
		out = out[:512] + "…"
	}
	if out == "" {
		return fmt.Sprintf("device command %q failed with exit code %d", e.Cmd, e.ExitCode)
	}
	return fmt.Sprintf("device command %q failed (exit code %d): %s", e.Cmd, e.ExitCode, out)
}

// Option configures a Device.
type Option func(*Device)

// WithRandom sets the source for temporary file names (default crypto/rand).
func WithRandom(r io.Reader) Option { return func(d *Device) { d.random = r } }

// WithTempDir sets the device directory for temporary uploads
// (default /data/local/tmp).
func WithTempDir(dir string) Option {
	return func(d *Device) { d.tmpDir = strings.TrimRight(dir, "/") }
}

// Device performs operations on one Android device. It is safe for
// concurrent use.
type Device struct {
	sh     Shell
	random io.Reader
	tmpDir string

	rootMu    sync.Mutex
	rootKnown bool
	rootPfx   string // "" when the shell is root, "su 0 " when su works
	rootOK    bool
}

// New returns a Device using sh.
func New(sh Shell, opts ...Option) *Device {
	d := &Device{sh: sh, random: rand.Reader, tmpDir: "/data/local/tmp"}
	for _, o := range opts {
		o(d)
	}
	return d
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) combined() string {
	return strings.TrimSpace(r.stdout + "\n" + r.stderr)
}

func (d *Device) run(ctx context.Context, cmd string) (result, error) {
	o, e, code, err := d.sh.Shell(ctx, cmd)
	if err != nil {
		return result{}, fmt.Errorf("running %q on the device: %w", firstLine(cmd), err)
	}
	return result{stdout: string(o), stderr: string(e), code: code}, nil
}

// runOK runs cmd and fails with a *CommandError on a non-zero exit code.
func (d *Device) runOK(ctx context.Context, cmd string) (result, error) {
	r, err := d.run(ctx, cmd)
	if err != nil {
		return r, err
	}
	if r.code != 0 {
		return r, &CommandError{Cmd: firstLine(cmd), ExitCode: r.code, Output: r.combined()}
	}
	return r, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// quote quotes s for /system/bin/sh.
func quote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var packageRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)*$`)

func checkPackage(pkg string) error {
	if len(pkg) > 255 || !packageRe.MatchString(pkg) {
		return fmt.Errorf("%w: %q", ErrInvalidPackage, pkg)
	}
	return nil
}

func (d *Device) tempName(prefix, ext string) (string, error) {
	var b [8]byte
	if _, err := io.ReadFull(d.random, b[:]); err != nil {
		return "", fmt.Errorf("generating temporary file name: %w", err)
	}
	return d.tmpDir + "/" + prefix + hex.EncodeToString(b[:]) + ext, nil
}

// cleanupCtx returns a context for cleanup commands that still runs after
// ctx was cancelled.
func cleanupCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

// rootPrefix returns "" if the shell runs as root, "su 0 " if su works, or
// ErrRootRequired. The answer is cached.
func (d *Device) rootPrefix(ctx context.Context) (string, error) {
	d.rootMu.Lock()
	defer d.rootMu.Unlock()
	if d.rootKnown {
		if !d.rootOK {
			return "", ErrRootRequired
		}
		return d.rootPfx, nil
	}
	r, err := d.run(ctx, "id -u")
	if err != nil {
		return "", err
	}
	switch {
	case r.code == 0 && strings.TrimSpace(r.stdout) == "0":
		d.rootOK = true
	default:
		r, err = d.run(ctx, "su 0 id -u")
		if err != nil {
			return "", err
		}
		if r.code == 0 && strings.TrimSpace(r.stdout) == "0" {
			d.rootOK, d.rootPfx = true, "su 0 "
		}
	}
	d.rootKnown = true
	if !d.rootOK {
		return "", ErrRootRequired
	}
	return d.rootPfx, nil
}

// runRootScript runs a multi-line script as root.
func (d *Device) runRootScript(ctx context.Context, script string) (result, error) {
	pfx, err := d.rootPrefix(ctx)
	if err != nil {
		return result{}, err
	}
	if pfx == "" {
		return d.runOK(ctx, script)
	}
	return d.runOK(ctx, pfx+"sh -c "+quote(script))
}

// Available reports whether Android has finished booting and the package
// manager answers.
func (d *Device) Available(ctx context.Context) (bool, error) {
	r, err := d.run(ctx, "getprop sys.boot_completed")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(r.stdout) != "1" {
		return false, nil
	}
	r, err = d.run(ctx, "pm path android")
	if err != nil {
		return false, err
	}
	return r.code == 0 && strings.Contains(r.stdout, "package:"), nil
}

// WaitBootCompleted polls Available every poll interval until it reports
// true or ctx ends. Transient shell errors are retried.
func (d *Device) WaitBootCompleted(ctx context.Context, poll time.Duration) error {
	if poll <= 0 {
		poll = time.Second
	}
	t := time.NewTimer(0)
	defer t.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("waiting for Android to boot: %w (last error: %v)", ctx.Err(), lastErr)
			}
			return fmt.Errorf("waiting for Android to boot: %w", ctx.Err())
		case <-t.C:
		}
		ok, err := d.Available(ctx)
		if ok {
			return nil
		}
		lastErr = err
		t.Reset(poll)
	}
}

// SetTime sets the device clock (root required; tries the shell directly,
// then "su 0").
func (d *Device) SetTime(ctx context.Context, t time.Time) error {
	cmd := fmt.Sprintf("date @%d", t.Unix())
	r, err := d.run(ctx, cmd)
	if err != nil {
		return err
	}
	if r.code == 0 {
		return nil
	}
	r2, err := d.run(ctx, "su 0 "+cmd)
	if err != nil {
		return err
	}
	if r2.code != 0 {
		return fmt.Errorf("setting the device clock: %w", &CommandError{Cmd: cmd, ExitCode: r.code, Output: r.combined() + "\n" + r2.combined()})
	}
	return nil
}

// Run executes a shell command and returns its combined output and exit code.
func (d *Device) Run(ctx context.Context, cmd string) (string, int, error) {
	r, err := d.run(ctx, cmd)
	if err != nil {
		return "", -1, err
	}
	return r.combined(), r.code, nil
}

// Push uploads size bytes from r to remotePath with the given mode.
func (d *Device) Push(ctx context.Context, r io.Reader, size int64, remotePath string, mode fs.FileMode, mtime time.Time) error {
	if err := checkRemotePath(remotePath); err != nil {
		return err
	}
	return d.sh.Push(ctx, r, size, remotePath, mode, mtime)
}

func checkRemotePath(p string) error {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, " '\"`$;&|") {
		return fmt.Errorf("invalid remote path %q", p)
	}
	return nil
}
