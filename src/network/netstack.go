package network

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// Addressing of the private sandbox network (mirrors QEMU slirp defaults so
// guest images behave as they would under the stock emulator).
type Addressing struct {
	Subnet     netip.Prefix // 10.0.2.0/24
	Gateway    netip.Addr   // 10.0.2.2  — gateway, DHCP server
	DNS        netip.Addr   // 10.0.2.3  — DNS server
	Guest      netip.Addr   // 10.0.2.15 — the single guest
	GatewayMAC net.HardwareAddr
	MTU        uint32
}

// DefaultAddressing returns the standard sandbox addressing plan.
func DefaultAddressing() Addressing {
	return Addressing{
		Subnet:     netip.MustParsePrefix("10.0.2.0/24"),
		Gateway:    netip.MustParseAddr("10.0.2.2"),
		DNS:        netip.MustParseAddr("10.0.2.3"),
		Guest:      netip.MustParseAddr("10.0.2.15"),
		GatewayMAC: net.HardwareAddr{0x5a, 0x94, 0xef, 0xe4, 0x0c, 0xdd},
		MTU:        1500,
	}
}

const nicID tcpip.NICID = 1

// Stack is the user-mode TCP/IP stack the guest NIC is attached to.
type Stack struct {
	s    *stack.Stack
	link *channel.Endpoint
	addr Addressing
	log  *slog.Logger

	mu       sync.Mutex
	attached bool
}

// TCPHandler receives every TCP connection the guest opens.
type TCPHandler func(r *tcp.ForwarderRequest)

// UDPHandler receives every UDP flow the guest opens (not DHCP).
type UDPHandler func(r *udp.ForwarderRequest) bool

func toTCPIP(a netip.Addr) tcpip.Address { return tcpip.AddrFromSlice(a.AsSlice()) }

// FromTCPIP converts a gVisor address back to netip.
func FromTCPIP(a tcpip.Address) netip.Addr {
	ip, _ := netip.AddrFromSlice(a.AsSlice())
	return ip.Unmap()
}

// NewStack builds the stack. Every guest TCP SYN and UDP datagram that is not
// addressed to a local service is handed to the handlers, whatever its
// destination (promiscuous + spoofing), which is what makes the gateway a
// transparent interception point.
func NewStack(addr Addressing, onTCP TCPHandler, onUDP UDPHandler, log *slog.Logger) (*Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4},
		HandleLocal:        false,
	})
	sack := tcpip.TCPSACKEnabled(true)
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		return nil, fmt.Errorf("enabling TCP SACK: %s", err)
	}
	link := channel.New(1024, addr.MTU, tcpip.LinkAddress(addr.GatewayMAC))
	if err := s.CreateNIC(nicID, ethernet.New(link)); err != nil {
		return nil, fmt.Errorf("creating sandbox NIC: %s", err)
	}
	for _, a := range []netip.Addr{addr.Gateway, addr.DNS} {
		pa := tcpip.ProtocolAddress{
			Protocol:          ipv4.ProtocolNumber,
			AddressWithPrefix: tcpip.AddressWithPrefix{Address: toTCPIP(a), PrefixLen: addr.Subnet.Bits()},
		}
		if err := s.AddProtocolAddress(nicID, pa, stack.AddressProperties{}); err != nil {
			return nil, fmt.Errorf("assigning %s: %s", a, err)
		}
	}
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("promiscuous mode: %s", err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("spoofing: %s", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})

	tcpFwd := tcp.NewForwarder(s, 0, 4096, func(r *tcp.ForwarderRequest) { onTCP(r) })
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool { return onUDP(r) })
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	return &Stack{s: s, link: link, addr: addr, log: log}, nil
}

// Addressing returns the network plan.
func (st *Stack) Addressing() Addressing { return st.addr }

// Close tears down the stack.
func (st *Stack) Close() {
	st.link.Close()
	st.s.Close()
	st.s.Wait()
}

// ListenUDP binds a UDP socket inside the stack (used for DHCP), with
// broadcast enabled.
func (st *Stack) ListenUDP(port uint16) (*gonet.UDPConn, error) {
	var wq waiter.Queue
	ep, err := st.s.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
	if err != nil {
		return nil, fmt.Errorf("creating UDP endpoint: %s", err)
	}
	ep.SocketOptions().SetBroadcast(true)
	if err := ep.Bind(tcpip.FullAddress{NIC: nicID, Port: port}); err != nil {
		ep.Close()
		return nil, fmt.Errorf("binding UDP port %d: %s", port, err)
	}
	return gonet.NewUDPConn(&wq, ep), nil
}

// DialGuest opens a TCP connection from the gateway to the guest (used for
// ADB). It never touches the host network.
func (st *Stack) DialGuest(ctx context.Context, port uint16) (net.Conn, error) {
	c, err := gonet.DialTCPWithBind(ctx, st.s,
		tcpip.FullAddress{NIC: nicID, Addr: toTCPIP(st.addr.Gateway)},
		tcpip.FullAddress{NIC: nicID, Addr: toTCPIP(st.addr.Guest), Port: port},
		ipv4.ProtocolNumber)
	if err != nil {
		return nil, fmt.Errorf("connecting to guest port %d: %w", port, err)
	}
	return c, nil
}

// ErrAlreadyAttached is returned when a second VM link is attached.
var ErrAlreadyAttached = errors.New("a virtual machine is already attached to the sandbox network")

// maxFrame bounds frames read from QEMU (MTU + ethernet header + slack).
const maxFrame = 65536

// ServeQEMU runs the link between the stack and a QEMU "socket"/"stream"
// netdev connection: each Ethernet frame is prefixed with its 32-bit
// big-endian length. It returns when the connection or ctx ends.
func (st *Stack) ServeQEMU(ctx context.Context, conn io.ReadWriteCloser) error {
	st.mu.Lock()
	if st.attached {
		st.mu.Unlock()
		return ErrAlreadyAttached
	}
	st.attached = true
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.attached = false
		st.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	// Outbound: stack → QEMU.
	errc := make(chan error, 1)
	go func() {
		defer cancel() // a write failure tears the link down
		w := bufio.NewWriterSize(conn, 256<<10)
		var hdr [4]byte
		for {
			pkt := st.link.ReadContext(ctx)
			if pkt == nil {
				errc <- ctx.Err()
				return
			}
			n := pkt.Size()
			binary.BigEndian.PutUint32(hdr[:], uint32(n))
			_, err := w.Write(hdr[:])
			if err == nil {
				for _, s := range pkt.AsSlices() {
					if _, err = w.Write(s); err != nil {
						break
					}
				}
			}
			pkt.DecRef()
			// Flush when no more packets are immediately pending (batching).
			if err == nil && st.link.NumQueued() == 0 {
				err = w.Flush()
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	// Inbound: QEMU → stack.
	r := bufio.NewReaderSize(conn, 256<<10)
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			cancel()
			<-errc
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reading from virtual NIC: %w", err)
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n < header.EthernetMinimumSize || n > maxFrame {
			cancel()
			<-errc
			return fmt.Errorf("virtual NIC sent an invalid frame length %d", n)
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(r, frame); err != nil {
			cancel()
			<-errc
			return fmt.Errorf("reading frame from virtual NIC: %w", err)
		}
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
		st.link.InjectInbound(0, pkt)
		pkt.DecRef()
	}
}
