package network

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/droidpector/apkinspector/src/model"
)

// Sink receives captured events. Implementations must be fast and must not
// block (the recorder enqueues to the async storage writer).
type Sink interface {
	// Emit publishes a new or updated event snapshot. The gateway never
	// mutates an event after emitting it.
	Emit(e *model.Event)
	// Frame publishes one WebSocket frame of an event.
	Frame(eventID string, f model.WSFrame)
}

// BodyStore persists captured bodies and returns their content hash.
type BodyStore interface {
	Put(data []byte) (string, error)
}

// Options configures the gateway.
type Options struct {
	Addressing    Addressing
	Policy        Policy
	Mappings      []HostMapping
	ExtraRoots    []*x509.Certificate // additional upstream trust anchors (tests, corporate CAs)
	Resolver      Resolver
	MaxBodyBytes  int64         // per-body capture limit (default 10 MiB); traffic itself is never limited
	MaxFrameBytes int           // per WebSocket frame capture limit (default 64 KiB)
	SniffTimeout  time.Duration // wait for client-first protocols (default 2s)
	// OnNewFlow is called (must not block) when the guest opens a TCP
	// connection; used to refresh app attribution before the first event.
	OnNewFlow func(client netip.AddrPort)
}

func (o *Options) defaults() {
	if !o.Addressing.Guest.IsValid() {
		o.Addressing = DefaultAddressing()
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 10 << 20
	}
	if o.MaxFrameBytes <= 0 {
		o.MaxFrameBytes = 64 << 10
	}
	if o.SniffTimeout <= 0 {
		o.SniffTimeout = 2 * time.Second
	}
}

// Stats are gateway counters for diagnostics.
type Stats struct {
	ActiveFlows   int64 `json:"activeFlows"`
	TotalFlows    int64 `json:"totalFlows"`
	Intercepted   int64 `json:"intercepted"`
	Passthrough   int64 `json:"passthrough"`
	Blocked       int64 `json:"blocked"`
	HandlerPanics int64 `json:"handlerPanics"`
}

// Gateway is the Sandbox Network Gateway.
type Gateway struct {
	opts   Options
	stack  *Stack
	up     *Upstream
	dns    *dnsAnswerer
	sink   Sink
	bodies BodyStore
	log    *slog.Logger

	ca          atomic.Pointer[CA]
	passthrough sync.Map // SNI → reason: hosts that rejected interception (pinning)

	ctx    context.Context
	cancel context.CancelFunc

	active, total, intercepted, passed, blocked, panics atomic.Int64
	connSeq                                             atomic.Int64
}

// New creates the gateway and its network stack.
func New(opts Options, sink Sink, bodies BodyStore, log *slog.Logger) (*Gateway, error) {
	opts.defaults()
	up, err := NewUpstream(opts.Policy, opts.Mappings, opts.ExtraRoots, opts.Resolver)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{opts: opts, up: up, sink: sink, bodies: bodies, log: log, ctx: ctx, cancel: cancel}
	g.dns = &dnsAnswerer{up: up, policy: opts.Policy, cache: newDNSCache()}
	st, err := NewStack(opts.Addressing, g.onTCP, g.onUDP, log)
	if err != nil {
		cancel()
		return nil, err
	}
	g.stack = st
	go func() {
		if err := st.ServeDHCP(ctx, log); err != nil {
			log.Error("DHCP server stopped", "err", err)
		}
	}()
	return g, nil
}

// Stack exposes the network stack (VM link attachment, ADB dialing).
func (g *Gateway) Stack() *Stack { return g.stack }

// Upstream exposes the upstream dialer (used by replay).
func (g *Gateway) Upstream() *Upstream { return g.up }

// SetCA enables HTTPS interception with ca, or disables it when nil. The
// passthrough memory is reset because a new CA means a fresh guest trust store.
func (g *Gateway) SetCA(ca *CA) {
	g.ca.Store(ca)
	g.passthrough.Clear()
}

// CA returns the active interception CA (nil when disabled).
func (g *Gateway) CA() *CA { return g.ca.Load() }

// Close stops all flows and the stack.
func (g *Gateway) Close() {
	g.cancel()
	g.stack.Close()
}

// Stats returns counters.
func (g *Gateway) Stats() Stats {
	return Stats{ActiveFlows: g.active.Load(), TotalFlows: g.total.Load(), Intercepted: g.intercepted.Load(),
		Passthrough: g.passed.Load(), Blocked: g.blocked.Load(), HandlerPanics: g.panics.Load()}
}

func (g *Gateway) emit(e *model.Event) {
	cp := *e
	g.sink.Emit(&cp)
}

// guard isolates a flow handler: a panic caused by hostile traffic is logged
// and counted but never takes the engine (or the UI) down.
func (g *Gateway) guard(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			g.panics.Add(1)
			g.log.Error("network handler panic recovered", "handler", what, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	fn()
}

// ---- TCP --------------------------------------------------------------------

func (g *Gateway) onTCP(r *tcp.ForwarderRequest) {
	g.guard("tcp", func() {
		id := r.ID()
		dst := netip.AddrPortFrom(FromTCPIP(id.LocalAddress), id.LocalPort)
		src := netip.AddrPortFrom(FromTCPIP(id.RemoteAddress), id.RemotePort)
		a := g.opts.Addressing

		if dst.Addr() == a.Gateway || dst.Addr() == a.DNS {
			if dst.Port() == 53 {
				if c := g.accept(r); c != nil {
					g.serveDNSTCP(g.ctx, c, src, dst)
				}
				return
			}
			// e.g. DNS-over-TLS (853): refuse so Android falls back to plain DNS.
			r.Complete(true)
			return
		}

		g.total.Add(1)
		if g.opts.OnNewFlow != nil {
			g.opts.OnNewFlow(src)
		}
		connID := fmt.Sprintf("c%d", g.connSeq.Add(1))
		started := time.Now()
		dialCtx, cancel := context.WithTimeout(g.ctx, 15*time.Second)
		upConn, err := g.up.Dial(dialCtx, dst)
		cancel()
		connectTime := time.Since(started)
		if err != nil {
			r.Complete(true) // RST: the guest sees a refused connection
			g.connFailed(connID, src, dst, started, err)
			return
		}
		c := g.accept(r)
		if c == nil {
			upConn.Close()
			return
		}
		g.active.Add(1)
		defer g.active.Add(-1)
		f := &flow{g: g, id: connID, client: c, upstream: upConn, src: src, dst: dst, started: started, connectTime: connectTime}
		defer f.close()
		f.run()
	})
}

func (g *Gateway) accept(r *tcp.ForwarderRequest) net.Conn {
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true)
		return nil
	}
	r.Complete(false)
	ep.SocketOptions().SetKeepAlive(true)
	return gonet.NewTCPConn(&wq, ep)
}

func (g *Gateway) hostFor(ip netip.Addr) string {
	if e, ok := g.dns.cache.lookup(ip); ok {
		return e.host
	}
	return ip.String()
}

func (g *Gateway) connFailed(connID string, src, dst netip.AddrPort, started time.Time, err error) {
	kind := model.KindTCP
	var be *BlockedError
	if errors.As(err, &be) {
		g.blocked.Add(1)
	}
	e := &model.Event{
		ID: model.NewID(), Kind: kind, Category: model.CatOther, State: model.StateError, Initiator: model.InitiatorGuest,
		StartedAt: started, DurationMs: ms(time.Since(started)), Protocol: "TCP", Host: g.hostFor(dst.Addr()), Port: int(dst.Port()),
		Scheme: schemeForPort(dst.Port()), Error: friendlyNetError(err),
		Conn: &model.Conn{ID: connID, ClientAddr: src.String(), ServerAddr: dst.String()},
	}
	g.emit(e)
}

func schemeForPort(p uint16) string {
	switch p {
	case 443, 8443:
		return "https"
	case 80, 8080:
		return "http"
	}
	return ""
}

func friendlyNetError(err error) string {
	var be *BlockedError
	if errors.As(err, &be) {
		return be.Error()
	}
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "connection timed out: " + err.Error()
	case strings.Contains(err.Error(), "refused"):
		return "connection refused by the server: " + err.Error()
	case strings.Contains(err.Error(), "reset"):
		return "connection reset: " + err.Error()
	}
	return err.Error()
}

// flow is one guest TCP connection and its upstream counterpart.
type flow struct {
	g           *Gateway
	id          string
	client      net.Conn // guest side
	upstream    net.Conn // Internet side
	src, dst    netip.AddrPort
	started     time.Time
	connectTime time.Duration
}

func (f *flow) close() {
	f.client.Close()
	f.upstream.Close()
}

func (f *flow) conn(remote string) *model.Conn {
	return &model.Conn{ID: f.id, ClientAddr: f.src.String(), ServerAddr: f.dst.String(), RemoteAddr: remote}
}

// peekConn lets the gateway look at the first bytes of a stream without consuming them.
type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func (f *flow) run() {
	pc := &peekConn{Conn: f.client, r: bufio.NewReaderSize(f.client, 32<<10)}
	f.client.SetReadDeadline(time.Now().Add(f.g.opts.SniffTimeout))
	first, err := pc.r.Peek(1)
	f.client.SetReadDeadline(time.Time{})
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			// Server-speaks-first protocol (SMTP, FTP, …): relay opaquely.
			f.relayRaw(pc, "TCP", "")
			return
		}
		return // client closed without sending anything
	}
	switch {
	case first[0] == 0x16:
		f.handleTLS(pc)
	case f.sniffHTTP1(pc):
		f.handleHTTP(pc, f.upstream, "http", nil)
	default:
		f.relayRaw(pc, "TCP", "")
	}
}

var httpMethods = []string{"GET ", "POST ", "PUT ", "PATCH ", "DELETE ", "HEAD ", "OPTIONS ", "CONNECT ", "TRACE "}

// looksLikeHTTP1 reports whether b starts with an HTTP/1.x request line
// (b may be a short prefix of the stream).
func looksLikeHTTP1(b []byte) bool {
	if len(b) < 3 {
		return false
	}
	for _, m := range httpMethods {
		n := min(len(m), len(b))
		if string(b[:n]) == m[:n] {
			return true
		}
	}
	return false
}

func (f *flow) sniffHTTP1(pc *peekConn) bool {
	f.client.SetReadDeadline(time.Now().Add(f.g.opts.SniffTimeout))
	b, _ := pc.r.Peek(8)
	f.client.SetReadDeadline(time.Time{})
	return looksLikeHTTP1(b)
}

// handleTLS decides between interception and passthrough based on the ClientHello.
func (f *flow) handleTLS(pc *peekConn) {
	var hello *ClientHello
	f.client.SetReadDeadline(time.Now().Add(10 * time.Second))
	for size := 1; ; {
		b, err := pc.r.Peek(size)
		if n := pc.r.Buffered(); n > len(b) {
			b, _ = pc.r.Peek(n) // already buffered: never blocks
		}
		h, perr := parseClientHello(b)
		if perr == nil {
			hello = h
			break
		}
		if perr != errNeedMoreData || err != nil || len(b) >= pc.r.Size() {
			break
		}
		size = len(b) + 1 // block for at least one more byte
	}
	f.client.SetReadDeadline(time.Time{})
	if hello == nil {
		f.relayRaw(pc, "TLS", "unparseable TLS ClientHello")
		return
	}
	sni := hello.ServerName
	if sni == "" {
		sni = f.g.hostFor(f.dst.Addr())
	}
	ca := f.g.ca.Load()
	switch {
	case ca == nil:
		f.passthroughTLS(pc, hello, sni, "HTTPS inspection is disabled.")
		return
	case !speaksHTTP(hello.ALPN):
		f.passthroughTLS(pc, hello, sni, "The TLS connection negotiates a non-HTTP protocol ("+strings.Join(hello.ALPN, ", ")+").")
		return
	}
	if reason, ok := f.g.passthrough.Load(strings.ToLower(sni)); ok {
		f.passthroughTLS(pc, hello, sni, reason.(string))
		return
	}
	f.interceptTLS(pc, hello, sni, ca)
}

func speaksHTTP(alpn []string) bool {
	if len(alpn) == 0 {
		return true
	}
	for _, p := range alpn {
		if p == "h2" || p == "http/1.1" || p == "http/1.0" {
			return true
		}
	}
	return false
}

func filterALPN(alpn []string) []string {
	var out []string
	for _, p := range alpn {
		if p == "h2" || p == "http/1.1" {
			out = append(out, p)
		}
	}
	if len(alpn) == 0 {
		return []string{"http/1.1"}
	}
	return out
}

const pinnedReason = "HTTPS encrypted traffic detected. Payload inspection unavailable for this connection: the app rejected the sandbox certificate (certificate pinning or a custom trust store). Further connections to this host are passed through uninspected."

func (f *flow) interceptTLS(pc *peekConn, hello *ClientHello, sni string, ca *CA) {
	// 1. Upstream handshake first, so the client is offered exactly the
	//    protocol the server selected (h2 vs http/1.1).
	upStart := time.Now()
	upTLS := tls.Client(f.upstream, f.g.up.TLSClientConfig(hello.ServerName, filterALPN(hello.ALPN)))
	hctx, cancel := context.WithTimeout(f.g.ctx, 15*time.Second)
	err := upTLS.HandshakeContext(hctx)
	cancel()
	tlsTime := time.Since(upStart)
	if err != nil {
		f.tlsEvent(sni, hello, nil, false, "", "Upstream TLS handshake with "+sni+" failed: "+err.Error())
		return
	}
	upState := upTLS.ConnectionState()
	proto := upState.NegotiatedProtocol
	if proto == "" {
		proto = "http/1.1"
	}

	// 2. Client handshake with a leaf for the SNI (or the IP when there is none).
	leafHost := hello.ServerName
	if leafHost == "" {
		leafHost = f.dst.Addr().String()
	}
	cfg := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return ca.Leaf(leafHost) },
		NextProtos:     []string{proto},
		MinVersion:     tls.VersionTLS10,
	}
	if len(hello.ALPN) == 0 {
		cfg.NextProtos = nil
	}
	clientTLS := tls.Server(pc, cfg)
	hctx, cancel = context.WithTimeout(f.g.ctx, 15*time.Second)
	err = clientTLS.HandshakeContext(hctx)
	cancel()
	if err != nil {
		reason := "TLS handshake with the app failed: " + err.Error()
		if clientRejectedCert(err) {
			f.g.passthrough.Store(strings.ToLower(sni), pinnedReason)
			reason = pinnedReason
		}
		f.g.passed.Add(1)
		f.tlsEvent(sni, hello, &upState, false, reason, "")
		return
	}
	f.g.intercepted.Add(1)
	info := tlsInfo(sni, hello, &upState, true, "")
	timing := &dialTiming{connect: f.connectTime, tls: tlsTime, dns: f.dnsTime()}
	f.handleHTTP(clientTLS, upTLS, "https", &httpsCtx{info: info, alpn: proto, timing: timing})
}

// clientRejectedCert reports whether a server-side handshake failure means
// the client does not trust our certificate.
func clientRejectedCert(err error) bool {
	s := err.Error()
	for _, m := range []string{"bad certificate", "unknown certificate", "certificate unknown", "unknown ca", "certificate required",
		"access denied", "decrypt error", "handshake failure", "EOF", "connection reset", "broken pipe"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}

func tlsInfo(sni string, hello *ClientHello, st *tls.ConnectionState, intercepted bool, reason string) *model.TLSInfo {
	info := &model.TLSInfo{SNI: sni, ClientALPN: hello.ALPN, Intercepted: intercepted, PassthroughReason: reason}
	if st != nil {
		info.Version = tlsVersionName(st.Version)
		info.CipherSuite = tls.CipherSuiteName(st.CipherSuite)
		info.ALPN = st.NegotiatedProtocol
		info.ServerCerts = certInfos(st.PeerCertificates)
	}
	return info
}

func certInfos(certs []*x509.Certificate) []model.CertInfo {
	out := make([]model.CertInfo, 0, len(certs))
	for _, c := range certs {
		out = append(out, model.CertInfo{
			Subject: c.Subject.String(), Issuer: c.Issuer.String(), NotBefore: c.NotBefore, NotAfter: c.NotAfter,
			DNSNames: c.DNSNames, SHA256: fmt.Sprintf("%X", sha256Sum(c.Raw)),
		})
	}
	return out
}

// tlsEvent records a TLS connection whose payload was not (or could not be) inspected.
func (f *flow) tlsEvent(sni string, hello *ClientHello, st *tls.ConnectionState, intercepted bool, reason, errMsg string) {
	e := &model.Event{
		ID: model.NewID(), Kind: model.KindTLS, Category: model.CatOther, State: model.StateComplete, Initiator: model.InitiatorGuest,
		StartedAt: f.started, DurationMs: ms(time.Since(f.started)), Protocol: "TLS", Scheme: "https", Host: sni,
		Port: int(f.dst.Port()), Encrypted: true, TLS: tlsInfo(sni, hello, st, intercepted, reason), Conn: f.conn(f.upstream.RemoteAddr().String()),
		Error: errMsg,
	}
	if errMsg != "" {
		e.State = model.StateError
	}
	f.g.emit(e)
}

// passthroughTLS relays an encrypted connection, recording metadata only.
func (f *flow) passthroughTLS(pc *peekConn, hello *ClientHello, sni, reason string) {
	f.g.passed.Add(1)
	e := &model.Event{
		ID: model.NewID(), Kind: model.KindTLS, Category: model.CatOther, State: model.StatePending, Initiator: model.InitiatorGuest,
		StartedAt: f.started, Protocol: "TLS", Scheme: "https", Host: sni, Port: int(f.dst.Port()), Encrypted: true,
		TLS: tlsInfo(sni, hello, nil, false, reason), Conn: f.conn(f.upstream.RemoteAddr().String()),
	}
	f.g.emit(e)
	up, down, err := relay(pc, f.upstream)
	e.State, e.DurationMs = model.StateComplete, ms(time.Since(f.started))
	e.RequestSize, e.ResponseSize = up, down
	e.Conn.BytesUp, e.Conn.BytesDown = up, down
	if err != nil {
		e.Error = err.Error()
	}
	f.g.emit(e)
}

// relayRaw relays a non-HTTP stream, recording a connection event.
func (f *flow) relayRaw(client io.ReadWriter, proto, note string) {
	host := f.g.hostFor(f.dst.Addr())
	e := &model.Event{
		ID: model.NewID(), Kind: model.KindTCP, Category: model.CatOther, State: model.StatePending, Initiator: model.InitiatorGuest,
		StartedAt: f.started, Protocol: proto, Host: host, Port: int(f.dst.Port()), Encrypted: proto == "TLS",
		Conn: f.conn(f.upstream.RemoteAddr().String()), Error: note,
	}
	if proto == "TLS" {
		e.Kind = model.KindTLS
	}
	f.g.emit(e)
	up, down, err := relay(client, f.upstream)
	e.State, e.DurationMs = model.StateComplete, ms(time.Since(f.started))
	e.RequestSize, e.ResponseSize = up, down
	e.Conn.BytesUp, e.Conn.BytesDown = up, down
	if err != nil && e.Error == "" {
		e.Error = err.Error()
	}
	f.g.emit(e)
}

func (f *flow) dnsTime() time.Duration {
	if e, ok := f.g.dns.cache.lookup(f.dst.Addr()); ok && time.Since(e.resolved) < 5*time.Second {
		return time.Duration(e.tookMs * float64(time.Millisecond))
	}
	return -1
}

// relay copies both directions until both are done and returns byte counts.
func relay(client io.ReadWriter, upstream net.Conn) (up, down int64, err error) {
	var wg sync.WaitGroup
	var upErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		up, upErr = io.Copy(upstream, client)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	down, err = io.Copy(client, upstream)
	if cw, ok := client.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else if c, ok := client.(io.Closer); ok {
		c.Close()
	}
	wg.Wait()
	if err == nil {
		err = upErr
	}
	if isClosedErr(err) {
		err = nil
	}
	return up, down, err
}

func isClosedErr(err error) bool {
	if err == nil {
		return true
	}
	s := err.Error()
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || strings.Contains(s, "use of closed") ||
		strings.Contains(s, "endpoint is closed") || strings.Contains(s, "connection reset")
}

// ---- UDP --------------------------------------------------------------------

func (g *Gateway) onUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	dst := netip.AddrPortFrom(FromTCPIP(id.LocalAddress), id.LocalPort)
	src := netip.AddrPortFrom(FromTCPIP(id.RemoteAddress), id.RemotePort)
	switch {
	case dst.Port() == 53:
		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			return false
		}
		go g.guard("dns", func() { g.serveDNSUDP(g.ctx, gonet.NewUDPConn(&wq, ep), src, dst) })
		return true
	case dst.Port() == 67 || dst.Port() == 68:
		return true // DHCP is served by the bound DHCP socket
	case dst.Port() == 443 && g.opts.Policy.BlockQUIC:
		return false // ICMP port unreachable → QUIC clients fall back to TCP immediately
	case dst.Addr() == g.opts.Addressing.Gateway || dst.Addr() == g.opts.Addressing.DNS:
		return false
	}
	if err := g.opts.Policy.Check(dst); err != nil {
		g.blocked.Add(1)
		return false
	}
	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		return false
	}
	go g.guard("udp", func() { g.relayUDP(gonet.NewUDPConn(&wq, ep), src, dst) })
	return true
}

// relayUDP NATs one guest UDP flow through a host socket until idle.
func (g *Gateway) relayUDP(guest net.Conn, src, dst netip.AddrPort) {
	defer guest.Close()
	started := time.Now()
	up, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(dst))
	if err != nil {
		return
	}
	defer up.Close()
	const idle = 60 * time.Second
	var upBytes, downBytes atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			up.SetReadDeadline(time.Now().Add(idle))
			n, err := up.Read(buf)
			if err != nil {
				guest.Close()
				return
			}
			downBytes.Add(int64(n))
			guest.Write(buf[:n])
		}
	}()
	buf := make([]byte, 65535)
	for {
		guest.SetReadDeadline(time.Now().Add(idle))
		n, err := guest.Read(buf)
		if err != nil {
			break
		}
		upBytes.Add(int64(n))
		up.Write(buf[:n])
	}
	up.Close()
	<-done
	g.emit(&model.Event{
		ID: model.NewID(), Kind: model.KindUDP, Category: model.CatOther, State: model.StateComplete, Initiator: model.InitiatorGuest,
		StartedAt: started, DurationMs: ms(time.Since(started)), Protocol: "UDP", Host: g.hostFor(dst.Addr()), Port: int(dst.Port()),
		RequestSize: upBytes.Load(), ResponseSize: downBytes.Load(),
		Conn: &model.Conn{ClientAddr: src.String(), ServerAddr: dst.String(), BytesUp: upBytes.Load(), BytesDown: downBytes.Load()},
	})
}
