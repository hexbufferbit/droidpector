package export

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"iter"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/model"
)

// memBodies is a trivial in-memory BodySource keyed by hash.
type memBodies map[string][]byte

func (m memBodies) ReadBody(ref *model.BodyRef) ([]byte, error) {
	b, ok := m[ref.Hash]
	if !ok {
		return nil, errors.New("missing blob")
	}
	return b, nil
}

func loginEvent() *model.Event {
	return &model.Event{
		ID: "E1", Kind: model.KindHTTP, State: model.StateComplete, Protocol: "HTTP/1.1",
		Method: "POST", Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1/login", Query: "lang=en&x=a%20b",
		Status: 200, StartedAt: time.Date(2026, 9, 28, 19, 20, 0, 0, time.UTC), DurationMs: 183,
		RequestHeaders: model.Headers{
			{Name: "Host", Value: "api.example.com"}, {Name: "Content-Type", Value: "application/json"}, {Name: "Authorization", Value: "Bearer abc"},
			{Name: "Content-Length", Value: "39"}, {Name: "Cookie", Value: "sid=1; theme=dark"}, {Name: "X-Quote", Value: "it's"},
		},
		ResponseHeaders: model.Headers{{Name: "Content-Type", Value: "application/json"}, {Name: "Content-Encoding", Value: "gzip"}, {Name: "Set-Cookie", Value: "sid=2; Path=/; HttpOnly; Secure"}},
		RequestBody:     &model.BodyRef{Hash: "req", Size: 39, Stored: 39},
		ResponseBody:    &model.BodyRef{Hash: "resp", Encoding: "gzip"},
		RequestSize:     39,
		Timing:          &model.Timing{Blocked: -1, DNS: 3, Connect: 10, TLS: 20, Send: 1, Wait: 140, Receive: 9},
		Conn:            &model.Conn{ID: "c1", RemoteAddr: "93.184.216.34:443"},
	}
}

func TestCurlGeneratesFaithfulCommand(t *testing.T) {
	// Given a POST request with URL, headers and a JSON body
	e := loginEvent()
	body := []byte(`{"username":"amir","password":"p'ss"}`)
	req, err := RequestFromEvent(e, body)
	if err != nil {
		t.Fatal(err)
	}
	// When generating cURL
	out, err := Curl{}.Generate(req)
	if err != nil {
		t.Fatal(err)
	}
	// Then method, URL, headers and body are present and correctly quoted
	want := `curl 'https://api.example.com/v1/login?lang=en&x=a%20b' \
  -X 'POST' \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer abc' \
  -H 'Cookie: sid=1; theme=dark' \
  -H 'X-Quote: it'\''s' \
  --data-raw '{"username":"amir","password":"p'\''ss"}'`
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestCurlVariants(t *testing.T) {
	get := Request{Method: "GET", URL: "http://h:8080/a?q[]=1", Headers: model.Headers{{Name: "Host", Value: "other"}, {Name: "Accept-Encoding", Value: "gzip"}, {Name: ":authority", Value: "x"}}}
	out, _ := Curl{}.Generate(get)
	for _, s := range []string{"curl 'http://h:8080/a?q[]=1' --globoff", "-H 'Host: other'", "--compressed"} {
		if !strings.Contains(out, s) {
			t.Errorf("GET output missing %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "-X") || strings.Contains(out, ":authority") {
		t.Errorf("unexpected content:\n%s", out)
	}
	head, _ := Curl{}.Generate(Request{Method: "HEAD", URL: "https://x/"})
	if !strings.Contains(head, "--head") {
		t.Error(head)
	}
	bin, _ := Curl{}.Generate(Request{Method: "PUT", URL: "https://x/u", Body: []byte{0, 1, 'a', '\'', 0xff, '\n'}})
	if !strings.Contains(bin, `--data-binary $'\x00\x01a\'\xff\n'`) {
		t.Errorf("binary body not ANSI-C quoted:\n%s", bin)
	}
	trunc, _ := Curl{}.Generate(Request{Method: "POST", URL: "https://x/", Body: []byte("a"), BodyTruncated: true})
	if !strings.HasPrefix(trunc, "# WARNING") {
		t.Error("truncation warning missing")
	}
	if _, err := (Curl{}).Generate(Request{}); err == nil {
		t.Error("empty URL must fail")
	}
	if _, err := RequestFromEvent(&model.Event{Kind: model.KindDNS}, nil); err == nil {
		t.Error("DNS event must not be exportable")
	}
	if _, err := RequestFromEvent(&model.Event{Kind: model.KindHTTP, Encrypted: true}, nil); err == nil {
		t.Error("encrypted event must not be exportable")
	}
}

// TestCurlRoundTripThroughShell verifies the quoting by letting a real POSIX
// shell parse the command and comparing the resulting argv.
func TestCurlRoundTripThroughShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	body := `{"a":"it's \"q\" $HOME ` + "`x`" + `"}`
	out, _ := Curl{}.Generate(Request{Method: "POST", URL: "https://x/p?a=$b", Headers: model.Headers{{Name: "X-A", Value: `v'"$`}}, Body: []byte(body)})
	script := "printargs() { for a in \"$@\"; do printf '%s\\0' \"$a\"; done; }\n" + strings.Replace(out, "curl ", "printargs ", 1)
	res, err := exec.Command(sh, "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(res), "\x00"), "\x00")
	want := []string{"https://x/p?a=$b", "-X", "POST", "-H", `X-A: v'"$`, "--data-raw", body}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("argv mismatch\n got %q\nwant %q", args, want)
	}
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func seq(events ...*model.Event) iter.Seq2[*model.Event, error] {
	return func(yield func(*model.Event, error) bool) {
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
	}
}

func TestHARExport(t *testing.T) {
	respJSON := []byte(`{"token":"t"}`)
	compressed := gz(respJSON)
	login := loginEvent()
	login.ResponseBody.Size = int64(len(compressed))
	login.ResponseBody.Stored = int64(len(compressed))
	img := &model.Event{
		ID: "E2", Kind: model.KindHTTP, Method: "GET", Scheme: "https", Host: "cdn.example.com", Port: 443, Path: "/a.png", Status: 200,
		ResponseHeaders: model.Headers{{Name: "Content-Type", Value: "image/png"}}, ResponseBody: &model.BodyRef{Hash: "png", Size: 4, Stored: 4},
		Initiator: model.InitiatorReplay, ReplayOf: "E0",
	}
	ws := &model.Event{
		ID: "E3", Kind: model.KindWebSocket, Method: "GET", Scheme: "wss", Host: "rt.example.com", Port: 443, Path: "/ws", Status: 101,
		Frames: []model.WSFrame{{Outgoing: true, Opcode: 1, Data: []byte("hi"), Time: time.Unix(10, 500_000_000)}, {Opcode: 2, Data: []byte{1, 2}}},
	}
	dns := &model.Event{ID: "E4", Kind: model.KindDNS, Host: "api.example.com"}
	pinned := &model.Event{ID: "E5", Kind: model.KindTLS, Encrypted: true, Host: "p.example"}
	bodies := memBodies{"req": []byte(`{"username":"amir","password":"p'ss"}`), "resp": compressed, "png": {0x89, 'P', 'N', 'G'}}

	var buf bytes.Buffer
	n, err := WriteHAR(&buf, HAROptions{Creator: "droidpector", Version: "1.0.0", IncludeBodies: true}, seq(login, img, ws, dns, pinned), bodies)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("wrote %d entries, want 3 (DNS and encrypted TLS are skipped)", n)
	}
	var doc struct {
		Log struct {
			Version string
			Creator harCreator
			Entries []HAREntry
		}
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if doc.Log.Version != "1.2" || doc.Log.Creator.Name != "droidpector" {
		t.Fatalf("bad log header: %+v", doc.Log)
	}
	e := doc.Log.Entries[0]
	if e.Request.URL != "https://api.example.com/v1/login?lang=en&x=a%20b" || e.Request.Method != "POST" {
		t.Errorf("request: %+v", e.Request)
	}
	if len(e.Request.QueryString) != 2 || e.Request.QueryString[1].Value != "a b" {
		t.Errorf("queryString: %+v", e.Request.QueryString)
	}
	if len(e.Request.Cookies) != 2 || e.Response.Cookies[0].Name != "sid" || !e.Response.Cookies[0].HTTPOnly {
		t.Errorf("cookies: %+v / %+v", e.Request.Cookies, e.Response.Cookies)
	}
	if e.Request.PostData == nil || e.Request.PostData.Text != string(bodies["req"]) || e.Request.PostData.MimeType != "application/json" {
		t.Errorf("postData: %+v", e.Request.PostData)
	}
	c := e.Response.Content
	if c.Text != string(respJSON) || c.Size != int64(len(respJSON)) || c.Compression != int64(len(compressed)-len(respJSON)) || c.Encoding != "" {
		t.Errorf("content: %+v", c)
	}
	if e.Timings.Connect != 30 || e.Timings.SSL != 20 || e.Timings.Wait != 140 || e.Time != 3+30+1+140+9 {
		t.Errorf("timings: %+v time=%v", e.Timings, e.Time)
	}
	if e.ServerIPAddress != "93.184.216.34" || e.StartedDateTime != "2026-09-28T19:20:00Z" {
		t.Errorf("entry meta: %s %s", e.ServerIPAddress, e.StartedDateTime)
	}
	if e.Response.StatusText != "OK" {
		t.Errorf("statusText %q", e.Response.StatusText)
	}
	p := doc.Log.Entries[1].Response.Content
	if p.Encoding != "base64" || p.Text != base64.StdEncoding.EncodeToString(bodies["png"]) {
		t.Errorf("binary content: %+v", p)
	}
	if !strings.Contains(doc.Log.Entries[1].Comment, "E0") {
		t.Error("replay comment missing")
	}
	w := doc.Log.Entries[2]
	if w.ResourceType != "websocket" || len(w.WSMessages) != 2 || w.WSMessages[0].Type != "send" || w.WSMessages[0].Time != 10.5 || w.WSMessages[1].Data != "AQI=" {
		t.Errorf("websocket: %+v", w.WSMessages)
	}
}

func TestHARMissingBodyDoesNotFail(t *testing.T) {
	e := loginEvent()
	var buf bytes.Buffer
	if _, err := WriteHAR(&buf, HAROptions{IncludeBodies: true}, seq(e), memBodies{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "response body unavailable") {
		t.Error("expected explanatory comment")
	}
}

func TestHAREmptyAndIteratorError(t *testing.T) {
	var buf bytes.Buffer
	if n, err := WriteHAR(&buf, HAROptions{}, seq(), nil); err != nil || n != 0 || !json.Valid(buf.Bytes()) {
		t.Fatalf("empty HAR invalid: %v %s", err, buf.String())
	}
	failing := func(yield func(*model.Event, error) bool) { yield(nil, errors.New("db gone")) }
	if _, err := WriteHAR(&buf, HAROptions{}, failing, nil); err == nil {
		t.Fatal("iterator error must propagate")
	}
}

func TestGeneratorsRegistry(t *testing.T) {
	g, ok := GeneratorByID("curl")
	if !ok || g.Label() != "Copy as cURL" {
		t.Fatal("curl generator missing")
	}
	if _, ok := GeneratorByID("cobol"); ok {
		t.Fatal("unexpected generator")
	}
}

func TestHeadersText(t *testing.T) {
	if got := HeadersText(model.Headers{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}); got != "A: 1\nB: 2\n" {
		t.Fatal(got)
	}
}
