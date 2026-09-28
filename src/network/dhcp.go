package network

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"
)

// Minimal DHCPv4 server (RFC 2131/2132) for the single sandbox guest.

const (
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpDecline  = 4
	dhcpAck      = 5
	dhcpNak      = 6
	dhcpRelease  = 7
	dhcpInform   = 8

	optPad        = 0
	optSubnetMask = 1
	optRouter     = 3
	optDNS        = 6
	optHostname   = 12
	optDomainName = 15
	optMTU        = 26
	optBroadcast  = 28
	optRequestIP  = 50
	optLeaseTime  = 51
	optMsgType    = 53
	optServerID   = 54
	optRenewal    = 58
	optRebinding  = 59
	optEnd        = 255
)

var dhcpMagic = [4]byte{99, 130, 83, 99}

// dhcpPacket is a parsed DHCP message (only the fields we need).
type dhcpPacket struct {
	Op      byte
	XID     uint32
	Flags   uint16
	CIAddr  netip.Addr
	CHAddr  net.HardwareAddr
	Options map[byte][]byte
}

func (p *dhcpPacket) msgType() byte {
	if v := p.Options[optMsgType]; len(v) == 1 {
		return v[0]
	}
	return 0
}

var errBadDHCP = errors.New("malformed DHCP packet")

func parseDHCP(b []byte) (*dhcpPacket, error) {
	if len(b) < 240 || b[0] != 1 || b[1] != 1 || b[2] != 6 || [4]byte(b[236:240]) != dhcpMagic {
		return nil, errBadDHCP
	}
	p := &dhcpPacket{
		Op:      b[0],
		XID:     binary.BigEndian.Uint32(b[4:8]),
		Flags:   binary.BigEndian.Uint16(b[10:12]),
		CIAddr:  netip.AddrFrom4([4]byte(b[12:16])),
		CHAddr:  net.HardwareAddr(append([]byte(nil), b[28:34]...)),
		Options: map[byte][]byte{},
	}
	opts := b[240:]
	for i := 0; i < len(opts); {
		code := opts[i]
		if code == optEnd {
			break
		}
		if code == optPad {
			i++
			continue
		}
		if i+1 >= len(opts) {
			return nil, errBadDHCP
		}
		l := int(opts[i+1])
		if i+2+l > len(opts) {
			return nil, errBadDHCP
		}
		p.Options[code] = append(p.Options[code], opts[i+2:i+2+l]...)
		i += 2 + l
	}
	return p, nil
}

// dhcpReply builds an OFFER/ACK/NAK for req.
func dhcpReply(req *dhcpPacket, msgType byte, a Addressing, lease time.Duration) []byte {
	b := make([]byte, 240, 320)
	b[0], b[1], b[2] = 2, 1, 6
	binary.BigEndian.PutUint32(b[4:8], req.XID)
	binary.BigEndian.PutUint16(b[10:12], req.Flags)
	if msgType != dhcpNak {
		copy(b[16:20], a.Guest.AsSlice())
	}
	copy(b[20:24], a.Gateway.AsSlice())
	copy(b[28:44], req.CHAddr)
	copy(b[236:240], dhcpMagic[:])
	opt := func(code byte, v []byte) {
		b = append(b, code, byte(len(v)))
		b = append(b, v...)
	}
	u32 := func(v uint32) []byte { x := make([]byte, 4); binary.BigEndian.PutUint32(x, v); return x }
	opt(optMsgType, []byte{msgType})
	opt(optServerID, a.Gateway.AsSlice())
	if msgType != dhcpNak {
		mask := net.CIDRMask(a.Subnet.Bits(), 32)
		opt(optSubnetMask, mask)
		opt(optRouter, a.Gateway.AsSlice())
		opt(optDNS, a.DNS.AsSlice())
		secs := uint32(lease / time.Second)
		opt(optLeaseTime, u32(secs))
		opt(optRenewal, u32(secs/2))
		opt(optRebinding, u32(secs*7/8))
		mtu := make([]byte, 2)
		binary.BigEndian.PutUint16(mtu, uint16(a.MTU))
		opt(optMTU, mtu)
		bc := a.Subnet.Masked().Addr().As4()
		for i := range bc {
			bc[i] |= ^mask[i]
		}
		opt(optBroadcast, bc[:])
	}
	return append(b, optEnd)
}

// handleDHCP computes the reply for one request (nil = no reply).
func handleDHCP(req *dhcpPacket, a Addressing, lease time.Duration) []byte {
	switch req.msgType() {
	case dhcpDiscover:
		return dhcpReply(req, dhcpOffer, a, lease)
	case dhcpRequest:
		want := req.CIAddr
		if v := req.Options[optRequestIP]; len(v) == 4 {
			want = netip.AddrFrom4([4]byte(v))
		}
		if sid := req.Options[optServerID]; len(sid) == 4 && netip.AddrFrom4([4]byte(sid)) != a.Gateway {
			return nil // client chose another server
		}
		if want.IsValid() && !want.IsUnspecified() && want != a.Guest {
			return dhcpReply(req, dhcpNak, a, lease)
		}
		return dhcpReply(req, dhcpAck, a, lease)
	case dhcpInform:
		return dhcpReply(req, dhcpAck, a, lease)
	}
	return nil // DECLINE / RELEASE need no answer
}

// ServeDHCP answers DHCP on the stack until ctx ends.
func (st *Stack) ServeDHCP(ctx context.Context, log *slog.Logger) error {
	conn, err := st.ListenUDP(67)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	buf := make([]byte, 1500)
	bcast := &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		req, err := parseDHCP(buf[:n])
		if err != nil {
			log.Debug("ignoring malformed DHCP packet", "err", err)
			continue
		}
		if reply := handleDHCP(req, st.addr, 24*time.Hour); reply != nil {
			if _, err := conn.WriteTo(reply, bcast); err != nil {
				log.Warn("DHCP reply failed", "err", err)
			}
			log.Debug("DHCP", "type", req.msgType(), "mac", req.CHAddr.String())
		}
	}
}
