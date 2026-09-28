package network

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// ReplayRequest describes a request to re-issue.
type ReplayRequest struct {
	Original *model.Event // the captured event being replayed
	Body     []byte       // stored request body (as transmitted)
}

// Replay re-sends a captured HTTP request from the host through the same
// egress policy and records the result as a NEW event (Initiator=replay,
// ReplayOf=original). The original event is never modified.
func (g *Gateway) Replay(ctx context.Context, rr ReplayRequest) (*model.Event, error) {
	o := rr.Original
	switch {
	case o == nil:
		return nil, fmt.Errorf("nothing to replay")
	case o.Kind != model.KindHTTP:
		return nil, fmt.Errorf("only HTTP requests can be replayed (this is a %s event)", o.Kind)
	case o.Encrypted || o.Method == "":
		return nil, fmt.Errorf("this request was not inspected (encrypted traffic), so it cannot be replayed")
	case o.RequestBody != nil && o.RequestBody.Truncated:
		return nil, fmt.Errorf("the captured request body was truncated (%d of %d bytes), so the request cannot be replayed faithfully", o.RequestBody.Stored, o.RequestBody.Size)
	}
	start := time.Now()
	e := &model.Event{
		ID: model.NewID(), Kind: model.KindHTTP, State: model.StatePending, Initiator: model.InitiatorReplay, ReplayOf: o.ID,
		StartedAt: start, Method: o.Method, Scheme: o.Scheme, Host: o.Host, Port: o.Port, Path: o.Path, Query: o.Query,
		RequestHeaders: o.RequestHeaders, Package: o.Package, Category: o.Category,
	}
	g.emit(e)

	req, err := http.NewRequestWithContext(ctx, o.Method, o.URL(), bytes.NewReader(rr.Body))
	if err != nil {
		return g.replayFailed(e, start, err)
	}
	if len(rr.Body) == 0 {
		req.Body = http.NoBody
	}
	for _, h := range o.RequestHeaders {
		switch strings.ToLower(h.Name) {
		case "host":
			req.Host = h.Value
		case "content-length":
		default:
			if !strings.HasPrefix(h.Name, ":") {
				req.Header.Add(h.Name, h.Value)
			}
		}
	}
	removeHopByHop(req.Header, false)
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "")
	}
	serverName := o.Host
	if o.TLS != nil && o.TLS.SNI != "" {
		serverName = o.TLS.SNI
	}
	var connStart, connDone time.Time
	var remote string
	tt := &traceTimes{}
	t := &http.Transport{
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
		TLSClientConfig:    &tls.Config{ServerName: serverName, RootCAs: g.up.RootCAs()},
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			connStart = time.Now()
			host, port := model.SplitHostPort(addr, o.Port)
			c, err := g.up.DialHost(ctx, host, port)
			connDone = time.Now()
			if c != nil {
				remote = c.RemoteAddr().String()
			}
			return c, err
		},
	}
	defer t.CloseIdleConnections()
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), tt.trace()))
	e.RequestSize = int64(len(rr.Body))
	if len(rr.Body) > 0 && g.bodies != nil {
		if h, err := g.bodies.Put(rr.Body); err == nil {
			e.RequestBody = &model.BodyRef{Hash: h, Size: int64(len(rr.Body)), Stored: int64(len(rr.Body)),
				Encoding: req.Header.Get("Content-Encoding"), MIME: model.MediaType(req.Header.Get("Content-Type"))}
		}
	}

	resp, err := t.RoundTrip(req)
	if err != nil {
		return g.replayFailed(e, start, err)
	}
	defer resp.Body.Close()
	c := newCapBuf(g.opts.MaxBodyBytes)
	_, cerr := io.Copy(writerFunc(func(b []byte) (int, error) { c.Write(b); return len(b), nil }), resp.Body)
	end := time.Now()

	e.Protocol = protoNameResp(resp)
	e.Status = resp.StatusCode
	e.StatusText = strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode)))
	e.ResponseHeaders = model.FromHTTP(resp.Header)
	e.MIME = model.MediaType(resp.Header.Get("Content-Type"))
	e.Category = model.Categorize(model.KindHTTP, e.MIME, e.Path)
	e.ResponseSize = c.total
	if c.total > 0 && g.bodies != nil {
		if h, err := g.bodies.Put(c.buf); err == nil {
			e.ResponseBody = &model.BodyRef{Hash: h, Size: c.total, Stored: int64(len(c.buf)), Truncated: c.truncated(),
				Encoding: resp.Header.Get("Content-Encoding"), MIME: e.MIME}
		}
	}
	gotConn, wrote, firstByte, _ := tt.get()
	tt.mu.Lock()
	tlsStart, tlsDone := tt.tlsStart, tt.tlsDone
	tt.mu.Unlock()
	tm := model.NewTiming()
	if !connStart.IsZero() {
		tm.Connect = ms(connDone.Sub(connStart))
	}
	if !tlsStart.IsZero() {
		tm.TLS = ms(tlsDone.Sub(tlsStart))
	}
	if !wrote.IsZero() && !gotConn.IsZero() {
		tm.Send = ms(wrote.Sub(gotConn))
	}
	if !firstByte.IsZero() && !wrote.IsZero() {
		tm.Wait = ms(firstByte.Sub(wrote))
		tm.Receive = ms(end.Sub(firstByte))
	}
	e.Timing = tm
	if resp.TLS != nil {
		e.TLS = &model.TLSInfo{SNI: serverName, Version: tlsVersionName(resp.TLS.Version), CipherSuite: tls.CipherSuiteName(resp.TLS.CipherSuite),
			ALPN: resp.TLS.NegotiatedProtocol, ServerCerts: certInfos(resp.TLS.PeerCertificates)}
	}
	e.Conn = &model.Conn{ID: "replay", RemoteAddr: remote}
	e.DurationMs = ms(end.Sub(start))
	e.State = model.StateComplete
	if cerr != nil {
		e.State, e.Error = model.StateError, "response body interrupted: "+friendlyNetError(cerr)
	}
	g.emit(e)
	return e, nil
}

func (g *Gateway) replayFailed(e *model.Event, start time.Time, err error) (*model.Event, error) {
	e.State, e.Error, e.DurationMs = model.StateError, friendlyNetError(err), ms(time.Since(start))
	g.emit(e)
	return e, nil
}

func protoNameResp(r *http.Response) string {
	if r.ProtoMajor == 2 {
		return "HTTP/2"
	}
	return r.Proto
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }
