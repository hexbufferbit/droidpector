package network

import (
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// Upstream connections are ordinary host sockets, so Windows routes them
// (through a VPN when one covers the destination). Recording the local
// address and interface of each connection makes that visible per request.

type ifaceCache struct {
	mu     sync.Mutex
	byIP   map[netip.Addr]string
	loaded time.Time
}

var egressIfaces ifaceCache

// name returns the host interface owning ip ("" if unknown). Interfaces come
// and go (a VPN connects), so a miss reloads the table at most every 2 s.
func (c *ifaceCache) name(ip netip.Addr) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.byIP[ip]; ok && time.Since(c.loaded) < time.Minute {
		return n
	}
	if time.Since(c.loaded) < 2*time.Second && c.byIP != nil {
		return c.byIP[ip]
	}
	c.byIP = map[netip.Addr]string{}
	c.loaded = time.Now()
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if addr, ok := netip.AddrFromSlice(n.IP); ok {
					c.byIP[addr.Unmap()] = ifc.Name
				}
			}
		}
	}
	return c.byIP[ip]
}

// setEgress records the local end of an upstream socket on c.
func setEgress(c *model.Conn, local net.Addr) {
	if c == nil || local == nil {
		return
	}
	ap, err := netip.ParseAddrPort(local.String())
	if err != nil {
		return
	}
	c.LocalAddr = ap.String()
	c.Interface = egressIfaces.name(ap.Addr().Unmap())
}

// routeEgress asks the host which local address it would use to reach dst
// (a connected UDP socket selects the route; nothing is sent). Used when the
// connection itself failed, to show where it was routed.
func routeEgress(dst netip.AddrPort) net.Addr {
	if !dst.IsValid() || virtualIPs.Contains(dst.Addr()) {
		return nil
	}
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(dst))
	if err != nil {
		return nil
	}
	defer c.Close()
	ap := c.LocalAddr().(*net.UDPAddr).AddrPort()
	return net.TCPAddrFromAddrPort(netip.AddrPortFrom(ap.Addr(), 0))
}
