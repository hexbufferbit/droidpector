// Package guestsim simulates the Android guest's network side for tests: a
// second user-mode TCP/IP stack attached to the gateway over the exact QEMU
// socket-netdev framing a real VM uses. Test infrastructure only.
package guestsim

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// Guest is a simulated guest network interface.
type Guest struct {
	s      *stack.Stack
	link   *channel.Endpoint
	IP     netip.Addr
	DNS    netip.Addr
	cancel context.CancelFunc
}

func addr(a netip.Addr) tcpip.Address { return tcpip.AddrFromSlice(a.AsSlice()) }

// New attaches a guest with a static IPv4 configuration to conn (the other
// end of the gateway's ServeQEMU). mac is the guest MAC address.
func New(conn io.ReadWriteCloser, ip, gateway, dns netip.Addr) (*Guest, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	link := channel.New(1024, 1500, tcpip.LinkAddress([]byte{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}))
	if err := s.CreateNIC(1, ethernet.New(link)); err != nil {
		return nil, fmt.Errorf("guest NIC: %s", err)
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: addr(ip), PrefixLen: 24}}, stack.AddressProperties{}); err != nil {
		return nil, fmt.Errorf("guest address: %s", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, Gateway: addr(gateway), NIC: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	g := &Guest{s: s, link: link, IP: ip, DNS: dns, cancel: cancel}
	go g.pump(ctx, conn)
	return g, nil
}

func (g *Guest) pump(ctx context.Context, conn io.ReadWriteCloser) {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go func() {
		w := bufio.NewWriter(conn)
		for {
			pkt := g.link.ReadContext(ctx)
			if pkt == nil {
				return
			}
			var hdr [4]byte
			binary.BigEndian.PutUint32(hdr[:], uint32(pkt.Size()))
			w.Write(hdr[:])
			for _, s := range pkt.AsSlices() {
				w.Write(s)
			}
			pkt.DecRef()
			if g.link.NumQueued() == 0 {
				if w.Flush() != nil {
					return
				}
			}
		}
	}()
	r := bufio.NewReader(conn)
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		frame := make([]byte, binary.BigEndian.Uint32(hdr[:]))
		if _, err := io.ReadFull(r, frame); err != nil {
			return
		}
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
		g.link.InjectInbound(0, pkt)
		pkt.DecRef()
	}
}

// Close detaches the guest.
func (g *Guest) Close() {
	g.cancel()
	g.link.Close()
	g.s.Close()
}

// DialTCP opens a TCP connection from the guest.
func (g *Guest) DialTCP(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
	return gonet.DialContextTCP(ctx, g.s, tcpip.FullAddress{NIC: 1, Addr: addr(dst.Addr()), Port: dst.Port()}, ipv4.ProtocolNumber)
}

// DialUDP opens a connected UDP socket from the guest.
func (g *Guest) DialUDP(dst netip.AddrPort) (net.Conn, error) {
	return gonet.DialUDP(g.s, nil, &tcpip.FullAddress{NIC: 1, Addr: addr(dst.Addr()), Port: dst.Port()}, ipv4.ProtocolNumber)
}

// Listen accepts TCP connections inside the guest (e.g. a fake adbd).
func (g *Guest) Listen(port uint16) (net.Listener, error) {
	return gonet.ListenTCP(g.s, tcpip.FullAddress{NIC: 1, Addr: addr(g.IP), Port: port}, ipv4.ProtocolNumber)
}

// BroadcastUDP sends one datagram from src port to the broadcast address and
// returns the first reply (used to test DHCP before the guest has an address).
func (g *Guest) BroadcastUDP(srcPort, dstPort uint16, payload []byte, timeout time.Duration) ([]byte, error) {
	var wq waiter.Queue
	ep, err := g.s.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	if err != nil {
		return nil, fmt.Errorf("endpoint: %s", err)
	}
	ep.SocketOptions().SetBroadcast(true)
	if err := ep.Bind(tcpip.FullAddress{NIC: 1, Port: srcPort}); err != nil {
		return nil, fmt.Errorf("bind: %s", err)
	}
	c := gonet.NewUDPConn(&wq, ep)
	defer c.Close()
	if _, err := c.WriteTo(payload, &net.UDPAddr{IP: net.IPv4bcast, Port: int(dstPort)}); err != nil {
		return nil, err
	}
	c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1500)
	n, _, rerr := c.ReadFrom(buf)
	if rerr != nil {
		return nil, rerr
	}
	return buf[:n], nil
}

// Resolver resolves names through the sandbox DNS server, like Android does.
func (g *Guest) Resolver() *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		if network == "tcp" || network == "tcp4" {
			return g.DialTCP(ctx, netip.AddrPortFrom(g.DNS, 53))
		}
		return g.DialUDP(netip.AddrPortFrom(g.DNS, 53))
	}}
}

// HTTPClient returns a client that behaves like an app inside the guest:
// guest DNS, guest TCP, and trust in the given roots (the guest system store).
func (g *Guest) HTTPClient(roots *x509.CertPool, h2 bool) *http.Client {
	res := g.Resolver()
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := res.LookupNetIP(ctx, "ip4", host)
			if err != nil {
				return nil, err
			}
			p, _ := net.LookupPort("tcp", port)
			return g.DialTCP(ctx, netip.AddrPortFrom(ips[0], uint16(p)))
		},
		TLSClientConfig:   &tls.Config{RootCAs: roots},
		ForceAttemptHTTP2: h2,
		DisableKeepAlives: false,
	}
	if !h2 {
		t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{Transport: t, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
