package network

import (
	"bytes"
	"compress/flate"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// captureHello records the ClientHello bytes a real crypto/tls client sends.
func captureHello(t *testing.T, cfg *tls.Config) []byte {
	t.Helper()
	c, s := net.Pipe()
	go func() {
		tls.Client(c, cfg).Handshake()
	}()
	var buf bytes.Buffer
	s.SetReadDeadline(time.Now().Add(2 * time.Second))
	tmp := make([]byte, 4096)
	for {
		n, err := s.Read(tmp)
		buf.Write(tmp[:n])
		if _, perr := parseClientHello(buf.Bytes()); perr != errNeedMoreData || err != nil {
			break
		}
	}
	s.Close()
	c.Close()
	return buf.Bytes()
}

func TestParseClientHelloFromRealClient(t *testing.T) {
	raw := captureHello(t, &tls.Config{ServerName: "api.example.com", NextProtos: []string{"h2", "http/1.1"}})
	h, err := parseClientHello(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h.ServerName != "api.example.com" || len(h.ALPN) != 2 || h.ALPN[0] != "h2" || len(h.Versions) == 0 {
		t.Fatalf("hello: %+v", h)
	}
	// Every strict prefix needs more data; nothing panics.
	for i := 0; i < len(raw); i++ {
		if _, err := parseClientHello(raw[:i]); err != errNeedMoreData {
			t.Fatalf("prefix %d: %v", i, err)
		}
	}
	noSNI := captureHello(t, &tls.Config{InsecureSkipVerify: true})
	if h, err := parseClientHello(noSNI); err != nil || h.ServerName != "" {
		t.Fatalf("no-SNI hello: %+v %v", h, err)
	}
}

func TestParseClientHelloFragmentedRecords(t *testing.T) {
	raw := captureHello(t, &tls.Config{ServerName: "frag.example"})
	body := raw[5:]
	// Re-split the handshake message into 3 records.
	var frag []byte
	for len(body) > 0 {
		n := min(len(body), 100)
		frag = append(frag, 0x16, 0x03, 0x01, byte(n>>8), byte(n))
		frag = append(frag, body[:n]...)
		body = body[n:]
	}
	h, err := parseClientHello(frag)
	if err != nil || h.ServerName != "frag.example" {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestParseClientHelloRejectsNonTLS(t *testing.T) {
	if _, err := parseClientHello([]byte("GET / HTTP/1.1\r\n")); err != errNotTLS {
		t.Fatal(err)
	}
	if _, err := parseClientHello([]byte{0x16, 0x03, 0x01, 0x00, 0x05, 2, 0, 0, 1, 0}); err != errBadHello {
		t.Fatal("non-ClientHello handshake should be rejected")
	}
}

func FuzzParseClientHello(f *testing.F) {
	f.Add([]byte{0x16, 0x03, 0x01, 0x00, 0x04, 1, 0, 0, 0})
	f.Add([]byte("GET /"))
	f.Fuzz(func(t *testing.T, b []byte) { parseClientHello(b) })
}

func TestLooksLikeHTTP1(t *testing.T) {
	for _, s := range []string{"GET / HTTP/1.1", "POST /x", "OPTIONS *", "DEL", "PATCH /"} {
		if !looksLikeHTTP1([]byte(s)) {
			t.Errorf("%q should look like HTTP", s)
		}
	}
	for _, s := range []string{"", "GE", "\x16\x03\x01", "SSH-2.0", "PRI * HTTP/2.0", "get / "} {
		if looksLikeHTTP1([]byte(s)) {
			t.Errorf("%q should not look like HTTP", s)
		}
	}
}

func dhcpMsg(msgType byte, extra ...byte) []byte {
	b := make([]byte, 240)
	b[0], b[1], b[2] = 1, 1, 6
	binary.BigEndian.PutUint32(b[4:8], 7)
	copy(b[28:34], []byte{1, 2, 3, 4, 5, 6})
	copy(b[236:240], dhcpMagic[:])
	b = append(b, optMsgType, 1, msgType)
	b = append(b, extra...)
	return append(b, optEnd)
}

func TestDHCPStateMachine(t *testing.T) {
	a := DefaultAddressing()
	parse := func(b []byte) *dhcpPacket {
		p, err := parseDHCP(b)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	offer := handleDHCP(parse(dhcpMsg(dhcpDiscover)), a, time.Hour)
	if offer == nil || offer[0] != 2 || netip.AddrFrom4([4]byte(offer[16:20])) != a.Guest {
		t.Fatal("bad OFFER")
	}
	// Reply options parse back (type, router, DNS, lease).
	o := offer
	o[0], o[1] = 1, 1 // make it parseable by our request parser
	rp := parse(o)
	if rp.msgType() != dhcpOffer || !bytes.Equal(rp.Options[optRouter], a.Gateway.AsSlice()) || !bytes.Equal(rp.Options[optDNS], a.DNS.AsSlice()) ||
		binary.BigEndian.Uint32(rp.Options[optLeaseTime]) != 3600 {
		t.Fatalf("offer options: %v", rp.Options)
	}
	ack := handleDHCP(parse(dhcpMsg(dhcpRequest, optRequestIP, 4, 10, 0, 2, 15)), a, time.Hour)
	if ack == nil || parseType(ack) != dhcpAck {
		t.Fatal("expected ACK")
	}
	nak := handleDHCP(parse(dhcpMsg(dhcpRequest, optRequestIP, 4, 10, 0, 2, 99)), a, time.Hour)
	if nak == nil || parseType(nak) != dhcpNak {
		t.Fatal("expected NAK for a foreign address")
	}
	if handleDHCP(parse(dhcpMsg(dhcpRequest, optServerID, 4, 1, 1, 1, 1)), a, time.Hour) != nil {
		t.Fatal("request for another server must be ignored")
	}
	if handleDHCP(parse(dhcpMsg(dhcpRelease)), a, time.Hour) != nil {
		t.Fatal("release needs no reply")
	}
	for _, bad := range [][]byte{nil, make([]byte, 239), append(dhcpMsg(dhcpDiscover)[:241], 53, 9)} {
		if _, err := parseDHCP(bad); err == nil {
			t.Error("malformed packet accepted")
		}
	}
}

func parseType(reply []byte) byte {
	r := append([]byte(nil), reply...)
	r[0] = 1
	p, _ := parseDHCP(r)
	return p.msgType()
}

func FuzzParseDHCP(f *testing.F) {
	f.Add(dhcpMsg(dhcpDiscover))
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, err := parseDHCP(b); err == nil {
			handleDHCP(p, DefaultAddressing(), time.Hour)
		}
	})
}

func wsFrame(op byte, fin, rsv1 bool, mask bool, payload []byte) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	if rsv1 {
		b0 |= 0x40
	}
	f := []byte{b0}
	m := byte(0)
	if mask {
		m = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		f = append(f, m|byte(n))
	case n < 65536:
		f = append(f, m|126, byte(n>>8), byte(n))
	default:
		f = append(f, m|127)
		f = binary.BigEndian.AppendUint64(f, uint64(n))
	}
	if mask {
		k := [4]byte{9, 8, 7, 6}
		f = append(f, k[:]...)
		for i, c := range payload {
			f = append(f, c^k[i%4])
		}
		return f
	}
	return append(f, payload...)
}

func TestWSParserFramesSplitAcrossReads(t *testing.T) {
	var got []model.WSFrame
	p := newWSParser(true, 1024, false, func(f model.WSFrame) { got = append(got, f) })
	stream := append(wsFrame(1, true, false, true, []byte("hello")), wsFrame(2, true, false, true, []byte{0, 1, 2})...)
	stream = append(stream, wsFrame(9, true, false, true, []byte("ping"))...)
	stream = append(stream, wsFrame(1, true, false, true, bytes.Repeat([]byte("x"), 300))...)
	for _, c := range stream { // one byte at a time
		p.feed([]byte{c})
	}
	if len(got) != 4 || string(got[0].Data) != "hello" || !got[0].Outgoing || got[1].Opcode != 2 || got[2].Opcode != 9 || got[3].Length != 300 {
		t.Fatalf("frames: %+v", got)
	}
}

func TestWSParserTruncatesLargeFrames(t *testing.T) {
	var got []model.WSFrame
	p := newWSParser(false, 10, false, func(f model.WSFrame) { got = append(got, f) })
	p.feed(wsFrame(1, true, false, false, bytes.Repeat([]byte("y"), 100)))
	if len(got) != 1 || len(got[0].Data) != 10 || !got[0].Truncated || got[0].Length != 100 {
		t.Fatalf("%+v", got)
	}
}

func TestWSParserPermessageDeflateWithContextTakeover(t *testing.T) {
	// Compress two messages with one shared compressor (context takeover).
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	var frames []byte
	for _, msg := range []string{"repeated payload repeated payload", "repeated payload again"} {
		buf.Reset()
		w.Write([]byte(msg))
		w.Flush()
		data := bytes.TrimSuffix(buf.Bytes(), []byte{0, 0, 0xff, 0xff})
		frames = append(frames, wsFrame(1, true, true, false, data)...)
	}
	var got []model.WSFrame
	p := newWSParser(false, 1024, true, func(f model.WSFrame) { got = append(got, f) })
	p.feed(frames)
	if len(got) != 2 || string(got[0].Data) != "repeated payload repeated payload" || string(got[1].Data) != "repeated payload again" {
		t.Fatalf("inflated: %+v", got)
	}
	if !wsDeflateNegotiated([]string{"permessage-deflate; client_max_window_bits"}) || wsDeflateNegotiated([]string{"x-webkit"}) {
		t.Fatal("extension detection")
	}
}

func TestWSParserStopsOnGarbageWithoutPanicking(t *testing.T) {
	n := 0
	p := newWSParser(true, 1024, false, func(model.WSFrame) { n++ })
	p.feed([]byte{0x83, 0x00}) // reserved opcode 3
	p.feed(wsFrame(1, true, false, false, []byte("later")))
	if n != 0 || !p.failed {
		t.Fatal("parser should stop after a protocol error")
	}
}

func FuzzWSParser(f *testing.F) {
	f.Add(wsFrame(1, true, false, true, []byte("hi")))
	f.Add([]byte{0x81, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		p := newWSParser(true, 64, true, func(model.WSFrame) {})
		p.feed(b)
		p.feed(b)
	})
}

func TestCAIssuesVerifiableLeaves(t *testing.T) {
	ca, err := NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Certificate())
	for _, host := range []string{"api.example.com", "10.1.2.3"} {
		leaf, err := ca.Leaf(host)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots}); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		again, _ := ca.Leaf(host)
		if again != leaf {
			t.Fatal("leaf not cached")
		}
	}
	if _, err := ca.Leaf(""); err == nil {
		t.Fatal("empty host accepted")
	}
	if !ca.Certificate().IsCA || len(ca.Fingerprint()) != 64 || !bytes.Contains(ca.CertPEM(), []byte("BEGIN CERTIFICATE")) {
		t.Fatal("CA properties")
	}
	other, _ := NewCA(time.Now())
	if other.Fingerprint() == ca.Fingerprint() {
		t.Fatal("each boot must get a fresh CA")
	}
}
