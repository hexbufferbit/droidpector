package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
)

// Config is the user-editable configuration (config.json). Unknown fields are
// ignored and missing fields take defaults, so old files keep working.
type Config struct {
	// VM
	MemoryMB        int    `json:"memoryMB"`
	CPUs            int    `json:"cpus"`
	Accelerator     string `json:"accelerator"`    // "auto", "whpx", "kvm", "hvf", "tcg"
	DefaultRuntime  string `json:"defaultRuntime"` // "x86_64" or "arm64"
	BootTimeoutSec  int    `json:"bootTimeoutSec"`
	AutoRestart     bool   `json:"autoRestart"` // restart the VM after a crash
	UseBootSnapshot bool   `json:"useBootSnapshot"`

	// Network
	InspectHTTPS  bool     `json:"inspectHttps"`
	AppOnly       bool     `json:"appOnlyTraffic"` // only the app under test may reach the network
	BlockPrivate  bool     `json:"blockPrivateNetworks"`
	BlockQUIC     bool     `json:"blockQuic"`
	MaxBodyMB     int      `json:"maxBodyMB"`
	HostMappings  []string `json:"hostMappings"`  // "host=ip:port" (advanced / tests)
	ExtraRootsPEM string   `json:"extraRootsPem"` // path to extra upstream trust anchors (corporate proxies, tests)
	// SandboxSubnet is the guest network: "auto" picks one that does not
	// overlap the host's routes (VPNs, LANs), or an IPv4 CIDR such as
	// "172.31.254.0/24".
	SandboxSubnet string `json:"sandboxSubnet"`

	// Storage
	KeepSessions int `json:"keepSessions"` // unsaved sessions retained
	KeepDays     int `json:"keepDays"`     // unsaved sessions older than this are removed
	WriteQueue   int `json:"writeQueue"`

	// Logging
	LogLevel string `json:"logLevel"` // TRACE, DEBUG, INFO, WARN, ERROR
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	return Config{
		MemoryMB: 4096, CPUs: 4, Accelerator: "auto", DefaultRuntime: "x86_64", BootTimeoutSec: 600,
		AutoRestart: true, UseBootSnapshot: true,
		InspectHTTPS: true, AppOnly: true, BlockQUIC: true, MaxBodyMB: 10, SandboxSubnet: "auto",
		KeepSessions: 50, KeepDays: 30, WriteQueue: 8192,
		LogLevel: "INFO",
	}
}

// Validate clamps values into supported ranges and rejects nonsense.
func (c *Config) Validate() error {
	var errs []error
	switch c.Accelerator {
	case "auto", "whpx", "kvm", "hvf", "tcg":
	default:
		errs = append(errs, fmt.Errorf("accelerator %q is not one of auto, whpx, kvm, hvf, tcg", c.Accelerator))
	}
	switch c.DefaultRuntime {
	case "x86_64", "arm64":
	default:
		errs = append(errs, fmt.Errorf("defaultRuntime %q is not x86_64 or arm64", c.DefaultRuntime))
	}
	if c.SandboxSubnet == "" {
		c.SandboxSubnet = "auto"
	}
	if c.SandboxSubnet != "auto" {
		if p, err := netip.ParsePrefix(c.SandboxSubnet); err != nil || !p.Addr().Is4() || p.Bits() < 16 || p.Bits() > 28 {
			errs = append(errs, fmt.Errorf("sandboxSubnet %q must be \"auto\" or an IPv4 network between /16 and /28, e.g. 172.31.254.0/24", c.SandboxSubnet))
			c.SandboxSubnet = "auto"
		}
	}
	if _, err := ParseLevel(c.LogLevel); err != nil {
		errs = append(errs, err)
	}
	c.MemoryMB = clamp(c.MemoryMB, 2048, 16384)
	c.CPUs = clamp(c.CPUs, 1, 16)
	c.BootTimeoutSec = clamp(c.BootTimeoutSec, 60, 3600)
	c.MaxBodyMB = clamp(c.MaxBodyMB, 1, 512)
	c.KeepSessions = clamp(c.KeepSessions, 1, 10000)
	c.KeepDays = clamp(c.KeepDays, 1, 3650)
	c.WriteQueue = clamp(c.WriteQueue, 256, 1<<20)
	return errors.Join(errs...)
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// LoadConfig reads path, creating it with defaults if it does not exist. A
// corrupt file is preserved as config.json.corrupt and defaults are used.
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return c, SaveConfig(path, c)
	case err != nil:
		return c, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		os.Rename(path, path+".corrupt")
		d := DefaultConfig()
		SaveConfig(path, d)
		return d, fmt.Errorf("configuration file was invalid (kept as %s.corrupt); defaults restored: %w", filepath.Base(path), err)
	}
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("configuration: %w", err)
	}
	return c, nil
}

// SaveConfig writes the configuration atomically.
func SaveConfig(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
