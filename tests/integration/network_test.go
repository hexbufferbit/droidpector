// Package integration contains cross-package integration tests. The network
// suite runs everywhere (no VM needed): a simulated guest drives real traffic
// through the Sandbox Network Gateway to a deterministic local test server.
package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/content"
	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/network"
	"github.com/droidpector/apkinspector/src/network/guestsim"
	"github.com/droidpector/apkinspector/src/storage"
	"github.com/droidpector/apkinspector/src/testserver"
)

const (
	httpsHost = "test.apkinspector.internal"
	httpHost  = "http.test.internal"
	rawHost   = "raw.test.internal"
)

// recorder is an in-memory network.Sink.
type recorder struct {
	mu     sync.Mutex
	events map[string]*model.Event
	order  []string
	frames map[string][]model.WSFrame
	notify chan struct{}
}

func newRecorder() *recorder {
	return &recorder{events: map[string]*model.Event{}, frames: map[string][]model.WSFrame{}, notify: make(chan struct{}, 1)}
}

func (r *recorder) Emit(e *model.Event) {
	r.mu.Lock()
	if _, ok := r.events[e.ID]; !ok {
		r.order = append(r.order, e.ID)
	}
	r.events[e.ID] = e
	r.mu.Unlock()
	r.poke()
}

func (r *recorder) Frame(id string, f model.WSFrame) {
	r.mu.Lock()
	r.frames[id] = append(r.frames[id], f)
	r.mu.Unlock()
	r.poke()
}

func (r *recorder) poke() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// wait returns the first event satisfying pred, waiting up to 10s.
func (r *recorder) wait(t *testing.T, what string, pred func(*model.Event) bool) *model.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		r.mu.Lock()
		for _, id := range r.order {
			if e := r.events[id]; pred(e) {
				r.mu.Unlock()
				return e
			}
		}
		r.mu.Unlock()
		select {
		case <-r.notify:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for event: %s\nevents: %s", what, r.dump())
		}
	}
}

func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sb strings.Builder
	for _, id := range r.order {
		e := r.events[id]
		fmt.Fprintf(&sb, "\n  %s %s %s %s://%s%s %d %s err=%q", e.Kind, e.State, e.Method, e.Scheme, e.Host, e.Path, e.Status, e.Protocol, e.Error)
	}
	return sb.String()
}

type env struct {
	t      *testing.T
	gw     *network.Gateway
	guest  *guestsim.Guest
	rec    *recorder
	blobs  *storage.BlobStore
	srv    *testserver.Server
	ca     *network.CA
	roots  *x509.CertPool // guest system trust store incl. sandbox CA
	rawSrv net.Listener
}

func newEnv(t *testing.T, maxBody int64, extra ...network.HostMapping) *env {
	t.Helper()
	srv, err := testserver.Start("", "", []string{httpsHost})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	go func() { // line echo server speaking a non-HTTP protocol
		for {
			c, err := raw.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.WriteString(c, "RAW-READY\n")
				line, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "ECHO "+line)
			}()
		}
	}()

	blobs, err := storage.OpenBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("NET_DEBUG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	gw, err := network.New(network.Options{
		Policy: network.DefaultPolicy(),
		Mappings: append([]network.HostMapping{
			{Host: httpsHost, Target: srv.HTTPSAddr},
			{Host: httpHost, Target: srv.HTTPAddr},
			{Host: rawHost, Target: raw.Addr().String()},
		}, extra...),
		ExtraRoots:   []*x509.Certificate{srv.CA},
		MaxBodyBytes: maxBody,
		SniffTimeout: 300 * time.Millisecond,
	}, rec, blobs, log)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := network.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gw.SetCA(ca)

	vmSide, gwSide := net.Pipe()
	go gw.Stack().ServeQEMU(context.Background(), gwSide)
	a := gw.Stack().Addressing()
	guest, err := guestsim.New(vmSide, a.Guest, a.Gateway, a.DNS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		guest.Close()
		gw.Close()
		blobs.Close()
	})
	roots := x509.NewCertPool()
	roots.AddCert(ca.Certificate())
	return &env{t: t, gw: gw, guest: guest, rec: rec, blobs: blobs, srv: srv, ca: ca, roots: roots, rawSrv: raw}
}

func (e *env) body(ref *model.BodyRef) []byte {
	e.t.Helper()
	if ref == nil {
		return nil
	}
	b, err := e.blobs.Get(ref.Hash)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func completeHTTP(path string) func(*model.Event) bool {
	return func(e *model.Event) bool {
		return (e.Kind == model.KindHTTP) && e.Path == path && e.State != model.StatePending && e.Initiator == model.InitiatorGuest
	}
}

func TestDHCPAssignsGuestAddress(t *testing.T) {
	e := newEnv(t, 0)
	discover := make([]byte, 240)
	discover[0], discover[1], discover[2] = 1, 1, 6
	binary.BigEndian.PutUint32(discover[4:8], 0xdeadbeef)
	copy(discover[28:34], []byte{0x52, 0x54, 0, 0x12, 0x34, 0x56})
	copy(discover[236:240], []byte{99, 130, 83, 99})
	discover = append(discover, 53, 1, 1, 255)
	reply, err := e.guest.BroadcastUDP(68, 67, discover, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reply[0] != 2 || binary.BigEndian.Uint32(reply[4:8]) != 0xdeadbeef {
		t.Fatalf("not an OFFER for our transaction: % x", reply[:8])
	}
	if got := netip.AddrFrom4([4]byte(reply[16:20])); got != netip.MustParseAddr("10.0.2.15") {
		t.Fatalf("offered %s", got)
	}
}

func TestDNSQueriesAreAnsweredAndRecorded(t *testing.T) {
	e := newEnv(t, 0)
	ctx := context.Background()
	ips, err := e.guest.Resolver().LookupNetIP(ctx, "ip4", httpsHost)
	if err != nil || len(ips) != 1 || !netip.MustParsePrefix("198.18.0.0/15").Contains(ips[0]) {
		t.Fatalf("lookup = %v %v", ips, err)
	}
	ev := e.rec.wait(t, "DNS A event", func(ev *model.Event) bool {
		return ev.Kind == model.KindDNS && ev.Host == httpsHost && ev.Method == "A"
	})
	if ev.DNS == nil || ev.DNS.RCode != "NOERROR" || len(ev.DNS.Answers) != 1 || ev.DNS.Answers[0].Data != ips[0].String() || ev.Category != model.CatDNS {
		t.Fatalf("DNS event: %+v %+v", ev, ev.DNS)
	}
	if _, err := e.guest.Resolver().LookupNetIP(ctx, "ip4", "does-not-exist.invalid"); err == nil {
		t.Fatal("expected NXDOMAIN")
	}
	nx := e.rec.wait(t, "NXDOMAIN event", func(ev *model.Event) bool { return ev.Kind == model.KindDNS && ev.Host == "does-not-exist.invalid" })
	if nx.DNS.RCode != "NXDOMAIN" {
		t.Fatalf("rcode %s", nx.DNS.RCode)
	}
	// AAAA is answered empty (IPv4-only sandbox), not an error.
	if ips, _ := e.guest.Resolver().LookupNetIP(ctx, "ip6", httpsHost); len(ips) != 0 {
		t.Fatalf("AAAA answers %v", ips)
	}
}

func TestPlainHTTPGetIsCaptured(t *testing.T) {
	e := newEnv(t, 0)
	resp, err := e.guest.HTTPClient(e.roots, false).Get("http://" + httpHost + "/test/get?x=1&y=two")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	ev := e.rec.wait(t, "GET /test/get", completeHTTP("/test/get"))
	if ev.Method != "GET" || ev.Scheme != "http" || ev.Host != httpHost || ev.Port != 80 || ev.Query != "x=1&y=two" ||
		ev.Status != 200 || ev.Protocol != "HTTP/1.1" || ev.Category != model.CatAPI || ev.State != model.StateComplete {
		t.Fatalf("event: %+v", ev)
	}
	if ev.ResponseHeaders.Get("X-Test") != "get" || ev.RequestHeaders.Get("Host") != httpHost {
		t.Fatalf("headers: %v / %v", ev.RequestHeaders, ev.ResponseHeaders)
	}
	if !bytes.Equal(e.body(ev.ResponseBody), got) || ev.ResponseSize != int64(len(got)) {
		t.Fatal("stored body differs from what the app received")
	}
	if ev.Timing == nil || ev.Timing.Wait < 0 || ev.Timing.Connect < 0 || ev.Conn == nil || ev.Conn.ClientAddr == "" {
		t.Fatalf("timing/conn: %+v %+v", ev.Timing, ev.Conn)
	}
	// The host side of the upstream socket and the interface Windows routed
	// it through (a VPN adapter when a VPN covers the destination).
	if !strings.HasPrefix(ev.Conn.LocalAddr, "127.0.0.1:") || ev.Conn.Interface == "" {
		t.Fatalf("egress not recorded: %+v", ev.Conn)
	}
	if ev.DurationMs <= 0 {
		t.Fatal("duration not recorded")
	}
}

func TestHTTPSInterceptionHTTP1AndHTTP2(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%v", h2), func(t *testing.T) {
			e := newEnv(t, 0)
			payload := `{"username":"amir","password":"secret"}`
			req, _ := http.NewRequest("POST", "https://"+httpsHost+"/test/post?lang=fa", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer token-123")
			resp, err := e.guest.HTTPClient(e.roots, h2).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var echoed map[string]any
			json.NewDecoder(resp.Body).Decode(&echoed)
			resp.Body.Close()
			if resp.StatusCode != 201 || echoed["body"] != payload {
				t.Fatalf("app saw status %d body %v", resp.StatusCode, echoed["body"])
			}
			wantProto := "HTTP/1.1"
			if h2 {
				wantProto = "HTTP/2"
			}
			if resp.TLS.PeerCertificates[0].Issuer.CommonName != e.ca.Certificate().Subject.CommonName {
				t.Fatal("app did not see the sandbox CA certificate")
			}
			ev := e.rec.wait(t, "POST /test/post", completeHTTP("/test/post"))
			if ev.Scheme != "https" || ev.Protocol != wantProto || ev.Status != 201 || ev.Method != "POST" || ev.Query != "lang=fa" {
				t.Fatalf("event: %+v", ev)
			}
			if ev.TLS == nil || !ev.TLS.Intercepted || ev.TLS.SNI != httpsHost || len(ev.TLS.ServerCerts) == 0 ||
				ev.TLS.ServerCerts[0].Issuer != "CN=droidpector Test Server CA" {
				t.Fatalf("tls: %+v", ev.TLS)
			}
			if string(e.body(ev.RequestBody)) != payload || ev.RequestSize != int64(len(payload)) {
				t.Fatalf("request body %q", e.body(ev.RequestBody))
			}
			if ev.RequestHeaders.Get("Authorization") != "Bearer token-123" {
				t.Fatal("request headers not captured")
			}
			if ev.Timing.TLS < 0 {
				t.Fatalf("first request should report TLS time: %+v", ev.Timing)
			}
		})
	}
}

func TestHTTPMatrix(t *testing.T) {
	e := newEnv(t, 1<<20)
	c := e.guest.HTTPClient(e.roots, false)
	base := "https://" + httpsHost
	type tc struct {
		method, path string
		body         string
		status       int
		mime         string
		cat          model.Category
	}
	cases := []tc{
		{"GET", "/test/json", "", 200, "application/json", model.CatAPI},
		{"GET", "/test/text", "", 200, "text/plain", model.CatAPI},
		{"GET", "/test/html", "", 200, "text/html", model.CatDocument},
		{"GET", "/test/xml", "", 200, "application/xml", model.CatAPI},
		{"GET", "/test/image", "", 200, "image/png", model.CatImage},
		{"GET", "/test/binary", "", 200, "application/octet-stream", model.CatOther},
		{"PUT", "/test/echo", `{"a":1}`, 200, "application/json", model.CatAPI},
		{"PATCH", "/test/echo", `{"b":2}`, 200, "application/json", model.CatAPI},
		{"DELETE", "/test/echo", "", 200, "application/json", model.CatAPI},
		{"GET", "/test/status/201", "", 201, "", model.CatAPI},
		{"GET", "/test/status/204", "", 204, "", model.CatAPI},
		{"GET", "/test/status/301", "", 301, "", model.CatAPI},
		{"GET", "/test/status/400", "", 400, "", model.CatAPI},
		{"GET", "/test/status/401", "", 401, "", model.CatAPI},
		{"GET", "/test/status/403", "", 403, "", model.CatAPI},
		{"GET", "/test/status/404", "", 404, "", model.CatAPI},
		{"GET", "/test/error", "", 500, "application/json", model.CatAPI},
	}
	for _, k := range cases {
		req, _ := http.NewRequest(k.method, base+k.path, strings.NewReader(k.body))
		if k.body == "" {
			req.Body = http.NoBody
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", k.method, k.path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		ev := e.rec.wait(t, k.method+" "+k.path, func(ev *model.Event) bool {
			return completeHTTP(k.path)(ev) && ev.Method == k.method && ev.Status == k.status
		})
		if k.mime != "" && ev.MIME != k.mime || ev.Category != k.cat {
			t.Errorf("%s %s: mime=%q cat=%s", k.method, k.path, ev.MIME, ev.Category)
		}
		if k.body != "" && string(e.body(ev.RequestBody)) != k.body {
			t.Errorf("%s %s: request body not captured", k.method, k.path)
		}
	}

	// Binary body integrity.
	resp, _ := c.Get(base + "/test/binary")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	bin := e.rec.wait(t, "binary", completeHTTP("/test/binary"))
	if !bytes.Equal(e.body(bin.ResponseBody), testserver.BinaryBody()) {
		t.Error("binary body corrupted")
	}

	// Large body: the app receives everything, capture is truncated at the limit.
	resp, err := c.Get(base + "/test/large")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(all, testserver.LargeBody()) {
		t.Fatal("app received a corrupted large body")
	}
	large := e.rec.wait(t, "large", completeHTTP("/test/large"))
	if large.ResponseSize != int64(testserver.LargeSize) || !large.ResponseBody.Truncated || large.ResponseBody.Stored != 1<<20 {
		t.Fatalf("large capture: size=%d body=%+v", large.ResponseSize, large.ResponseBody)
	}

	// Content-Encoding is passed through untouched and decodable afterwards.
	req, _ := http.NewRequest("GET", base+"/test/gzip", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, _ = c.Do(req)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	gz := e.rec.wait(t, "gzip", completeHTTP("/test/gzip"))
	dec, err := content.Decode(gz.ResponseBody.Encoding, e.body(gz.ResponseBody), 1<<20)
	if err != nil || !bytes.Equal(dec, testserver.JSONBody) {
		t.Fatalf("gzip body: %v %q", err, dec)
	}
}

func TestSlowResetAndTimeout(t *testing.T) {
	e := newEnv(t, 0)
	c := e.guest.HTTPClient(e.roots, false)
	base := "http://" + httpHost

	resp, err := c.Get(base + "/test/slow?ms=400")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	slow := e.rec.wait(t, "slow", completeHTTP("/test/slow"))
	if slow.DurationMs < 400 || slow.Timing.Wait < 350 {
		t.Fatalf("slow timing: %v %+v", slow.DurationMs, slow.Timing)
	}

	if r, err := c.Get(base + "/test/reset"); err == nil {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("app should see a failure for a reset upstream, got %d %q %s", r.StatusCode, b, e.rec.dump())
	}
	reset := e.rec.wait(t, "reset", completeHTTP("/test/reset"))
	if reset.State != model.StateError || reset.Error == "" {
		t.Fatalf("reset event: %+v", reset)
	}

	short := e.guest.HTTPClient(e.roots, false)
	short.Timeout = 500 * time.Millisecond
	if _, err := short.Get(base + "/test/timeout"); err == nil {
		t.Fatal("expected client timeout")
	}
	to := e.rec.wait(t, "timeout", completeHTTP("/test/timeout"))
	if to.State != model.StateError {
		t.Fatalf("timeout event: %+v", to)
	}
}

func TestWebSocketFramesAreCaptured(t *testing.T) {
	e := newEnv(t, 0)
	ctx := context.Background()
	ips, err := e.guest.Resolver().LookupNetIP(ctx, "ip4", httpsHost)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := e.guest.DialTCP(ctx, netip.AddrPortFrom(ips[0], 443))
	if err != nil {
		t.Fatal(err)
	}
	conn := tls.Client(raw, &tls.Config{ServerName: httpsHost, RootCAs: e.roots, NextProtos: []string{"http/1.1"}})
	defer conn.Close()
	key := make([]byte, 16)
	rand.Read(key)
	fmt.Fprintf(conn, "GET /test/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		httpsHost, base64.StdEncoding.EncodeToString(key))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade failed: %v %v", resp, err)
	}
	readText := func() string {
		var h [2]byte
		io.ReadFull(br, h[:])
		b := make([]byte, h[1]&0x7f)
		io.ReadFull(br, b)
		return string(b)
	}
	if got := readText(); got != "welcome" {
		t.Fatalf("got %q", got)
	}
	send := func(op byte, msg string) {
		mask := [4]byte{1, 2, 3, 4}
		f := []byte{0x80 | op, 0x80 | byte(len(msg))}
		f = append(f, mask[:]...)
		for i := 0; i < len(msg); i++ {
			f = append(f, msg[i]^mask[i%4])
		}
		conn.Write(f)
	}
	send(1, "hello")
	if got := readText(); got != "echo: hello" {
		t.Fatalf("got %q", got)
	}
	send(1, "close")
	readText()

	ws := e.rec.wait(t, "websocket complete", func(ev *model.Event) bool { return ev.Kind == model.KindWebSocket && ev.State == model.StateComplete })
	if ws.Scheme != "wss" || ws.Path != "/test/ws" || ws.Status != 101 || ws.Category != model.CatWebSocket {
		t.Fatalf("ws event: %+v", ws)
	}
	e.rec.mu.Lock()
	frames := e.rec.frames[ws.ID]
	e.rec.mu.Unlock()
	var texts []string
	for _, f := range frames {
		dir := "in"
		if f.Outgoing {
			dir = "out"
		}
		texts = append(texts, dir+":"+string(f.Data))
	}
	joined := strings.Join(texts, "|")
	for _, want := range []string{"in:welcome", "out:hello", "in:echo: hello", "out:close"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("frame %q missing in %s", want, joined)
		}
	}
}

func TestCertificatePinningFallsBackToPassthrough(t *testing.T) {
	e := newEnv(t, 0)
	// A "pinned" app trusts only the real server's CA, not the sandbox CA.
	pinned := x509.NewCertPool()
	pinned.AddCert(e.srv.CA)
	c := e.guest.HTTPClient(pinned, false)
	if _, err := c.Get("https://" + httpsHost + "/test/get"); err == nil {
		t.Fatal("first connection must fail: the app rejects the interception certificate")
	}
	ev := e.rec.wait(t, "pinning detected", func(ev *model.Event) bool { return ev.Kind == model.KindTLS && ev.Encrypted })
	if ev.TLS == nil || !strings.Contains(ev.TLS.PassthroughReason, "Payload inspection unavailable") || ev.Host != httpsHost {
		t.Fatalf("pinning event: %+v %+v", ev, ev.TLS)
	}
	// Retry (apps retry): now passed through untouched and succeeds.
	c = e.guest.HTTPClient(pinned, false)
	resp, err := c.Get("https://" + httpsHost + "/test/get")
	if err != nil {
		t.Fatalf("passthrough failed: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	c.CloseIdleConnections()
	pt := e.rec.wait(t, "passthrough", func(ev *model.Event) bool {
		return ev.Kind == model.KindTLS && ev.State == model.StateComplete && ev.ResponseSize > 0 && ev.ID != "" && ev.Error == ""
	})
	if !pt.Encrypted || pt.TLS.Intercepted || pt.Method != "" {
		t.Fatalf("passthrough event: %+v", pt)
	}
}

// Servers behind a corporate VPN often use an internal CA that Windows does
// not trust (the app ships or trusts it itself), or are reached by IP without
// SNI. The sandbox must not break them: it passes them through uninspected.
func TestUntrustedUpstreamCertificateIsPassedThrough(t *testing.T) {
	internal := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "internal-ok")
	}))
	t.Cleanup(internal.Close)
	const internalHost = "intranet.corp.test"
	e := newEnv(t, 0, network.HostMapping{Host: internalHost, Target: internal.Listener.Addr().String()})
	appRoots := x509.NewCertPool() // the app trusts the organization's CA itself
	appRoots.AddCert(internal.Certificate())
	appRoots.AddCert(e.ca.Certificate())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ips, err := e.guest.Resolver().LookupNetIP(ctx, "ip4", internalHost)
	if err != nil {
		t.Fatal(err)
	}
	dst := netip.AddrPortFrom(ips[0], 443)
	get := func(serverName string, skipVerify bool) string {
		t.Helper()
		raw, err := e.guest.DialTCP(ctx, dst)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		c := tls.Client(raw, &tls.Config{ServerName: serverName, RootCAs: appRoots, InsecureSkipVerify: skipVerify, NextProtos: []string{"http/1.1"}})
		c.SetDeadline(time.Now().Add(15 * time.Second))
		if err := c.HandshakeContext(ctx); err != nil {
			t.Fatalf("app handshake (%q): %v", serverName, err)
		}
		if skipVerify == false && c.ConnectionState().PeerCertificates[0].Equal(internal.Certificate()) == false {
			t.Fatal("the app must see the real server certificate, not an interception certificate")
		}
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+internalHost+"\r\nConnection: close\r\n\r\n")
		b, _ := io.ReadAll(c)
		return string(b)
	}

	// SNI "example.com" matches the test certificate, which Windows does not trust.
	if body := get("example.com", false); !strings.HasSuffix(body, "internal-ok") {
		t.Fatalf("response through passthrough: %q", body)
	}
	ev := e.rec.wait(t, "untrusted passthrough", func(ev *model.Event) bool {
		return ev.Kind == model.KindTLS && ev.Host == "example.com" && ev.State == model.StateComplete
	})
	if ev.Error != "" || ev.TLS == nil || ev.TLS.Intercepted || !strings.Contains(ev.TLS.PassthroughReason, "not trusted by Windows") ||
		!strings.Contains(ev.TLS.PassthroughReason, network.TrustedCAFolder) {
		t.Fatalf("event: %+v %+v", ev, ev.TLS)
	}
	// The decision is remembered: the next connection skips the attempt.
	if body := get("example.com", false); !strings.HasSuffix(body, "internal-ok") {
		t.Fatalf("second connection: %q", body)
	}

	// No SNI at all (the app connects to an IP address).
	if body := get("", true); !strings.HasSuffix(body, "internal-ok") {
		t.Fatalf("no-SNI connection: %q", body)
	}
	e.rec.wait(t, "no-SNI passthrough", func(ev *model.Event) bool {
		return ev.Kind == model.KindTLS && ev.State == model.StateComplete && ev.TLS != nil && ev.TLS.SNI != "example.com" &&
			!ev.TLS.Intercepted && ev.Error == "" && ev.ResponseSize > 0
	})
}

func TestSandboxPolicyProtectsHost(t *testing.T) {
	e := newEnv(t, 0)
	_, port, _ := net.SplitHostPort(e.srv.HTTPAddr)
	var p uint16
	fmt.Sscan(port, &p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Unlike QEMU slirp, the gateway address is NOT an alias of host loopback.
	a := e.gw.Stack().Addressing()
	if c, err := e.guest.DialTCP(ctx, netip.AddrPortFrom(a.Gateway, p)); err == nil {
		c.Close()
		t.Fatal("guest reached a host service through the gateway address")
	}
	// Cloud metadata / link-local destinations are refused and recorded.
	if c, err := e.guest.DialTCP(ctx, netip.MustParseAddrPort("169.254.169.254:80")); err == nil {
		c.Close()
		t.Fatal("guest reached the metadata address")
	}
	ev := e.rec.wait(t, "blocked", func(ev *model.Event) bool { return ev.Kind == model.KindTCP && ev.State == model.StateError })
	if !strings.Contains(ev.Error, "blocked by sandbox policy") {
		t.Fatalf("blocked event: %+v", ev)
	}
	if e.gw.Stats().Blocked != 1 {
		t.Fatal("blocked counter")
	}
}

func TestRawTCPIsRelayedAndRecorded(t *testing.T) {
	e := newEnv(t, 0)
	ctx := context.Background()
	ips, _ := e.guest.Resolver().LookupNetIP(ctx, "ip4", rawHost)
	c, err := e.guest.DialTCP(ctx, netip.AddrPortFrom(ips[0], 7000))
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	if l, _ := br.ReadString('\n'); l != "RAW-READY\n" {
		t.Fatalf("server-first protocol broken: %q", l)
	}
	io.WriteString(c, "ping\n")
	if l, _ := br.ReadString('\n'); l != "ECHO ping\n" {
		t.Fatalf("got %q", l)
	}
	c.Close()
	ev := e.rec.wait(t, "raw tcp", func(ev *model.Event) bool { return ev.Kind == model.KindTCP && ev.State == model.StateComplete })
	if ev.Host != rawHost || ev.RequestSize != 5 || ev.ResponseSize != int64(len("RAW-READY\nECHO ping\n")) {
		t.Fatalf("raw event: %+v", ev)
	}
	// The stream prefix is kept so opaque protocols can still be inspected.
	if string(e.body(ev.RequestBody)) != "ping\n" || string(e.body(ev.ResponseBody)) != "RAW-READY\nECHO ping\n" {
		t.Fatalf("stream capture: %q / %q", e.body(ev.RequestBody), e.body(ev.ResponseBody))
	}
	if ev.Protocol != "TCP" || ev.URL() != "tcp://"+rawHost+":7000" {
		t.Fatalf("protocol/url: %s %s", ev.Protocol, ev.URL())
	}
}

func TestReplayCreatesNewEvent(t *testing.T) {
	e := newEnv(t, 0)
	payload := `{"replay":true}`
	req, _ := http.NewRequest("POST", "https://"+httpsHost+"/test/post", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.guest.HTTPClient(e.roots, false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	orig := e.rec.wait(t, "original", completeHTTP("/test/post"))
	origCopy := *orig

	ev, err := e.gw.Replay(context.Background(), network.ReplayRequest{Original: orig, Body: e.body(orig.RequestBody)})
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID == orig.ID || ev.Initiator != model.InitiatorReplay || ev.ReplayOf != orig.ID || ev.Status != 201 || ev.State != model.StateComplete {
		t.Fatalf("replay event: %+v", ev)
	}
	var echoed map[string]any
	json.Unmarshal(e.body(ev.ResponseBody), &echoed)
	if echoed["body"] != payload || !strings.Contains(fmt.Sprint(echoed["headers"]), "application/json") {
		t.Fatalf("server saw %v", echoed)
	}
	e.rec.mu.Lock()
	still := e.rec.events[orig.ID]
	e.rec.mu.Unlock()
	if still.Status != origCopy.Status || still.DurationMs != origCopy.DurationMs {
		t.Fatal("replay modified the original event")
	}
	// Replays obey the sandbox policy too.
	bad := origCopy
	bad.Host, bad.Port, bad.Scheme = "127.0.0.1", 1, "http"
	res, _ := e.gw.Replay(context.Background(), network.ReplayRequest{Original: &bad})
	if res.State != model.StateError || !strings.Contains(res.Error, "policy") {
		t.Fatalf("policy bypass via replay: %+v", res)
	}
	if _, err := e.gw.Replay(context.Background(), network.ReplayRequest{Original: &model.Event{Kind: model.KindDNS}}); err == nil {
		t.Fatal("DNS replay must be rejected")
	}
}

// TestHostTrafficIsNotCaptured proves the isolation property: traffic that
// does not come from the guest NIC never produces events.
func TestHostTrafficIsNotCaptured(t *testing.T) {
	e := newEnv(t, 0)
	resp, err := http.Get("http://" + e.srv.HTTPAddr + "/test/get")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	// Then generate one guest request as a positive control.
	r2, err := e.guest.HTTPClient(e.roots, false).Get("http://" + httpHost + "/test/text")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	e.rec.wait(t, "guest control request", completeHTTP("/test/text"))
	e.rec.mu.Lock()
	defer e.rec.mu.Unlock()
	for _, ev := range e.rec.events {
		if ev.Path == "/test/get" {
			t.Fatalf("host traffic leaked into capture: %+v", ev)
		}
	}
}

func TestManyConcurrentRequests(t *testing.T) {
	e := newEnv(t, 0)
	c := e.guest.HTTPClient(e.roots, true)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := c.Get(fmt.Sprintf("https://%s/test/get?i=%d", httpsHost, i))
			if err != nil {
				errs <- err
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		e.rec.mu.Lock()
		for _, ev := range e.rec.events {
			if ev.Path == "/test/get" && ev.State == model.StateComplete {
				n++
			}
		}
		e.rec.mu.Unlock()
		if n == 200 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("not all 200 requests were captured: %s", e.rec.dump()[:500])
}
