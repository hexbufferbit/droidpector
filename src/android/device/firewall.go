package device

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The sandbox firewall lives in the guest (iptables owner matching), the same
// mechanism Android itself uses for per-app data restrictions. It is the only
// place where the sending UID is known synchronously, so enforcement is exact:
// a blocked app's SYN never leaves Android, and the gateway never sees it.
const fwChain = "droidpector"

// FirewallRules describes what may leave the sandbox besides the allowed apps.
type FirewallRules struct {
	// AllowUIDs are the apps under test (their Linux UIDs).
	AllowUIDs []int
	// AllowDNS keeps name resolution working (port 53). Android resolves on
	// behalf of apps from a system process, so this cannot be attributed and
	// must stay open for the allowed apps to work.
	AllowDNS bool
}

// firewallScript builds the idempotent iptables program applied by
// SetAppFirewall. Exposed for tests.
func firewallScript(r FirewallRules) string {
	var b strings.Builder
	// Every mandatory rule fails the script explicitly ("|| exit 1"); optional
	// steps (deleting a rule that may not exist yet) never abort it. No
	// "set -e": with it, a harmless failing "-D" would end the script early.
	must := func(format string, a ...any) { fmt.Fprintf(&b, format+" || exit 1\n", a...) }
	opt := func(format string, a ...any) { fmt.Fprintf(&b, format+" 2>/dev/null || true\n", a...) }
	for _, ipt := range []string{"iptables", "ip6tables"} {
		opt("%s -w -N %s", ipt, fwChain)
		must("%s -w -F %s", ipt, fwChain)
		// Order matters: our chain must run before Android's own OUTPUT rules.
		opt("%s -w -D OUTPUT -j %s", ipt, fwChain)
		must("%s -w -I OUTPUT 1 -j %s", ipt, fwChain)
		must("%s -w -A %s -o lo -j RETURN", ipt, fwChain)
		// Replies to connections the host opened into the guest (ADB).
		must("%s -w -A %s -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN", ipt, fwChain)
		if ipt == "iptables" {
			must("%s -w -A %s -p udp --dport 67:68 -j RETURN", ipt, fwChain) // DHCP
		}
		if r.AllowDNS {
			must("%s -w -A %s -p udp --dport 53 -j RETURN", ipt, fwChain)
			must("%s -w -A %s -p tcp --dport 53 -j RETURN", ipt, fwChain)
		}
		for _, uid := range r.AllowUIDs {
			must("%s -w -A %s -m owner --uid-owner %d -j RETURN", ipt, fwChain, uid)
		}
		// Reject (not drop): blocked apps fail immediately instead of hanging.
		// Kernels without the REJECT target fall back to DROP.
		reject := "icmp-port-unreachable"
		if ipt == "ip6tables" {
			reject = "icmp6-port-unreachable"
		}
		fmt.Fprintf(&b, "%s -w -A %s -p tcp -j REJECT --reject-with tcp-reset 2>/dev/null || %s -w -A %s -p tcp -j DROP || exit 1\n", ipt, fwChain, ipt, fwChain)
		fmt.Fprintf(&b, "%s -w -A %s -j REJECT --reject-with %s 2>/dev/null || %s -w -A %s -j DROP || exit 1\n", ipt, fwChain, reject, ipt, fwChain)
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// SetAppFirewall installs (or replaces) the sandbox firewall so only the
// given UIDs (plus DNS/DHCP and loopback) can open network connections.
// Requires root.
func (d *Device) SetAppFirewall(ctx context.Context, rules FirewallRules) error {
	if _, err := d.runRootScript(ctx, firewallScript(rules)); err != nil {
		return fmt.Errorf("applying the sandbox firewall: %w", err)
	}
	return nil
}

// ClearAppFirewall removes the firewall (everything may connect again).
func (d *Device) ClearAppFirewall(ctx context.Context) error {
	script := ""
	for _, ipt := range []string{"iptables", "ip6tables"} {
		script += fmt.Sprintf("%s -w -D OUTPUT -j %s 2>/dev/null; %s -w -F %s 2>/dev/null; %s -w -X %s 2>/dev/null; true\n", ipt, fwChain, ipt, fwChain, ipt, fwChain)
	}
	if _, err := d.runRootScript(ctx, script); err != nil {
		return fmt.Errorf("removing the sandbox firewall: %w", err)
	}
	return nil
}

var rejectCountRe = regexp.MustCompile(`(?m)^\s*(\d+)\s+\d+\s+REJECT`)

// FirewallBlockedCount returns how many packets the firewall rejected so far
// (sum of the REJECT rule counters of both chains).
func (d *Device) FirewallBlockedCount(ctx context.Context) (int64, error) {
	r, err := d.runRootScript(ctx, "iptables -w -L "+fwChain+" -v -x -n 2>/dev/null; ip6tables -w -L "+fwChain+" -v -x -n 2>/dev/null; true")
	if err != nil {
		return 0, err
	}
	return parseRejectCount(r.stdout), nil
}

func parseRejectCount(out string) int64 {
	var total int64
	for _, m := range rejectCountRe.FindAllStringSubmatch(out, -1) {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		total += n
	}
	return total
}

// PackageUID returns the Linux UID of an installed package.
func (d *Device) PackageUID(ctx context.Context, pkg string) (int, error) {
	if err := checkPackage(pkg); err != nil {
		return 0, err
	}
	pkgs, err := d.ListPackagesWithUID(ctx)
	if err != nil {
		return 0, err
	}
	uid, ok := pkgs[pkg]
	if !ok {
		return 0, ErrNotInstalled
	}
	return uid, nil
}
