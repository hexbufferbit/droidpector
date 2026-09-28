package network

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

// httpsCtx carries interception details into the HTTP layer.
type httpsCtx struct {
	info   *model.TLSInfo
	alpn   string
	timing *dialTiming
}

// dialTiming describes how the first upstream connection was established.
// Only the first request on it reports these phases (like DevTools).
type dialTiming struct {
	dns, connect, tls time.Duration
}

// hopByHop headers are connection-specific and never forwarded (RFC 9110 §7.6.1).
var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func removeHopByHop(h http.Header, keepUpgrade bool) {
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			if name = strings.TrimSpace(name); name != "" && !(keepUpgrade && strings.EqualFold(name, "upgrade")) {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHop {
		if keepUpgrade && (name == "Connection" || name == "Upgrade") {
			continue
		}
		h.Del(name)
	}
}

func isWebSocketUpgrade(h http.Header) bool {
	return headerHasToken(h, "Connection", "upgrade") && strings.EqualFold(strings.TrimSpace(h.Get("Upgrade")), "websocket")
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// firstConnDialer hands out a pre-established connection once, then dials
// fresh connections to the same destination (keep-alive reconnects).
type firstConnDialer struct {
	mu    sync.Mutex
	first net.Conn
	dial  func(ctx context.Context) (net.Conn, error)
}

func (d *firstConnDialer) get(ctx context.Context) (net.Conn, bool, error) {
	d.mu.Lock()
	c := d.first
	d.first = nil
	d.mu.Unlock()
	if c != nil {
		return c, true, nil
	}
	nc, err := d.dial(ctx)
	return nc, false, err
}

// exchanger proxies and records HTTP exchanges of one guest connection.
type exchanger struct {
	f        *flow
	scheme   string
	hc       *httpsCtx
	rt       http.RoundTripper
	handlers sync.WaitGroup // in-flight handlers (incl. hijacked WebSocket relays)

	mu        sync.Mutex
	firstUsed bool // dial timing already reported
}

// handleHTTP serves one guest connection (plain HTTP/1.x, or TLS-terminated
// HTTP/1.1 or HTTP/2 negotiated via ALPN) and proxies every exchange upstream.
func (f *flow) handleHTTP(client net.Conn, upstream net.Conn, scheme string, hc *httpsCtx) {
	ex := &exchanger{f: f, scheme: scheme, hc: hc}
	serverName := ""
	if hc != nil {
		serverName = hc.info.SNI
	}
	h2 := hc != nil && hc.alpn == "h2"
	dialer := &firstConnDialer{first: upstream}
	t := &http.Transport{
		DisableCompression:  true,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     90 * time.Second,
	}
	protos := []string{"http/1.1"}
	if h2 {
		protos = []string{"h2"}
		t.ForceAttemptHTTP2 = true
	} else {
		t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{} // HTTP/1.1 only
	}
	if scheme == "https" {
		dialer.dial = func(ctx context.Context) (net.Conn, error) {
			c, err := f.g.up.Dial(ctx, f.dst)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(c, f.g.up.TLSClientConfig(serverName, protos))
			if err := tc.HandshakeContext(ctx); err != nil {
				c.Close()
				return nil, err
			}
			return tc, nil
		}
		t.DialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, _, err := dialer.get(ctx)
			return c, err
		}
	} else {
		dialer.dial = func(ctx context.Context) (net.Conn, error) { return f.g.up.Dial(ctx, f.dst) }
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, _, err := dialer.get(ctx)
			return c, err
		}
	}
	ex.rt = t
	defer t.CloseIdleConnections()

	done := make(chan struct{})
	var once sync.Once
	srv := &http.Server{
		Handler:           ex,
		ReadHeaderTimeout: 2 * time.Minute,
		IdleTimeout:       5 * time.Minute,
		ErrorLog:          discardLogger,
		ConnState: func(_ net.Conn, st http.ConnState) {
			if st == http.StateClosed || st == http.StateHijacked {
				once.Do(func() { close(done) })
			}
		},
	}
	go func() {
		select {
		case <-f.g.ctx.Done():
			client.Close()
		case <-done:
		}
	}()
	// The connection is passed through unwrapped so a *tls.Conn that
	// negotiated "h2" is served by net/http's built-in HTTP/2 server.
	srv.Serve(&oneConnListener{conn: client, done: done})
	ex.handlers.Wait()
}

// ServeHTTP proxies one exchange and records it.
func (ex *exchanger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ex.handlers.Add(1)
	defer ex.handlers.Done()
	defer func() {
		if p := recover(); p != nil {
			if p != http.ErrAbortHandler {
				ex.f.g.panics.Add(1)
				ex.f.g.log.Error("HTTP exchange panic recovered", "panic", fmt.Sprint(p))
			}
			panic(http.ErrAbortHandler) // drop the guest connection, never crash
		}
	}()
	f := ex.f
	start := time.Now()
	ws := r.ProtoMajor == 1 && isWebSocketUpgrade(r.Header)

	host, port := model.SplitHostPort(r.Host, int(f.dst.Port()))
	if host == "" && ex.hc != nil {
		host = ex.hc.info.SNI
	}
	if host == "" {
		host = f.g.hostFor(f.dst.Addr())
	}
	scheme := ex.scheme
	if ws {
		scheme = map[string]string{"http": "ws", "https": "wss"}[ex.scheme]
	}
	e := &model.Event{
		ID: model.NewID(), Kind: model.KindHTTP, State: model.StatePending, Initiator: model.InitiatorGuest,
		StartedAt: start, Protocol: protoName(r), Method: r.Method, Scheme: scheme, Host: strings.ToLower(host), Port: port,
		Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Conn: f.conn(f.upstream.RemoteAddr().String()),
	}
	if ex.hc != nil {
		e.TLS = ex.hc.info
	}
	reqHeaders := r.Header.Clone()
	if r.ProtoMajor == 1 {
		reqHeaders.Set("Host", r.Host)
	}
	e.RequestHeaders = model.FromHTTP(reqHeaders)
	if ws {
		e.Kind, e.Category = model.KindWebSocket, model.CatWebSocket
	}
	f.g.emit(e)

	reqCap := newCapBuf(f.g.opts.MaxBodyBytes)
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = ex.scheme
	out.URL.Host = r.Host
	if out.URL.Host == "" {
		out.URL.Host = host
	}
	if r.Body != nil && r.Body != http.NoBody {
		out.Body = &captureReader{rc: r.Body, cap: reqCap}
	}
	removeHopByHop(out.Header, ws)
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header.Set("User-Agent", "") // suppress Go's default User-Agent
	}

	tt := &traceTimes{}
	out = out.WithContext(httptrace.WithClientTrace(out.Context(), tt.trace()))

	resp, err := ex.rt.RoundTrip(out)
	if err != nil {
		e.State, e.Error = model.StateError, friendlyNetError(err)
		ex.finishRequest(e, reqCap, r.Header.Get("Content-Encoding"))
		e.DurationMs = ms(time.Since(start))
		f.g.emit(e)
		// Mirror the failure to the app (connection drop) instead of inventing
		// a response, so the app behaves exactly as it would on a real network.
		panic(http.ErrAbortHandler)
	}
	defer resp.Body.Close()

	e.Status, e.StatusText = resp.StatusCode, strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
	e.ResponseHeaders = model.FromHTTP(resp.Header)
	ct := resp.Header.Get("Content-Type")
	e.MIME = model.MediaType(ct)
	if !ws {
		e.Category = model.Categorize(model.KindHTTP, ct, e.Path)
	}
	ex.finishRequest(e, reqCap, r.Header.Get("Content-Encoding"))

	if ws && resp.StatusCode == http.StatusSwitchingProtocols {
		ex.relayWebSocket(w, r, resp, e, start)
		return
	}

	// Stream the response to the guest while capturing it.
	h := w.Header()
	for k, v := range resp.Header {
		h[k] = v
	}
	removeHopByHop(h, false)
	for k := range resp.Trailer {
		h.Add("Trailer", k)
	}
	w.WriteHeader(resp.StatusCode)
	respCap := newCapBuf(f.g.opts.MaxBodyBytes)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var copyErr error
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			respCap.Write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				copyErr = fmt.Errorf("the app closed the connection before the response was complete")
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				copyErr = fmt.Errorf("response body interrupted: %s", friendlyNetError(rerr))
			}
			break
		}
	}
	for k, v := range resp.Trailer {
		for _, vv := range v {
			w.Header().Add(http.TrailerPrefix+k, vv)
		}
	}
	end := time.Now()
	e.ResponseSize = respCap.total
	e.ResponseBody = ex.storeBody(respCap, resp.Header.Get("Content-Encoding"), e.MIME)
	e.DurationMs = ms(end.Sub(start))
	gotConn, wrote, firstByte, reused := tt.get()
	e.Timing = ex.timing(start, gotConn, wrote, firstByte, end, reused)
	e.State = model.StateComplete
	if copyErr != nil {
		e.State, e.Error = model.StateError, copyErr.Error()
	}
	f.g.emit(e)
	if copyErr != nil {
		panic(http.ErrAbortHandler) // make the server drop the guest connection
	}
}

func protoName(r *http.Request) string {
	if r.ProtoMajor == 2 {
		return "HTTP/2"
	}
	return r.Proto
}

func (ex *exchanger) finishRequest(e *model.Event, reqCap *capBuf, encoding string) {
	e.RequestSize = reqCap.total
	e.RequestBody = ex.storeBody(reqCap, encoding, model.MediaType(e.RequestHeaders.Get("Content-Type")))
}

func (ex *exchanger) storeBody(c *capBuf, encoding, mime string) *model.BodyRef {
	if c.total == 0 || ex.f.g.bodies == nil {
		return nil
	}
	hash, err := ex.f.g.bodies.Put(c.buf)
	if err != nil {
		ex.f.g.log.Warn("storing body failed", "err", err)
		return nil
	}
	return &model.BodyRef{Hash: hash, Size: c.total, Stored: int64(len(c.buf)), Truncated: c.truncated(), Encoding: strings.TrimSpace(encoding), MIME: mime}
}

func (ex *exchanger) timing(start, gotConn, wrote, firstByte, end time.Time, reused bool) *model.Timing {
	t := model.NewTiming()
	ex.mu.Lock()
	first := !ex.firstUsed && !reused
	if first {
		ex.firstUsed = true
	}
	ex.mu.Unlock()
	if first {
		t.Connect = ms(ex.f.connectTime)
		if d := ex.f.dnsTime(); d >= 0 {
			t.DNS = ms(d)
		}
		if ex.hc != nil && ex.hc.timing != nil {
			t.TLS = ms(ex.hc.timing.tls)
		}
	}
	if !gotConn.IsZero() {
		t.Blocked = ms(gotConn.Sub(start))
	}
	if !wrote.IsZero() && !gotConn.IsZero() {
		t.Send = ms(wrote.Sub(gotConn))
	}
	if !firstByte.IsZero() && !wrote.IsZero() {
		t.Wait = ms(firstByte.Sub(wrote))
		t.Receive = ms(end.Sub(firstByte))
	}
	return t
}

// relayWebSocket completes a 101 upgrade and relays frames while recording them.
func (ex *exchanger) relayWebSocket(w http.ResponseWriter, r *http.Request, resp *http.Response, e *model.Event, start time.Time) {
	f := ex.f
	upConn, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		e.State, e.Error = model.StateError, "upstream did not provide a raw connection after 101"
		f.g.emit(e)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		e.State, e.Error = model.StateError, "WebSocket upgrade is not supported on this connection"
		f.g.emit(e)
		return
	}
	clientConn, brw, err := hj.Hijack()
	if err != nil {
		e.State, e.Error = model.StateError, "WebSocket hijack failed: "+err.Error()
		f.g.emit(e)
		return
	}
	defer clientConn.Close()
	defer upConn.Close()

	// Write the 101 response verbatim to the guest.
	fmt.Fprintf(brw, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Write(brw)
	brw.WriteString("\r\n")
	if err := brw.Flush(); err != nil {
		return
	}
	e.DurationMs = ms(time.Since(start))
	f.g.emit(e) // connection established; frames follow

	deflate := wsDeflateNegotiated(resp.Header.Values("Sec-WebSocket-Extensions"))
	var seqMu sync.Mutex
	var seq int64
	var upBytes, downBytes int64
	emit := func(fr model.WSFrame) {
		seqMu.Lock()
		seq++
		fr.Seq = seq
		seqMu.Unlock()
		f.g.sink.Frame(e.ID, fr)
	}
	toServer := newWSParser(true, f.g.opts.MaxFrameBytes, deflate, emit)
	toClient := newWSParser(false, f.g.opts.MaxFrameBytes, deflate, emit)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// brw.Reader holds bytes the client already sent after the handshake.
		upBytes, _ = io.Copy(upConn, &tapReader{r: io.MultiReader(bufferedPart(brw.Reader), clientConn), p: toServer})
		upConn.Close()
	}()
	downBytes, _ = io.Copy(clientConn, &tapReader{r: upConn, p: toClient})
	clientConn.Close()
	wg.Wait()

	e.State = model.StateComplete
	e.DurationMs = ms(time.Since(start))
	e.RequestSize, e.ResponseSize = upBytes, downBytes
	f.g.emit(e)
}

func bufferedPart(r *bufio.Reader) io.Reader {
	n := r.Buffered()
	if n == 0 {
		return strings.NewReader("")
	}
	b, _ := r.Peek(n)
	return strings.NewReader(string(b))
}

// traceTimes collects httptrace timestamps. Callbacks run on transport
// goroutines (WroteRequest can even fire after RoundTrip returned), hence the lock.
type traceTimes struct {
	mu                                     sync.Mutex
	connStart, connDone, tlsStart, tlsDone time.Time
	gotConn, wrote, firstByte              time.Time
	reused                                 bool
}

func (t *traceTimes) set(f func()) {
	t.mu.Lock()
	f()
	t.mu.Unlock()
}

func (t *traceTimes) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		ConnectStart:         func(string, string) { t.set(func() { t.connStart = time.Now() }) },
		ConnectDone:          func(string, string, error) { t.set(func() { t.connDone = time.Now() }) },
		TLSHandshakeStart:    func() { t.set(func() { t.tlsStart = time.Now() }) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { t.set(func() { t.tlsDone = time.Now() }) },
		GotConn:              func(i httptrace.GotConnInfo) { t.set(func() { t.gotConn, t.reused = time.Now(), i.Reused }) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { t.set(func() { t.wrote = time.Now() }) },
		GotFirstResponseByte: func() { t.set(func() { t.firstByte = time.Now() }) },
	}
}

func (t *traceTimes) get() (gotConn, wrote, firstByte time.Time, reused bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gotConn, t.wrote, t.firstByte, t.reused
}

// capBuf keeps the first max bytes of a stream and counts the rest.
type capBuf struct {
	buf   []byte
	max   int64
	total int64
}

func newCapBuf(max int64) *capBuf { return &capBuf{max: max} }

func (c *capBuf) Write(b []byte) {
	c.total += int64(len(b))
	if room := c.max - int64(len(c.buf)); room > 0 {
		c.buf = append(c.buf, b[:min(int64(len(b)), room)]...)
	}
}

func (c *capBuf) truncated() bool { return c.total > int64(len(c.buf)) }

type captureReader struct {
	rc  io.ReadCloser
	cap *capBuf
}

func (c *captureReader) Read(b []byte) (int, error) {
	n, err := c.rc.Read(b)
	if n > 0 {
		c.cap.Write(b[:n])
	}
	return n, err
}

func (c *captureReader) Close() error { return c.rc.Close() }

// oneConnListener serves exactly one connection to http.Server and then
// blocks Accept until that connection closes, so Serve returns afterwards.
type oneConnListener struct {
	mu   sync.Mutex
	conn net.Conn
	done chan struct{}
}

var errListenerDone = errors.New("listener done")

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, errListenerDone
}

func (l *oneConnListener) Close() error { return nil }
func (l *oneConnListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(10, 0, 2, 2)}
}

var discardLogger = log.New(io.Discard, "", 0)
