//go:build windows

package network

import (
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// HostRoutes returns the destinations of the host's IPv4 routing table,
// including routes a VPN client installed.
func HostRoutes() ([]netip.Prefix, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	var out []netip.Prefix
	for _, row := range table.Rows() {
		dp := row.DestinationPrefix
		if dp.Prefix.Family != windows.AF_INET {
			continue
		}
		sa := (*windows.RawSockaddrInet4)(unsafe.Pointer(&dp.Prefix))
		out = append(out, netip.PrefixFrom(netip.AddrFrom4(sa.Addr), int(dp.PrefixLength)))
	}
	return out, nil
}
