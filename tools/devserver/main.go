// Command devserver runs the real application core without a VM: a simulated
// guest (second user-mode stack speaking the QEMU link protocol) drives real
// HTTP/HTTPS/WebSocket/DNS traffic through the real gateway to the
// deterministic test server. It is used for UI development and for the
// browser-level E2E tests of the Network Inspector.
//
//	go run ./tools/devserver -port 8765
//	POST /dev/traffic?kind=get|post|json|image|error|ws|pinned|all  (bearer auth)
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/droidpector/apkinspector/src/core"
	"github.com/droidpector/apkinspector/src/ipc"
	"github.com/droidpector/apkinspector/src/network"
	"github.com/droidpector/apkinspector/src/network/guestsim"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/testserver"
	"github.com/droidpector/apkinspector/src/ui"
)

const (
	apiHost  = "api.example.test"
	httpHost = "plain.example.test"
	rawHost  = "raw.example.test" // line-echo server: exercises the raw TCP path
)

func main() {
	port := flag.Int("port", 0, "API port")
	dataDir := flag.String("data", "", "data directory (default: temporary)")
	flag.Parse()
	if err := run(*port, *dataDir); err != nil {
		fmt.Fprintln(os.Stderr, "devserver:", err)
		os.Exit(1)
	}
}

func run(port int, dataDir string) error {
	srv, err := testserver.Start("", "", []string{apiHost})
	if err != nil {
		return err
	}
	defer srv.Close()
	if dataDir == "" {
		if dataDir, err = os.MkdirTemp("", "apkinspector-dev-*"); err != nil {
			return err
		}
		defer os.RemoveAll(dataDir)
	}
	caFile := filepath.Join(dataDir, "testserver-ca.pem")
	os.MkdirAll(dataDir, 0o700)
	if err := os.WriteFile(caFile, srv.CAPEM, 0o600); err != nil {
		return err
	}
	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer rawLn.Close()
	go serveRawEcho(rawLn)
	paths := platform.NewPaths(dataDir, filepath.Join(dataDir, "no-runtime"), dataDir)
	cfg := platform.DefaultConfig()
	cfg.HostMappings = []string{apiHost + "=" + srv.HTTPSAddr, httpHost + "=" + srv.HTTPAddr, rawHost + "=" + rawLn.Addr().String()}
	cfg.ExtraRootsPEM = caFile
	logs, err := platform.OpenLoggers(paths.LogDir, platform.LevelTrace, false)
	if err != nil {
		return err
	}
	defer logs.Close()
	app, err := core.NewApp("dev", paths, cfg, logs)
	if err != nil {
		return err
	}
	defer app.Close()

	// Simulated guest attached to the real gateway, trusting a real per-run CA.
	ca, err := newCA(app)
	if err != nil {
		return err
	}
	vmSide, gwSide := net.Pipe()
	go app.Gateway.Stack().ServeQEMU(context.Background(), gwSide)
	a := app.Gateway.Stack().Addressing()
	guest, err := guestsim.New(vmSide, a.Guest, a.Gateway, a.DNS)
	if err != nil {
		return err
	}
	defer guest.Close()
	if _, err := app.Sessions.Start(context.Background(), "simulated"); err != nil {
		return err
	}
	app.Sessions.SetAPK(context.Background(), "simulated.apk", "com.apkinspector.testapp")

	s, err := ipc.NewServer(app, ui.Assets(), port, logs.App)
	if err != nil {
		return err
	}
	gen := &generator{guest: guest, roots: ca, pinned: srv.CA}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /dev/traffic", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.Token() {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		n, err := gen.run(r.URL.Query().Get("kind"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"requests":%d}`, n)
	})
	devLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go http.Serve(devLn, mux)
	go s.Serve()
	info, _ := json.Marshal(map[string]string{"url": s.URL(), "base": s.BaseURL(), "token": s.Token(), "dev": "http://" + devLn.Addr().String()})
	fmt.Println(string(info))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	return nil
}

// newCA installs a fresh interception CA in the gateway and returns the pool
// the simulated guest trusts (its "system store").
func newCA(app *core.App) (*x509.CertPool, error) {
	ca, err := networkCA()
	if err != nil {
		return nil, err
	}
	app.Gateway.SetCA(ca)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate())
	return pool, nil
}

type generator struct {
	guest  *guestsim.Guest
	roots  *x509.CertPool
	pinned *x509.Certificate
}

func (g *generator) run(kind string) (int, error) {
	c := g.guest.HTTPClient(g.roots, true)
	c1 := g.guest.HTTPClient(g.roots, false)
	do := func(method, url, ct, body string) error {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if body == "" {
			req.Body = http.NoBody
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		req.Header.Set("User-Agent", "okhttp/4.12.0")
		req.Header.Set("Authorization", "Bearer demo-token")
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	}
	base := "https://" + apiHost
	steps := map[string]func() error{
		"get": func() error { return do("GET", base+"/test/get?page=1&q=hello%20world", "", "") },
		"post": func() error {
			return do("POST", base+"/test/post", "application/json", `{"username":"amir","password":"secret"}`)
		},
		"json":  func() error { return do("GET", base+"/test/json", "", "") },
		"image": func() error { return do("GET", base+"/test/image", "", "") },
		"error": func() error { return do("GET", base+"/test/error", "", "") },
		"401":   func() error { return do("GET", base+"/test/status/401", "", "") },
		"html":  func() error { return do("GET", base+"/test/html", "", "") },
		"http": func() error {
			resp, err := c1.Get("http://" + httpHost + "/test/text")
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			return err
		},
		"ws":     g.websocket,
		"pinned": g.pinnedCall,
		"tcp":    g.rawTCP,
	}
	order := []string{"get", "post", "json", "image", "error", "401", "html", "http", "ws"}
	if kind == "" || kind == "all" {
		for _, k := range order {
			if err := steps[k](); err != nil {
				return 0, fmt.Errorf("%s: %w", k, err)
			}
		}
		return len(order), nil
	}
	if kind == "bulk" {
		for i := 0; i < 500; i++ {
			if err := do("GET", fmt.Sprintf("%s/test/get?i=%d", base, i), "", ""); err != nil {
				return i, err
			}
		}
		return 500, nil
	}
	f, ok := steps[kind]
	if !ok {
		return 0, fmt.Errorf("unknown kind %q", kind)
	}
	return 1, f()
}

func (g *generator) websocket() error {
	ctx := context.Background()
	ips, err := g.guest.Resolver().LookupNetIP(ctx, "ip4", apiHost)
	if err != nil {
		return err
	}
	raw, err := g.guest.DialTCP(ctx, netip.AddrPortFrom(ips[0], 443))
	if err != nil {
		return err
	}
	conn := tls.Client(raw, &tls.Config{ServerName: apiHost, RootCAs: g.roots, NextProtos: []string{"http/1.1"}})
	defer conn.Close()
	key := make([]byte, 16)
	rand.Read(key)
	fmt.Fprintf(conn, "GET /test/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		apiHost, base64.StdEncoding.EncodeToString(key))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 101 {
		return fmt.Errorf("upgrade status %d", resp.StatusCode)
	}
	read := func() {
		var h [2]byte
		io.ReadFull(br, h[:])
		io.CopyN(io.Discard, br, int64(h[1]&0x7f))
	}
	read()
	for _, m := range []string{"hello", `{"type":"subscribe","channel":"prices"}`, "close"} {
		f := []byte{0x81, 0x80 | byte(len(m)), 1, 2, 3, 4}
		for i := 0; i < len(m); i++ {
			f = append(f, m[i]^byte(i%4+1))
		}
		conn.Write(f)
		read()
	}
	time.Sleep(100 * time.Millisecond)
	return nil
}

// serveRawEcho answers each line with "ECHO <line>" (a non-HTTP protocol).
func serveRawEcho(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			io.WriteString(c, "RAW-READY\n")
			r := bufio.NewReader(c)
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				io.WriteString(c, "ECHO "+line)
			}
		}()
	}
}

// rawTCP opens a raw (non-HTTP) connection from the guest and exchanges a
// few lines, like a custom-protocol app would.
func (g *generator) rawTCP() error {
	ctx := context.Background()
	ips, err := g.guest.Resolver().LookupNetIP(ctx, "ip4", rawHost)
	if err != nil {
		return err
	}
	c, err := g.guest.DialTCP(ctx, netip.AddrPortFrom(ips[0], 7777))
	if err != nil {
		return err
	}
	defer c.Close()
	r := bufio.NewReader(c)
	if _, err := r.ReadString('\n'); err != nil {
		return err
	}
	for _, m := range []string{"hello raw\n", "{\"op\":\"ping\"}\n"} {
		if _, err := io.WriteString(c, m); err != nil {
			return err
		}
		if _, err := r.ReadString('\n'); err != nil {
			return err
		}
	}
	return nil
}

// pinnedCall simulates an app with certificate pinning (it trusts only the
// real server CA): the first attempt fails, the retry passes through.
func (g *generator) pinnedCall() error {
	pool := x509.NewCertPool()
	pool.AddCert(g.pinned)
	for i := 0; i < 2; i++ {
		c := g.guest.HTTPClient(pool, false)
		if resp, err := c.Get("https://" + apiHost + "/test/json"); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		c.CloseIdleConnections()
	}
	return nil
}

func networkCA() (*network.CA, error) { return network.NewCA(time.Now()) }
