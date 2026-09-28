package model

import (
	"net/http"
	"sort"
	"testing"
	"time"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want URLParts
	}{
		{"https://API.Example.com/v1/login?x=1", URLParts{"https", "api.example.com", 443, "/v1/login", "x=1"}},
		{"http://example.com", URLParts{"http", "example.com", 80, "/", ""}},
		{"http://example.com:8080/a%20b", URLParts{"http", "example.com", 8080, "/a%20b", ""}},
		{"wss://[::1]:9000/ws", URLParts{"wss", "::1", 9000, "/ws", ""}},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("ParseURL(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseURL(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"ftp://x/", "/relative", "http://", "http://h:99999/", "::"} {
		if _, err := ParseURL(bad); err == nil {
			t.Errorf("ParseURL(%q) succeeded, want error", bad)
		}
	}
}

func TestEventURLRoundTrip(t *testing.T) {
	e := Event{Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1/login", Query: "a=1&b=2"}
	if got := e.URL(); got != "https://api.example.com/v1/login?a=1&b=2" {
		t.Fatalf("URL() = %q", got)
	}
	e = Event{Scheme: "http", Host: "::1", Port: 8080}
	if got := e.URL(); got != "http://[::1]:8080/" {
		t.Fatalf("URL() = %q", got)
	}
	if (&Event{}).URL() != "" {
		t.Fatal("URL of host-less event must be empty")
	}
	for _, c := range []struct {
		e    Event
		want string
	}{
		{Event{Kind: KindTCP, Host: "149.154.167.51", Port: 443}, "tcp://149.154.167.51:443"},
		{Event{Kind: KindTLS, Scheme: "https", Host: "pinned.example", Port: 443}, "https://pinned.example"},
		{Event{Kind: KindTLS, Host: "1.2.3.4", Port: 8443}, "tls://1.2.3.4:8443"},
		{Event{Kind: KindUDP, Host: "time.android.com", Port: 123}, "udp://time.android.com:123"},
		{Event{Kind: KindDNS, Host: "api.example.com"}, "dns://api.example.com"},
	} {
		if got := c.e.URL(); got != c.want {
			t.Errorf("%s URL() = %q, want %q", c.e.Kind, got, c.want)
		}
	}
}

func TestParseQueryPreservesOrderAndDuplicates(t *testing.T) {
	got := ParseQuery("b=2&a=1&b=3&flag&enc=a%20b%26c&bad=%zz")
	want := []QueryParam{{"b", "2"}, {"a", "1"}, {"b", "3"}, {"flag", ""}, {"enc", "a b&c"}, {"bad", "%zz"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("param %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestHeaders(t *testing.T) {
	h := http.Header{"X-B": {"2"}, "Content-Type": {"application/json"}, "X-A": {"1", "11"}}
	hs := FromHTTP(h)
	names := []string{}
	for _, f := range hs {
		names = append(names, f.Name)
	}
	if !sort.StringsAreSorted(names) || len(hs) != 4 {
		t.Fatalf("FromHTTP not sorted/complete: %v", hs)
	}
	if hs.Get("content-type") != "application/json" {
		t.Fatal("case-insensitive Get failed")
	}
	if v := hs.Values("x-a"); len(v) != 2 || v[1] != "11" {
		t.Fatalf("Values = %v", v)
	}
	if back := hs.HTTP(); back.Get("X-A") != "1" || len(back["X-A"]) != 2 {
		t.Fatalf("HTTP() = %v", back)
	}
}

func TestParseHeaderLines(t *testing.T) {
	hs := ParseHeaderLines("Host: a.com\r\nX-Long: one\r\n  two\r\nbroken line\r\n:authority: x\r\nBad Name: v\r\nEmpty:\r\n")
	want := Headers{{"Host", "a.com"}, {"X-Long", "one two"}, {"Empty", ""}}
	if len(hs) != len(want) {
		t.Fatalf("got %v", hs)
	}
	for i := range want {
		if hs[i] != want[i] {
			t.Errorf("%d: %v want %v", i, hs[i], want[i])
		}
	}
}

func TestCategorize(t *testing.T) {
	cases := []struct {
		kind Kind
		mt   string
		path string
		want Category
	}{
		{KindHTTP, "application/json; charset=utf-8", "/x", CatAPI},
		{KindHTTP, "application/vnd.api+json", "/x", CatAPI},
		{KindHTTP, "text/html", "/", CatDocument},
		{KindHTTP, "image/png", "/a", CatImage},
		{KindHTTP, "video/mp4", "/a", CatMedia},
		{KindHTTP, "application/octet-stream", "/logo.webp", CatImage},
		{KindHTTP, "", "/v1/ping", CatAPI},
		{KindHTTP, "application/octet-stream", "/blob", CatOther},
		{KindHTTP, "font/woff2", "/f", CatFont},
		{KindWebSocket, "", "/ws", CatWebSocket},
		{KindDNS, "", "", CatDNS},
		{KindTLS, "", "", CatOther},
	}
	for _, c := range cases {
		if got := Categorize(c.kind, c.mt, c.path); got != c.want {
			t.Errorf("Categorize(%s,%q,%q) = %s, want %s", c.kind, c.mt, c.path, got, c.want)
		}
	}
}

func TestIsTextual(t *testing.T) {
	for _, mt := range []string{"text/plain", "application/json", "application/problem+json", "image/svg+xml", "application/xml; charset=x"} {
		if !IsTextual(mt) {
			t.Errorf("%s should be textual", mt)
		}
	}
	for _, mt := range []string{"image/png", "application/octet-stream", "application/x-protobuf", ""} {
		if IsTextual(mt) {
			t.Errorf("%s should not be textual", mt)
		}
	}
}

func TestNewIDSortableAndUnique(t *testing.T) {
	t0 := time.UnixMilli(1_700_000_000_000)
	a, b := newIDAt(t0), newIDAt(t0.Add(time.Millisecond))
	if len(a) != 26 || len(b) != 26 {
		t.Fatalf("bad length %q %q", a, b)
	}
	if !(a < b) {
		t.Fatalf("IDs not time-sortable: %s >= %s", a, b)
	}
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := NewID()
		if seen[id] {
			t.Fatal("duplicate id")
		}
		seen[id] = true
	}
}

func TestSessionDuration(t *testing.T) {
	start := time.Date(2026, 9, 28, 19, 20, 0, 0, time.UTC)
	s := Session{StartedAt: start}
	if d := s.Duration(start.Add(12 * time.Minute)); d != 12*time.Minute {
		t.Fatal(d)
	}
	end := start.Add(time.Minute)
	s.EndedAt = &end
	if d := s.Duration(start.Add(time.Hour)); d != time.Minute {
		t.Fatal(d)
	}
}
