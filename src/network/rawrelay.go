package network

import (
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// streamTap counts the bytes of one direction and keeps the first max bytes
// so non-HTTP protocols still leave an inspectable trace (hex view).
type streamTap struct {
	mu    sync.Mutex
	buf   []byte
	max   int
	total int64
}

func newStreamTap(max int) *streamTap { return &streamTap{max: max} }

func (t *streamTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.total += int64(len(p))
	if room := t.max - len(t.buf); room > 0 {
		t.buf = append(t.buf, p[:min(len(p), room)]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *streamTap) count() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}

func (t *streamTap) prefix() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// relayProgressInterval bounds how often a long-lived connection updates its
// event while it is still open (sizes, duration).
const relayProgressInterval = time.Second

// relayLive relays both directions like relay, calling progress (from a
// separate goroutine) at most once per relayProgressInterval while the byte
// counts change. It returns the final counts.
func relayLive(client io.ReadWriter, upstream net.Conn, upTap, downTap *streamTap, progress func(up, down int64)) (int64, int64, error) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	if progress != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(relayProgressInterval)
			defer t.Stop()
			var lastUp, lastDown int64
			for {
				select {
				case <-done:
					return
				case <-t.C:
					up, down := upTap.count(), downTap.count()
					if up != lastUp || down != lastDown {
						lastUp, lastDown = up, down
						progress(up, down)
					}
				}
			}
		}()
	}
	var upErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, upErr = io.Copy(io.MultiWriter(upstream, upTap), client)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	_, err := io.Copy(io.MultiWriter(client, downTap), upstream)
	if cw, ok := client.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else if c, ok := client.(io.Closer); ok {
		c.Close()
	}
	close(done)
	wg.Wait()
	if err == nil {
		err = upErr
	}
	if isClosedErr(err) {
		err = nil
	}
	return upTap.count(), downTap.count(), err
}

// storeTap turns a captured stream prefix into a body reference.
func (g *Gateway) storeTap(t *streamTap) *model.BodyRef {
	if g.bodies == nil {
		return nil
	}
	data := t.prefix()
	total := t.count()
	if len(data) == 0 {
		return nil
	}
	hash, err := g.bodies.Put(data)
	if err != nil {
		g.log.Warn("storing stream capture failed", "err", err)
		return nil
	}
	return &model.BodyRef{Hash: hash, Size: total, Stored: int64(len(data)), Truncated: total > int64(len(data)), MIME: "application/octet-stream"}
}

// ---- protocol identification -----------------------------------------------------

// telegramNets are Telegram's published data-centre ranges. MTProto connections
// to them are labeled so users understand why no HTTP appears for Telegram.
var telegramNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"149.154.160.0/20", "91.108.4.0/22", "91.108.8.0/22", "91.108.12.0/22", "91.108.16.0/22",
		"91.108.20.0/22", "91.108.56.0/22", "91.105.192.0/23", "185.76.151.0/24", "95.161.64.0/20",
		"2001:b28:f23d::/48", "2001:b28:f23f::/48", "2001:67c:4e8::/48", "2001:b28:f23c::/48", "2a0a:f280::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// identifyProtocol names a non-HTTP, non-TLS stream from its destination and
// first client bytes. It returns "" when nothing specific is recognized.
func identifyProtocol(dst netip.AddrPort, first []byte) string {
	if len(first) >= 4 && string(first[:4]) == "SSH-" {
		return "SSH"
	}
	if len(first) >= 4 {
		switch {
		case first[0] == 0xef, // MTProto abridged
			first[0] == 0xee && first[1] == 0xee && first[2] == 0xee && first[3] == 0xee, // intermediate
			first[0] == 0xdd && first[1] == 0xdd && first[2] == 0xdd && first[3] == 0xdd: // padded intermediate
			return "MTProto"
		}
	}
	ip := dst.Addr().Unmap()
	for _, p := range telegramNets {
		if p.Contains(ip) {
			return "MTProto"
		}
	}
	return ""
}
