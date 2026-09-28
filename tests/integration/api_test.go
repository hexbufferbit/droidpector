package integration

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/droidpector/apkinspector/src/core"
	"github.com/droidpector/apkinspector/src/ipc"
	"github.com/droidpector/apkinspector/src/model"
	"github.com/droidpector/apkinspector/src/network"
	"github.com/droidpector/apkinspector/src/network/guestsim"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/query"
	"github.com/droidpector/apkinspector/src/testserver"
)

type apiEnv struct {
	t     *testing.T
	app   *core.App
	srv   *ipc.Server
	guest *guestsim.Guest
	roots *x509.CertPool
	sid   string
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	ts, err := testserver.Start("", "", []string{httpsHost})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ts.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, ts.CAPEM, 0o600)
	paths := platform.NewPaths(dir, filepath.Join(dir, "no-runtime"), dir)
	cfg := platform.DefaultConfig()
	cfg.HostMappings = []string{httpsHost + "=" + ts.HTTPSAddr, httpHost + "=" + ts.HTTPAddr}
	cfg.ExtraRootsPEM = ca
	logs, err := platform.OpenLoggers(paths.LogDir, platform.LevelTrace, false)
	if err != nil {
		t.Fatal(err)
	}
	app, err := core.NewApp("test", paths, cfg, logs)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := network.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	app.Gateway.SetCA(cert)
	vmSide, gwSide := net.Pipe()
	go app.Gateway.Stack().ServeQEMU(context.Background(), gwSide)
	a := app.Gateway.Stack().Addressing()
	guest, err := guestsim.New(vmSide, a.Guest, a.Gateway, a.DNS)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := app.Sessions.Start(context.Background(), "simulated")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ipc.NewServer(app, nil, 0, logs.App)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() {
		s.Shutdown(context.Background())
		guest.Close()
		app.Close()
		logs.Close()
	})
	roots := x509.NewCertPool()
	roots.AddCert(cert.Certificate())
	return &apiEnv{t: t, app: app, srv: s, guest: guest, roots: roots, sid: sess.ID}
}

func (e *apiEnv) do(method, path string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.BaseURL()+path, body)
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func (e *apiEnv) waitRows(filter string, n int) []model.Summary {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, b := e.do("GET", "/api/sessions/"+e.sid+"/events?filter="+filter, nil, nil)
		var p query.Page
		json.Unmarshal(b, &p)
		done := 0
		for _, r := range p.Rows {
			if r.State != model.StatePending {
				done++
			}
		}
		if done >= n {
			return p.Rows
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("rows for %q not found", filter)
	return nil
}

func TestAPISecurity(t *testing.T) {
	e := newAPIEnv(t)
	resp, err := http.Get(e.srv.BaseURL() + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if r, _ := e.do("GET", "/api/status", nil, map[string]string{"Host": "attacker.example:80"}); r.StatusCode != 403 {
		t.Fatalf("DNS-rebinding Host accepted: %d", r.StatusCode)
	}
	if r, _ := e.do("POST", "/api/sandbox/stop", nil, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != 403 {
		t.Fatalf("cross-origin request accepted: %d", r.StatusCode)
	}
	// Cookie bootstrap: /auth sets an HttpOnly SameSite=Strict cookie.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := noRedirect.Get(e.srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	c := r.Cookies()
	if r.StatusCode != 302 || len(c) != 1 || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("auth cookie: %d %+v", r.StatusCode, c)
	}
	r, _ = noRedirect.Get(e.srv.BaseURL() + "/auth?token=wrong")
	if r.StatusCode != 401 {
		t.Fatalf("wrong token accepted: %d", r.StatusCode)
	}
}

func TestAPINetworkInspectorFlow(t *testing.T) {
	e := newAPIEnv(t)
	c := e.guest.HTTPClient(e.roots, true)
	req, _ := http.NewRequest("POST", "https://"+httpsHost+"/test/post?lang=fa", strings.NewReader(`{"u":"a"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, _ = c.Get("https://" + httpsHost + "/test/gzip")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows := e.waitRows("method:POST", 1)
	id := rows[0].ID

	// Invalid filter → 400 with a readable message.
	r, b := e.do("GET", "/api/sessions/"+e.sid+"/events?filter=(method:GET", nil, nil)
	if r.StatusCode != 400 || !strings.Contains(string(b), "bad_filter") {
		t.Fatalf("bad filter: %d %s", r.StatusCode, b)
	}

	// Detail with derived views.
	_, b = e.do("GET", "/api/events/"+id, nil, nil)
	var d struct {
		URL          string             `json:"url"`
		QueryParams  []model.QueryParam `json:"queryParams"`
		ResponseKind string             `json:"responseKind"`
		Replayable   bool               `json:"replayable"`
		Status       int                `json:"status"`
	}
	json.Unmarshal(b, &d)
	if d.URL != "https://"+httpsHost+"/test/post?lang=fa" || len(d.QueryParams) != 1 || d.ResponseKind != "json" || !d.Replayable || d.Status != 201 {
		t.Fatalf("detail: %s", b)
	}

	// Bodies are served with a sandboxing CSP; gzip bodies are decoded on request.
	r, b = e.do("GET", "/api/events/"+id+"/body/request", nil, nil)
	if string(b) != `{"u":"a"}` || !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") || r.Header.Get("X-Body-Kind") != "json" {
		t.Fatalf("request body: %q %v", b, r.Header)
	}
	gz := e.waitRows("path:/test/gzip", 1)
	_, b = e.do("GET", "/api/events/"+gz[0].ID+"/body/response?decode=1", nil, nil)
	if !bytes.Equal(b, testserver.JSONBody) {
		t.Fatalf("decoded body: %q", b)
	}

	// Copy as cURL.
	_, b = e.do("GET", "/api/events/"+id+"/code/curl", nil, nil)
	if !strings.HasPrefix(string(b), "curl 'https://"+httpsHost+"/test/post?lang=fa'") || !strings.Contains(string(b), `--data-raw '{"u":"a"}'`) {
		t.Fatalf("curl: %s", b)
	}

	// Replay → a new event; the original is unchanged.
	r, b = e.do("POST", "/api/events/"+id+"/replay", nil, nil)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"initiator":"replay"`) {
		t.Fatalf("replay: %d %s", r.StatusCode, b)
	}
	e.waitRows("is:replay", 1)

	// HAR export of the session and of a single request.
	r, b = e.do("GET", "/api/sessions/"+e.sid+"/har", nil, nil)
	var har struct {
		Log struct{ Entries []json.RawMessage }
	}
	if json.Unmarshal(b, &har) != nil || len(har.Log.Entries) < 3 || !strings.Contains(r.Header.Get("Content-Disposition"), ".har") {
		t.Fatalf("har: %d entries", len(har.Log.Entries))
	}
	if r, _ := e.do("GET", "/api/events/"+id+"/har", nil, nil); r.StatusCode != 200 {
		t.Fatal("single HAR")
	}

	// Not found is 404, not 500.
	if r, _ := e.do("GET", "/api/events/doesnotexist", nil, nil); r.StatusCode != 404 {
		t.Fatalf("missing event: %d", r.StatusCode)
	}
	// DNS events cannot be exported as cURL: clear 409 message.
	dns := e.waitRows("kind:dns", 1)
	r, b = e.do("GET", "/api/events/"+dns[0].ID+"/code/curl", nil, nil)
	if r.StatusCode != 409 || !strings.Contains(string(b), "cannot be exported") {
		t.Fatalf("dns curl: %d %s", r.StatusCode, b)
	}
}

func TestAPIWebSocketPushesChanges(t *testing.T) {
	e := newAPIEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(e.srv.BaseURL(), "http", "ws", 1)+"/api/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + e.srv.Token()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	go func() {
		c := e.guest.HTTPClient(e.roots, false)
		if r, err := c.Get("http://" + httpHost + "/test/text"); err == nil {
			r.Body.Close()
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("no events message: %v", err)
		}
		var m core.Message
		json.Unmarshal(data, &m)
		if m.Type == "events" && m.SessionID == e.sid && m.Stats != nil && m.Stats.Requests > 0 {
			return
		}
	}
}

func TestAPIUploadAPKAndSandboxErrors(t *testing.T) {
	e := newAPIEnv(t)
	r, b := e.do("POST", "/api/apks", strings.NewReader("definitely not a zip"), map[string]string{"X-File-Name": "broken%20app.apk"})
	if r.StatusCode != 201 {
		t.Fatalf("upload: %d %s", r.StatusCode, b)
	}
	var entry core.APKEntry
	json.Unmarshal(b, &entry)
	if entry.Valid || entry.FileName != "broken app.apk" || len(entry.Problems) == 0 {
		t.Fatalf("entry: %s", b)
	}
	if r, b := e.do("POST", "/api/apks", strings.NewReader("x"), map[string]string{"X-File-Name": "virus.exe"}); r.StatusCode != 400 {
		t.Fatalf("non-apk: %d %s", r.StatusCode, b)
	}
	// App operations without a running sandbox explain what to do.
	r, b = e.do("POST", "/api/apps/com.x/launch", nil, nil)
	if r.StatusCode != 409 || !strings.Contains(string(b), "not running") {
		t.Fatalf("launch without sandbox: %d %s", r.StatusCode, b)
	}
	// Starting without an installed runtime reports a clear error in the status.
	e.do("POST", "/api/sandbox/start", strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, b = e.do("GET", "/api/status", nil, nil)
		var st core.Status
		json.Unmarshal(b, &st)
		if st.State == core.StateError {
			if st.Error == nil || st.Error.Title == "" || strings.Contains(strings.ToLower(st.Error.Title), "unknown") {
				t.Fatalf("unhelpful error: %+v", st.Error)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no error state: %s", b)
}
