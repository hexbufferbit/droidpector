package network

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestIdentifyProtocol(t *testing.T) {
	tg := netip.MustParseAddrPort("149.154.167.51:443")
	other := netip.MustParseAddrPort("93.184.216.34:443")
	cases := []struct {
		dst   netip.AddrPort
		first []byte
		want  string
	}{
		{tg, nil, "MTProto"},
		{tg, []byte{0x16, 0x03, 0x01}, "MTProto"}, // Telegram DC, whatever the framing
		{other, []byte{0xef, 0x0a, 0x00, 0x00}, "MTProto"},
		{other, []byte{0xee, 0xee, 0xee, 0xee}, "MTProto"},
		{other, []byte{0xdd, 0xdd, 0xdd, 0xdd}, "MTProto"},
		{other, []byte("SSH-2.0-OpenSSH"), "SSH"},
		{other, []byte("hello"), ""},
		{other, nil, ""},
		{netip.MustParseAddrPort("[2001:b28:f23d::a]:443"), nil, "MTProto"},
	}
	for _, c := range cases {
		if got := identifyProtocol(c.dst, c.first); got != c.want {
			t.Errorf("identifyProtocol(%s, %q) = %q, want %q", c.dst, c.first, got, c.want)
		}
	}
}

func TestStreamTapCapsPrefixButCountsAll(t *testing.T) {
	tap := newStreamTap(8)
	tap.Write([]byte("hello "))
	tap.Write([]byte("world!"))
	if tap.count() != 12 || string(tap.prefix()) != "hello wo" {
		t.Fatalf("count=%d prefix=%q", tap.count(), tap.prefix())
	}
	zero := newStreamTap(0)
	zero.Write([]byte("x"))
	if zero.count() != 1 || len(zero.prefix()) != 0 {
		t.Fatal("zero-capacity tap must only count")
	}
}

func TestRelayLiveReportsProgressAndCounts(t *testing.T) {
	clientA, clientB := net.Pipe() // gateway side <-> "guest"
	upA, upB := net.Pipe()         // gateway side <-> "server"
	var mu sync.Mutex
	var progress [][2]int64
	upTap, downTap := newStreamTap(4), newStreamTap(4)
	done := make(chan struct{})
	var up, down int64
	go func() {
		defer close(done)
		up, down, _ = relayLive(clientA, upA, upTap, downTap, func(u, d int64) {
			mu.Lock()
			progress = append(progress, [2]int64{u, d})
			mu.Unlock()
		})
	}()
	// Server echoes with a prefix; the guest sends two messages 1.2 s apart so
	// the ticker fires in between.
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := upB.Read(buf)
			if err != nil {
				return
			}
			upB.Write(append([]byte("echo:"), buf[:n]...))
		}
	}()
	rd := make(chan []byte, 2)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := clientB.Read(buf)
			if err != nil {
				return
			}
			rd <- append([]byte(nil), buf[:n]...)
		}
	}()
	clientB.Write([]byte("ping1"))
	if got := <-rd; !bytes.Equal(got, []byte("echo:ping1")) {
		t.Fatalf("first echo %q", got)
	}
	time.Sleep(relayProgressInterval + 300*time.Millisecond)
	clientB.Write([]byte("ping2"))
	<-rd
	clientB.Close()
	upB.Close()
	<-done
	if up != 10 || down != 20 {
		t.Fatalf("counts up=%d down=%d", up, down)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(progress) == 0 || progress[0] != [2]int64{5, 10} {
		t.Fatalf("expected a progress report after the first exchange, got %v", progress)
	}
	if string(upTap.prefix()) != "ping" || string(downTap.prefix()) != "echo" {
		t.Fatalf("prefixes %q %q", upTap.prefix(), downTap.prefix())
	}
	_ = io.EOF
}
