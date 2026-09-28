package query

import (
	"errors"
	"testing"

	"github.com/droidpector/apkinspector/src/model"
)

var corpus = []model.Summary{
	{ID: "login", Kind: model.KindHTTP, Category: model.CatAPI, State: model.StateComplete, Method: "POST", Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1/login", Status: 200, MIME: "application/json", ResponseSize: 4210, DurationMs: 183},
	{ID: "user", Kind: model.KindHTTP, Category: model.CatAPI, State: model.StateComplete, Method: "GET", Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1/user", Query: "id=7", Status: 200, MIME: "application/json", ResponseSize: 900, DurationMs: 40},
	{ID: "token", Kind: model.KindHTTP, Category: model.CatAPI, State: model.StateComplete, Method: "POST", Scheme: "https", Host: "auth.example.com", Port: 443, Path: "/token", Status: 401, MIME: "application/json", ResponseSize: 80, DurationMs: 95},
	{ID: "img", Kind: model.KindHTTP, Category: model.CatImage, State: model.StateComplete, Method: "GET", Scheme: "https", Host: "cdn.example.com", Port: 443, Path: "/a.png", Status: 200, MIME: "image/png", ResponseSize: 200_000, DurationMs: 1500},
	{ID: "plain", Kind: model.KindHTTP, Category: model.CatDocument, State: model.StatePending, Method: "GET", Scheme: "http", Host: "other.org", Port: 8080, Path: "/index.html", MIME: "text/html"},
	{ID: "dns", Kind: model.KindDNS, Category: model.CatDNS, State: model.StateComplete, Host: "api.example.com", Protocol: "DNS"},
	{ID: "pinned", Kind: model.KindTLS, Category: model.CatOther, State: model.StateComplete, Scheme: "https", Host: "pinned.example.net", Port: 443, Encrypted: true},
	{ID: "replay", Kind: model.KindHTTP, Category: model.CatAPI, State: model.StateError, Initiator: model.InitiatorReplay, Method: "DELETE", Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1/item/1", Error: "connection reset"},
	{ID: "ws", Kind: model.KindWebSocket, Category: model.CatWebSocket, State: model.StateComplete, Method: "GET", Scheme: "wss", Host: "rt.example.com", Port: 443, Path: "/socket", Status: 101},
}

func ids(t *testing.T, expr string) []string {
	t.Helper()
	m, err := Compile(expr)
	if err != nil {
		t.Fatalf("Compile(%q): %v", expr, err)
	}
	var out []string
	for i := range corpus {
		if m(&corpus[i]) {
			out = append(out, corpus[i].ID)
		}
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilterExpressions(t *testing.T) {
	cases := []struct {
		expr string
		want []string
	}{
		{"", []string{"login", "user", "token", "img", "plain", "dns", "pinned", "replay", "ws"}},
		{"host:api.example.com", []string{"login", "user", "dns", "replay"}},
		{"host:API.EXAMPLE.COM", []string{"login", "user", "dns", "replay"}},
		{"domain:*.example.com", []string{"login", "user", "token", "img", "dns", "replay", "ws"}},
		{`host contains "example.com"`, []string{"login", "user", "token", "img", "dns", "replay", "ws"}},
		{`host contains example`, []string{"login", "user", "token", "img", "dns", "pinned", "replay", "ws"}},
		{`host endswith example.net`, []string{"pinned"}},
		{`host startswith auth`, []string{"token"}},
		{"method:POST", []string{"login", "token"}},
		{"method:post status:401", []string{"token"}},
		{"status:401", []string{"token"}},
		{"status:2xx", []string{"login", "user", "img"}},
		{"status>=400", []string{"token"}},
		{"status:>=300", []string{"token"}},
		{"status!=200 kind:http", []string{"token", "plain", "replay"}},
		{"path:/v1/", []string{"login", "user", "replay"}},
		{"scheme:http", []string{"plain"}},
		{"content-type:json", []string{"login", "user", "token"}},
		{"mime:image", []string{"img"}},
		{"-host:api.example.com kind:http", []string{"token", "img", "plain"}},
		{"NOT method:GET kind:http", []string{"login", "token", "replay"}},
		{"(method:GET OR method:DELETE) host:api.example.com", []string{"user", "replay"}},
		{"method:GET || method:DELETE", []string{"user", "img", "plain", "replay", "ws"}},
		{"login", []string{"login"}},
		{"EXAMPLE.com/v1", []string{"login", "user", "replay"}},
		{`"id=7"`, []string{"user"}},
		{"size>10k", []string{"img"}},
		{"larger-than:1k", []string{"login", "img"}},
		{"duration>=1s", []string{"img"}},
		{"duration<100 duration>0", []string{"user", "token"}},
		{"is:error", []string{"token", "replay"}},
		{"is:pending", []string{"plain"}},
		{"is:encrypted", []string{"pinned"}},
		{"is:replay", []string{"replay"}},
		{"type:websocket", []string{"ws"}},
		{"kind:dns", []string{"dns"}},
		{"port:8080", []string{"plain"}},
		{`path:/^\/v1\/(login|user)$/`, []string{"login", "user"}},
		{`host matches "^(api|auth)\."`, []string{"login", "user", "token", "dns", "replay"}},
		{"http://other.org:8080/index", []string{"plain"}},
		{"unknownfield:xyz", nil},
		{"error:reset", []string{"replay"}},
		{"query:id=", []string{"user"}},
		{"url:https://cdn", []string{"img"}},
	}
	for _, c := range cases {
		if got := ids(t, c.expr); !eq(got, c.want) {
			t.Errorf("filter %q\n got  %v\n want %v", c.expr, got, c.want)
		}
	}
}

func TestFilterErrors(t *testing.T) {
	for _, expr := range []string{
		`host:"unterminated`, "(method:GET", "method:GET OR", "status>abc", "is:banana",
		"host:", "size>=10q", "host contains", `path:/([/`, "status>4xx", "method>5", ")",
	} {
		_, err := Compile(expr)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("Compile(%q) error = %v, want *ParseError", expr, err)
		}
	}
}

func TestQuickFilters(t *testing.T) {
	count := func(name string) int {
		m, err := QuickFilter(name)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for i := range corpus {
			if m(&corpus[i]) {
				n++
			}
		}
		return n
	}
	if count("") != len(corpus) || count("all") != len(corpus) {
		t.Fatal("all")
	}
	if count("api") != 4 || count("image") != 1 || count("document") != 1 || count("websocket") != 1 || count("dns") != 1 || count("other") != 1 || count("media") != 0 {
		t.Fatal("category counts wrong")
	}
	if _, err := QuickFilter("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func FuzzCompile(f *testing.F) {
	for _, s := range []string{"host:a", `x contains "y"`, "(a OR -b) status:4xx", "size>1.5mb", `path:/[a/`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		m, err := Compile(expr)
		if err == nil {
			for i := range corpus {
				m(&corpus[i])
			}
		}
	})
}
