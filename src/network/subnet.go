package network

import (
	"fmt"
	"net/netip"
	"strings"
)

// The guest routes its own subnet on-link, so any destination inside it
// never reaches the gateway. When the host reaches a network through a VPN
// (or a LAN) that overlaps the sandbox subnet, those servers would be
// unreachable from the app. The subnet is therefore chosen to avoid every
// network the host has a route to.
// virtualIPs is where host mappings get their addresses (see vipBase).
var virtualIPs = netip.PrefixFrom(vipBase, 15)

var subnetCandidates = []netip.Prefix{
	netip.MustParsePrefix("10.0.2.0/24"), // QEMU's default: keeps existing quick-start snapshots valid
	netip.MustParsePrefix("172.31.254.0/24"),
	netip.MustParsePrefix("192.168.254.0/24"),
	netip.MustParsePrefix("10.254.254.0/24"),
	netip.MustParsePrefix("100.127.254.0/24"),
	netip.MustParsePrefix("172.16.254.0/24"),
}

// AddressingFor builds the addressing plan for an IPv4 subnet: gateway .2,
// DNS .3, guest .15 (the QEMU slirp layout).
func AddressingFor(subnet netip.Prefix) (Addressing, error) {
	if !subnet.IsValid() || !subnet.Addr().Is4() {
		return Addressing{}, fmt.Errorf("sandbox subnet %q is not an IPv4 network", subnet)
	}
	if subnet.Bits() < 16 || subnet.Bits() > 28 {
		return Addressing{}, fmt.Errorf("sandbox subnet %s must be between /16 and /28", subnet)
	}
	subnet = subnet.Masked()
	if subnet.Overlaps(virtualIPs) {
		return Addressing{}, fmt.Errorf("sandbox subnet %s overlaps the reserved range %s", subnet, virtualIPs)
	}
	base := subnet.Addr().As4()
	host := func(n byte) netip.Addr {
		b := base
		b[3] += n
		return netip.AddrFrom4(b)
	}
	a := DefaultAddressing()
	a.Subnet, a.Gateway, a.DNS, a.Guest = subnet, host(2), host(3), host(15)
	return a, nil
}

// SubnetChoice is the outcome of ChooseAddressing.
type SubnetChoice struct {
	Addressing Addressing
	// Conflicts are host routes that overlap the chosen subnet (only when no
	// conflict-free subnet exists, or a configured one conflicts).
	Conflicts []netip.Prefix
}

// Warning explains a remaining conflict in user terms ("" when none).
func (c SubnetChoice) Warning() string {
	if len(c.Conflicts) == 0 {
		return ""
	}
	nets := make([]string, len(c.Conflicts))
	for i, p := range c.Conflicts {
		nets[i] = p.String()
	}
	return fmt.Sprintf("The sandbox network %s overlaps a network this computer reaches (%s, e.g. through a VPN). Servers in that range are unreachable from the app. Restart droidpector after connecting the VPN (it then picks a free range), or set \"sandboxSubnet\" in config.json to an unused range.",
		c.Addressing.Subnet, strings.Join(nets, ", "))
}

// ChooseAddressing picks the sandbox subnet. configured is "" or "auto" for
// automatic selection, or an explicit IPv4 CIDR. hostRoutes are the host's
// IPv4 routes; default-like routes (shorter than /8, e.g. a full-tunnel VPN's
// 0.0.0.0/1 + 128.0.0.0/1) do not count as conflicts, since traffic to them
// still reaches the gateway.
func ChooseAddressing(configured string, hostRoutes []netip.Prefix) (SubnetChoice, error) {
	routes := relevantRoutes(hostRoutes)
	if configured != "" && !strings.EqualFold(configured, "auto") {
		p, err := netip.ParsePrefix(strings.TrimSpace(configured))
		if err != nil {
			return SubnetChoice{}, fmt.Errorf("sandboxSubnet %q is not a network like 172.31.254.0/24: %w", configured, err)
		}
		a, err := AddressingFor(p)
		if err != nil {
			return SubnetChoice{}, err
		}
		return SubnetChoice{Addressing: a, Conflicts: conflicts(a.Subnet, routes)}, nil
	}
	var best SubnetChoice
	for i, p := range subnetCandidates {
		a, _ := AddressingFor(p)
		c := SubnetChoice{Addressing: a, Conflicts: conflicts(p, routes)}
		if len(c.Conflicts) == 0 {
			return c, nil
		}
		if i == 0 || len(c.Conflicts) < len(best.Conflicts) {
			best = c
		}
	}
	return best, nil
}

func relevantRoutes(routes []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, r := range routes {
		r = r.Masked()
		a := r.Addr()
		switch {
		case !a.Is4() || r.Bits() < 8:
		case a.IsLoopback() || a.IsMulticast() || a.IsLinkLocalUnicast():
		case r.Bits() == 32 && a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		default:
			out = append(out, r)
		}
	}
	return out
}

func conflicts(subnet netip.Prefix, routes []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, r := range routes {
		if subnet.Overlaps(r) && !containsPrefix(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func containsPrefix(list []netip.Prefix, p netip.Prefix) bool {
	for _, q := range list {
		if q == p {
			return true
		}
	}
	return false
}
