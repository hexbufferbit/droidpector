package network

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

func ntpTime(b []byte) time.Time {
	secs := int64(binary.BigEndian.Uint32(b[0:4])) - ntpEpoch
	frac := int64(binary.BigEndian.Uint32(b[4:8]))
	return time.Unix(secs, frac*int64(time.Second)>>32)
}

func ntpRequest(transmit []byte) []byte {
	req := make([]byte, ntpPacketSize)
	req[0] = 4<<3 | ntpModeClient
	req[2] = 6
	copy(req[40:48], transmit)
	return req
}

// The checks mirror Android's SntpClient.checkValidServerReply.
func TestNTPReplyPassesAndroidValidation(t *testing.T) {
	transmit := []byte{0xea, 0x1b, 0x2c, 0x3d, 0x12, 0x34, 0x56, 0x78} // randomized by the client
	now := time.Date(2026, 9, 29, 23, 0, 0, 250_000_000, time.UTC)
	b := ntpReply(ntpRequest(transmit), now.Add(-time.Millisecond), now, netip.MustParseAddr("10.0.2.2"))
	if len(b) != ntpPacketSize {
		t.Fatalf("reply of %d bytes", len(b))
	}
	leap, version, mode, stratum := b[0]>>6, (b[0]>>3)&7, b[0]&7, b[1]
	if leap == 3 || mode != ntpModeServer || stratum == 0 || stratum > 15 || version != 4 {
		t.Fatalf("header leap=%d version=%d mode=%d stratum=%d", leap, version, mode, stratum)
	}
	if string(b[24:32]) != string(transmit) {
		t.Fatal("the originate timestamp must echo the client's transmit timestamp")
	}
	if ref := ntpTime(b[16:24]); !ref.Equal(now.Truncate(time.Nanosecond)) && ref.Sub(now).Abs() > time.Microsecond {
		t.Fatalf("reference time %v", ref)
	}
	if tx := ntpTime(b[40:48]); tx.Sub(now).Abs() > time.Microsecond {
		t.Fatalf("transmit time %v, want %v", tx, now)
	}
	if rx := ntpTime(b[32:40]); rx.Sub(now.Add(-time.Millisecond)).Abs() > time.Microsecond {
		t.Fatalf("receive time %v", rx)
	}
	if b[2] != 6 || netip.AddrFrom4([4]byte(b[12:16])).String() != "10.0.2.2" {
		t.Fatalf("poll %d refid %v", b[2], b[12:16])
	}
}

func TestNTPReplyIgnoresNonRequests(t *testing.T) {
	now := time.Now()
	if ntpReply(make([]byte, 20), now, now, netip.Addr{}) != nil {
		t.Fatal("short packet answered")
	}
	server := ntpRequest(nil)
	server[0] = 4<<3 | ntpModeServer
	if ntpReply(server, now, now, netip.Addr{}) != nil {
		t.Fatal("a server packet must not be answered (no reflection loops)")
	}
}
