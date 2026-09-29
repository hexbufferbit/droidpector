package network

import (
	"encoding/binary"
	"net"
	"net/netip"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// The sandbox answers NTP itself from the host clock, like DHCP and DNS.
// A correct clock is what HTTPS certificate validation needs, and public
// NTP servers are not always reachable (Google's time.android.com does not
// answer on some networks): Android then keeps retrying unanswered queries,
// which on slow (emulated) machines can stall the system process.

const (
	ntpPacketSize = 48
	ntpModeClient = 3
	ntpModeServer = 4
	ntpEpoch      = 2208988800 // seconds from 1900 (NTP) to 1970 (Unix)
)

func putNTPTime(b []byte, t time.Time) {
	secs := uint64(t.Unix()) + ntpEpoch
	frac := uint64(t.Nanosecond()) << 32 / uint64(time.Second)
	binary.BigEndian.PutUint32(b[0:4], uint32(secs))
	binary.BigEndian.PutUint32(b[4:8], uint32(frac))
}

// ntpReply builds the server reply to an NTP client request (nil if req is
// not one). The client's transmit timestamp is echoed as the originate
// timestamp, which clients (Android's SntpClient included) verify.
func ntpReply(req []byte, received, now time.Time, refID netip.Addr) []byte {
	if len(req) < ntpPacketSize || req[0]&0x7 != ntpModeClient {
		return nil
	}
	version := (req[0] >> 3) & 0x7
	if version < 1 || version > 4 {
		version = 4
	}
	b := make([]byte, ntpPacketSize)
	b[0] = version<<3 | ntpModeServer               // leap indicator 0: synchronized
	b[1] = 2                                        // stratum: secondary (synchronized to the host clock)
	b[2] = req[2]                                   // poll interval, as asked
	b[3] = 0xec                                     // precision: 2^-20 s
	binary.BigEndian.PutUint32(b[8:12], 1<<16/1000) // root dispersion: ~1 ms
	if refID.Is4() {
		a := refID.As4()
		copy(b[12:16], a[:])
	}
	putNTPTime(b[16:24], now) // reference
	copy(b[24:32], req[40:48])
	putNTPTime(b[32:40], received)
	putNTPTime(b[40:48], now) // transmit
	return b
}

// serveNTP answers NTP queries of one guest flow and records each as an event.
func (g *Gateway) serveNTP(conn net.Conn, client, server netip.AddrPort) {
	defer conn.Close()
	buf := make([]byte, 1024)
	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		received := time.Now()
		reply := ntpReply(buf[:n], received, time.Now(), g.opts.Addressing.Gateway)
		e := &model.Event{
			ID: model.NewID(), Kind: model.KindUDP, Category: model.CatOther, State: model.StateComplete, Initiator: model.InitiatorGuest,
			StartedAt: received, Protocol: "NTP", Host: g.hostFor(server.Addr()), Port: int(server.Port()), RequestSize: int64(n),
			Conn: &model.Conn{ClientAddr: client.String(), ServerAddr: server.String(), RemoteAddr: "answered by the sandbox (host clock)", BytesUp: int64(n)},
		}
		if reply == nil {
			e.State, e.Error = model.StateError, "not an NTP client request; not answered"
		} else if _, err := conn.Write(reply); err == nil {
			e.ResponseSize, e.Conn.BytesDown = int64(len(reply)), int64(len(reply))
		}
		e.DurationMs = ms(time.Since(received))
		g.emit(e)
	}
}
