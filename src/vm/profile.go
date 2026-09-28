package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Runtime profile names (see ADR-004).
const (
	ProfileX86_64 = "x86_64"
	ProfileARM64  = "arm64"
)

// Profile describes one Android runtime image and how to boot it. It is
// loaded from runtime/android/<name>/runtime.json so images can be updated
// without code changes.
type Profile struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Android      string            `json:"android"` // e.g. "13"
	SDK          int               `json:"sdk"`
	QEMU         string            `json:"qemu"`    // binary name, e.g. qemu-system-x86_64
	Machine      string            `json:"machine"` // e.g. q35, virt
	Kernel       string            `json:"kernel"`  // file names relative to the profile dir
	Initrd       string            `json:"initrd"`
	ISO          string            `json:"iso"`
	DataTemplate string            `json:"dataTemplate"`
	Cmdline      string            `json:"cmdline"`
	ABIs         []string          `json:"abis"`             // ro.product.cpu.abilist
	Translation  bool              `json:"translation"`      // has an ARM native-bridge translator
	Accels       []string          `json:"accels"`           // preferred accelerators in order (tcg is always the fallback)
	Display      Display           `json:"display"`          // guest screen geometry (portrait phone by default)
	SHA256       map[string]string `json:"sha256,omitempty"` // integrity of the image files

	Dir     string `json:"-"` // resolved profile directory
	QEMUDir string `json:"-"` // resolved QEMU directory
}

// Display is the guest screen: the emulated display advertises this as its
// preferred (EDID) mode, so Android boots with a phone-shaped framebuffer.
type Display struct {
	Width   int `json:"width"`
	Height  int `json:"height"`
	Density int `json:"density"` // Android dpi (320 = xhdpi phone)
}

// DefaultDisplay is used when a profile does not specify a display.
var DefaultDisplay = Display{Width: 720, Height: 1280, Density: 320}

// display returns the effective display geometry.
func (p Profile) display() Display {
	d := p.Display
	if d.Width <= 0 || d.Height <= 0 {
		d.Width, d.Height = DefaultDisplay.Width, DefaultDisplay.Height
	}
	if d.Density <= 0 {
		d.Density = DefaultDisplay.Density
	}
	return d
}

// EffectiveDisplay is the exported accessor used by provisioning.
func (p Profile) EffectiveDisplay() Display { return p.display() }

// Path returns an absolute path to a profile file.
func (p Profile) Path(name string) string { return filepath.Join(p.Dir, name) }

// QEMUBinary returns the QEMU executable path.
func (p Profile) QEMUBinary() string {
	name := p.QEMU
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	if p.QEMUDir == "" {
		return name // resolved from PATH (development)
	}
	return filepath.Join(p.QEMUDir, name)
}

// MissingFileError reports an incomplete runtime installation.
type MissingFileError struct {
	Profile string
	Files   []string
}

func (e *MissingFileError) Error() string {
	return fmt.Sprintf("the %s Android runtime is incomplete; missing: %s. Reinstall droidpector to restore the runtime files.",
		e.Profile, strings.Join(e.Files, ", "))
}

// Validate checks that every referenced file exists.
func (p Profile) Validate() error {
	var missing []string
	for _, f := range []string{p.Kernel, p.Initrd, p.ISO, p.DataTemplate} {
		if f == "" {
			continue
		}
		if _, err := os.Stat(p.Path(f)); err != nil {
			missing = append(missing, f)
		}
	}
	if p.QEMUDir != "" {
		if _, err := os.Stat(p.QEMUBinary()); err != nil {
			missing = append(missing, filepath.Base(p.QEMUBinary()))
		}
	}
	if len(missing) > 0 {
		return &MissingFileError{Profile: p.Name, Files: missing}
	}
	return nil
}

// LoadProfiles reads every runtime/android/*/runtime.json. qemuDir is the
// bundled QEMU directory ("" = use PATH, for development).
func LoadProfiles(androidDir, qemuDir string) (map[string]Profile, error) {
	entries, err := os.ReadDir(androidDir)
	if err != nil {
		return nil, fmt.Errorf("the Android runtime folder %s is missing: %w", androidDir, err)
	}
	out := map[string]Profile{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(androidDir, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
		if err != nil {
			continue
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("runtime manifest %s is invalid: %w", filepath.Join(dir, "runtime.json"), err)
		}
		if p.Name == "" {
			p.Name = e.Name()
		}
		p.Dir, p.QEMUDir = dir, qemuDir
		out[p.Name] = p
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Android runtime found in %s", androidDir)
	}
	return out, nil
}

// Names returns sorted profile names.
func Names(ps map[string]Profile) []string {
	var n []string
	for k := range ps {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}
