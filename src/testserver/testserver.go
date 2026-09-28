// Package testserver is the deterministic HTTP/HTTPS/WebSocket server used by
// integration and E2E tests (and by cmd/testserver). Every response is a pure
// function of the request, so tests never depend on the Internet.
package testserver

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Server bundles plain HTTP and HTTPS listeners serving the same handler.
type Server struct {
	HTTPAddr  string // host:port of the plain listener
	HTTPSAddr string // host:port of the TLS listener
	CA        *x509.Certificate
	CAPEM     []byte

	httpSrv, httpsSrv *http.Server
}

// Start listens on 127.0.0.1 (random ports unless given) and serves until Close.
// hostnames are the names the TLS certificate is valid for.
func Start(httpAddr, httpsAddr string, hostnames []string) (*Server, error) {
	if httpAddr == "" {
		httpAddr = "127.0.0.1:0"
	}
	if httpsAddr == "" {
		httpsAddr = "127.0.0.1:0"
	}
	caCert, caKey, err := newCA()
	if err != nil {
		return nil, err
	}
	leaf, err := newLeaf(caCert, caKey, append([]string{"localhost", "127.0.0.1"}, hostnames...))
	if err != nil {
		return nil, err
	}
	hl, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return nil, err
	}
	tl, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		hl.Close()
		return nil, err
	}
	h := Handler()
	s := &Server{
		HTTPAddr: hl.Addr().String(), HTTPSAddr: tl.Addr().String(), CA: caCert,
		CAPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}),
		httpSrv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ErrorLog: quiet},
		httpsSrv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ErrorLog: quiet,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{leaf}, NextProtos: []string{"h2", "http/1.1"}}},
	}
	go s.httpSrv.Serve(hl)
	go s.httpsSrv.Serve(tls.NewListener(tl, s.httpsSrv.TLSConfig))
	return s, nil
}

// quiet discards the stdlib server's connection-level error logs (tests
// deliberately reset and abort connections).
var quiet = log.New(io.Discard, "", 0)

// Close stops both listeners.
func (s *Server) Close() {
	s.httpSrv.Close()
	s.httpsSrv.Close()
}

// Deterministic payloads.
var (
	JSONBody  = []byte(`{"id":42,"name":"droidpector","tags":["android","network"],"nested":{"ok":true,"n":1.5}}`)
	TextBody  = []byte("Hello from the droidpector test server.\n")
	LargeSize = 5 << 20
)

// LargeBody returns the deterministic /test/large payload.
func LargeBody() []byte {
	b := make([]byte, LargeSize)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// PNG returns the deterministic /test/image payload (16x16 gradient).
func PNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 16), uint8(y * 16), 128, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// BinaryBody is the deterministic /test/binary payload (all byte values).
func BinaryBody() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// Handler returns the test endpoints.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/test/get", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Test", "get")
		fmt.Fprintf(w, `{"method":%q,"query":%q}`, r.Method, r.URL.RawQuery)
	})
	mux.HandleFunc("/test/post", echo)
	mux.HandleFunc("/test/echo", echo)
	mux.HandleFunc("/test/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(JSONBody)
	})
	mux.HandleFunc("/test/text", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(TextBody)
	})
	mux.HandleFunc("/test/html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!doctype html><html><body><h1>Test</h1></body></html>")
	})
	mux.HandleFunc("/test/xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<?xml version="1.0"?><root><item id="1">one</item></root>`)
	})
	mux.HandleFunc("/test/binary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(BinaryBody())
	})
	mux.HandleFunc("/test/large", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(LargeSize))
		w.Write(LargeBody())
	})
	mux.HandleFunc("/test/gzip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write(JSONBody)
		gz.Close()
	})
	mux.HandleFunc("/test/image", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(PNG())
	})
	mux.HandleFunc("/test/error", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"deterministic failure"}`)
	})
	mux.HandleFunc("/test/status/", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/test/status/"))
		if err != nil || code < 200 || code > 599 {
			http.Error(w, "bad status", http.StatusBadRequest)
			return
		}
		if code >= 300 && code < 400 {
			w.Header().Set("Location", "/test/get")
		}
		w.WriteHeader(code)
		if code != http.StatusNoContent && code != http.StatusNotModified {
			fmt.Fprintf(w, "status %d\n", code)
		}
	})
	mux.HandleFunc("/test/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/test/get", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/test/slow", func(w http.ResponseWriter, r *http.Request) {
		d, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		if d <= 0 {
			d = 1500
		}
		select {
		case <-time.After(time.Duration(d) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "slow response\n")
	})
	mux.HandleFunc("/test/timeout", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answers
	})
	mux.HandleFunc("/test/reset", func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok { // HTTP/2: abort the stream
			panic(http.ErrAbortHandler)
		}
		c, _, err := hj.Hijack()
		if err != nil {
			return
		}
		if tc, ok := c.(*net.TCPConn); ok {
			tc.SetLinger(0) // RST instead of FIN
		}
		c.Close()
	})
	mux.HandleFunc("/test/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "data: event %d\n\n", i)
			if f != nil {
				f.Flush()
			}
		}
	})
	mux.HandleFunc("/test/ws", serveWebSocket)
	return mux
}

// echo returns a JSON description of the request, including its body.
func echo(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 50<<20))
	headers := map[string]string{}
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		headers[k] = strings.Join(r.Header[k], ", ")
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"method": r.Method, "path": r.URL.Path, "query": r.URL.RawQuery, "headers": headers,
		"body": string(body), "bodyLength": len(body), "proto": r.Proto,
	})
}

// serveWebSocket is a minimal RFC 6455 echo endpoint: it echoes text/binary
// messages and answers "close" by closing.
func serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusUpgradeRequired)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	c, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer c.Close()
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	brw.Flush()
	writeWSFrame(c, 1, []byte("welcome"))
	for {
		op, data, err := readWSFrame(brw.Reader)
		if err != nil {
			return
		}
		switch op {
		case 8:
			writeWSFrame(c, 8, data)
			return
		case 9:
			writeWSFrame(c, 10, data)
		case 1, 2:
			if string(data) == "close" {
				writeWSFrame(c, 8, []byte{0x03, 0xe8})
				return
			}
			writeWSFrame(c, op, append([]byte("echo: "), data...))
		}
	}
}

func readWSFrame(r *bufio.Reader) (int, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	op := int(h[0] & 0x0f)
	n := int64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = int64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = int64(binary.BigEndian.Uint64(b[:]))
	}
	if n > 16<<20 {
		return 0, nil, fmt.Errorf("frame too large")
	}
	var mask [4]byte
	masked := h[1]&0x80 != 0
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	return op, data, nil
}

func writeWSFrame(w io.Writer, op int, data []byte) error {
	hdr := []byte{0x80 | byte(op)}
	switch n := len(data); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 65536:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	_, err := w.Write(append(hdr, data...))
	return err
}

func newCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "droidpector Test Server CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	c, err := x509.ParseCertificate(der)
	return c, key, err
}

func newLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, names []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
