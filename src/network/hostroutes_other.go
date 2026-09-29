//go:build !windows

package network

import (
	"net"
	"net/netip"
)

// HostRoutes approximates the host's IPv4 routes with the networks of its
// interfaces (which include VPN tunnel interfaces).
func HostRoutes() ([]netip.Prefix, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap().Is4() {
				bits, _ := n.Mask.Size()
				out = append(out, netip.PrefixFrom(ip.Unmap(), bits))
			}
		}
	}
	return out, nil
}
