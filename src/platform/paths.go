// Package platform isolates OS specifics: standard directories, configuration,
// structured logging and child-process lifetime management.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AppName is used for per-user directories.
const AppName = "droidpector"

// HomeEnv overrides the data directory (tests, custom locations).
const HomeEnv = "DROIDPECTOR_HOME"

// Paths are the resolved runtime locations. Nothing is hardcoded: every path
// derives from the OS's standard per-user locations or the executable's
// install directory.
type Paths struct {
	InstallDir string // directory containing the executable
	RuntimeDir string // bundled QEMU and Android runtime (read-only)
	DataDir    string // per-user writable root
	ConfigFile string
	LogDir     string
	SandboxDir string // VM disks and snapshots
	SessionDir string // SQLite database and bodies
	KeysDir    string // ADB key (protected at rest on Windows)
	TempDir    string // cleaned on every start
}

// ResolvePaths computes the locations for this process.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("locating the executable: %w", err)
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	install := filepath.Dir(exe)
	data := os.Getenv(HomeEnv)
	if data == "" {
		data = portableDataDir(install)
	}
	if data == "" {
		if data, err = userDataDir(); err != nil {
			return Paths{}, err
		}
	}
	runtimeDir := os.Getenv("DROIDPECTOR_RUNTIME")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(install, "runtime")
	}
	return NewPaths(install, runtimeDir, data), nil
}

// NewPaths derives all locations from an install dir, runtime dir and data root.
func NewPaths(install, runtimeDir, data string) Paths {
	return Paths{
		InstallDir: install,
		RuntimeDir: runtimeDir,
		DataDir:    data,
		ConfigFile: filepath.Join(data, "config.json"),
		LogDir:     filepath.Join(data, "logs"),
		SandboxDir: filepath.Join(data, "sandbox"),
		SessionDir: filepath.Join(data, "sessions"),
		KeysDir:    filepath.Join(data, "keys"),
		TempDir:    filepath.Join(data, "tmp"),
	}
}

// portableDataDir returns <exe dir>/data for the portable layout (the exe
// next to its runtime folder): everything — sandbox, sessions, logs,
// settings — stays in the extracted folder; nothing goes to AppData or the
// registry. It returns "" when the layout is not portable or the folder is
// not writable (e.g. extracted under Program Files).
func portableDataDir(install string) string {
	if st, err := os.Stat(filepath.Join(install, "runtime")); err != nil || !st.IsDir() {
		return ""
	}
	data := filepath.Join(install, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return ""
	}
	probe, err := os.CreateTemp(data, ".write-test-*")
	if err != nil {
		return ""
	}
	probe.Close()
	os.Remove(probe.Name())
	return data
}

// userDataDir returns %LOCALAPPDATA%\droidpector on Windows (data is
// machine-specific and large, so it must not roam), the Application Support
// folder on macOS and $XDG_DATA_HOME on Linux.
func userDataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, AppName), nil
		}
		d, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locating the user data folder: %w", err)
		}
		return filepath.Join(d, AppName), nil
	case "darwin":
		d, err := os.UserConfigDir() // ~/Library/Application Support
		if err != nil {
			return "", err
		}
		return filepath.Join(d, AppName), nil
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "myapkinspector"), nil
		}
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".local", "share", "myapkinspector"), nil
	}
}

// Ensure creates the writable directories and empties TempDir.
func (p Paths) Ensure() error {
	os.RemoveAll(p.TempDir)
	for _, d := range []string{p.DataDir, p.LogDir, p.SandboxDir, p.SessionDir, p.KeysDir, p.TempDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	return nil
}
