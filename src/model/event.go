// Package model defines the capture-layer–independent domain types shared by the
// network engine, storage, query, export and IPC layers. It has no dependencies on
// other project packages.
package model

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Kind is the kind of network activity an Event describes.
type Kind string

const (
	KindHTTP      Kind = "http"
	KindWebSocket Kind = "websocket"
	KindDNS       Kind = "dns"
	KindTLS       Kind = "tls" // TLS connection whose payload could not be inspected
	KindTCP       Kind = "tcp" // non-HTTP TCP connection
	KindUDP       Kind = "udp"
)

// Category drives the UI quick filters (All, XHR/API, Documents, ...).
type Category string

const (
	CatAPI       Category = "api"
	CatDocument  Category = "document"
	CatImage     Category = "image"
	CatMedia     Category = "media"
	CatScript    Category = "script"
	CatStyle     Category = "style"
	CatFont      Category = "font"
	CatWebSocket Category = "websocket"
	CatDNS       Category = "dns"
	CatOther     Category = "other"
)

// State is the lifecycle state of an Event. Events are upserted by ID, so a
// request first appears as pending and is later completed or failed.
type State string

const (
	StatePending  State = "pending"
	StateComplete State = "complete"
	StateError    State = "error"
)

// Initiator tells whether traffic originated in the guest or from a replay.
type Initiator string

const (
	InitiatorGuest  Initiator = "guest"
	InitiatorReplay Initiator = "replay"
)

// BodyRef references a body stored in the blob store. Bodies are never stored
// inline in the events table.
type BodyRef struct {
	Hash      string `json:"hash"`                // sha256 hex of stored (as-transmitted) bytes
	Size      int64  `json:"size"`                // full size as transmitted on the wire
	Stored    int64  `json:"stored"`              // number of bytes actually stored
	Truncated bool   `json:"truncated,omitempty"` // true when Stored < Size (body limit)
	Encoding  string `json:"encoding,omitempty"`  // Content-Encoding of the stored bytes
	MIME      string `json:"mime,omitempty"`
}

// Timing is a HAR-compatible timing breakdown in milliseconds; -1 means n/a.
type Timing struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	TLS     float64 `json:"tls"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

// NewTiming returns a Timing with every phase marked not applicable.
func NewTiming() *Timing {
	return &Timing{Blocked: -1, DNS: -1, Connect: -1, TLS: -1, Send: -1, Wait: -1, Receive: -1}
}

// CertInfo summarises one certificate of a server chain.
type CertInfo struct {
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	DNSNames  []string  `json:"dnsNames,omitempty"`
	SHA256    string    `json:"sha256"`
}

// TLSInfo describes the TLS properties of a connection.
type TLSInfo struct {
	SNI               string     `json:"sni,omitempty"`
	Version           string     `json:"version,omitempty"`
	CipherSuite       string     `json:"cipherSuite,omitempty"`
	ALPN              string     `json:"alpn,omitempty"`
	ClientALPN        []string   `json:"clientAlpn,omitempty"`
	Intercepted       bool       `json:"intercepted"`
	PassthroughReason string     `json:"passthroughReason,omitempty"`
	ServerCerts       []CertInfo `json:"serverCerts,omitempty"`
}

// Conn identifies the guest-side and upstream endpoints of a flow.
type Conn struct {
	ID         string `json:"id"`
	ClientAddr string `json:"clientAddr"`           // guest ip:port
	ServerAddr string `json:"serverAddr"`           // original destination ip:port
	RemoteAddr string `json:"remoteAddr,omitempty"` // actual upstream peer
	Reused     bool   `json:"reused,omitempty"`
	BytesUp    int64  `json:"bytesUp"`
	BytesDown  int64  `json:"bytesDown"`
}

// DNSAnswer is a single resource record of a DNS response.
type DNSAnswer struct {
	Name string `json:"name"`
	Type string `json:"type"`
	TTL  uint32 `json:"ttl"`
	Data string `json:"data"`
}

// DNSInfo describes a DNS query/response pair.
type DNSInfo struct {
	Question string      `json:"question"`
	QType    string      `json:"qtype"`
	RCode    string      `json:"rcode"`
	Answers  []DNSAnswer `json:"answers,omitempty"`
}

// WSFrame is one captured WebSocket frame.
type WSFrame struct {
	Seq       int64     `json:"seq"`
	Time      time.Time `json:"time"`
	Outgoing  bool      `json:"outgoing"` // guest → server
	Opcode    int       `json:"opcode"`
	Length    int64     `json:"length"`
	Data      []byte    `json:"data,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}

// Event is the normalized representation of any captured network activity.
type Event struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"sessionId"`
	Seq        int64     `json:"seq"`
	Kind       Kind      `json:"kind"`
	Category   Category  `json:"category"`
	State      State     `json:"state"`
	Initiator  Initiator `json:"initiator"`
	ReplayOf   string    `json:"replayOf,omitempty"`
	StartedAt  time.Time `json:"timestamp"`
	DurationMs float64   `json:"durationMs"`
	Package    string    `json:"package,omitempty"`

	Protocol   string `json:"protocol,omitempty"` // HTTP/1.1, HTTP/2, DNS, TLS, TCP ...
	Method     string `json:"method,omitempty"`
	Scheme     string `json:"scheme,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Path       string `json:"path,omitempty"`
	Query      string `json:"query,omitempty"` // raw query without '?'
	Status     int    `json:"status,omitempty"`
	StatusText string `json:"statusText,omitempty"`
	MIME       string `json:"mime,omitempty"`

	RequestSize  int64 `json:"requestSize"`
	ResponseSize int64 `json:"responseSize"`

	RequestHeaders  Headers  `json:"requestHeaders,omitempty"`
	ResponseHeaders Headers  `json:"responseHeaders,omitempty"`
	RequestBody     *BodyRef `json:"requestBody,omitempty"`
	ResponseBody    *BodyRef `json:"responseBody,omitempty"`

	Timing *Timing   `json:"timing,omitempty"`
	TLS    *TLSInfo  `json:"tls,omitempty"`
	Conn   *Conn     `json:"conn,omitempty"`
	DNS    *DNSInfo  `json:"dns,omitempty"`
	Frames []WSFrame `json:"frames,omitempty"` // only populated on detail reads

	// Encrypted is true when the payload could not be inspected (pinning, MITM
	// disabled). Metadata (SNI, bytes, timing) is still available.
	Encrypted bool   `json:"encrypted,omitempty"`
	Error     string `json:"error,omitempty"`
}

// URL reconstructs the absolute URL of an HTTP/WebSocket event.
func (e *Event) URL() string {
	if e.Host == "" {
		return ""
	}
	switch e.Kind {
	case KindTCP, KindUDP, KindTLS:
		// Non-HTTP flows: an address in URL form (tcp://host:port), never a
		// scheme-less "//host/" that looks like a broken web URL.
		scheme := strings.ToLower(string(e.Kind))
		if e.Kind == KindTLS && e.Scheme == "https" {
			scheme = "https"
		}
		return scheme + "://" + HostPort(scheme, e.Host, e.Port)
	case KindDNS:
		return "dns://" + e.Host
	}
	u := url.URL{Scheme: e.Scheme, Host: HostPort(e.Scheme, e.Host, e.Port), Opaque: ""}
	path := e.Path
	if path == "" {
		path = "/"
	}
	s := u.String() + path
	if e.Query != "" {
		s += "?" + e.Query
	}
	return s
}

// DefaultPort returns the default port for a scheme, or 0 if unknown.
func DefaultPort(scheme string) int {
	switch strings.ToLower(scheme) {
	case "http", "ws":
		return 80
	case "https", "wss":
		return 443
	}
	return 0
}

// HostPort formats host[:port], omitting the port when it is the scheme default
// and bracketing IPv6 literals.
func HostPort(scheme, host string, port int) string {
	h := host
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	if port == 0 || port == DefaultPort(scheme) {
		return h
	}
	return h + ":" + strconv.Itoa(port)
}

// SplitHostPort splits "host[:port]" leniently; missing port yields def.
func SplitHostPort(hostport string, def int) (string, int) {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.Trim(hostport, "[]"), def
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return h, def
	}
	return h, n
}

// Summary is the compact row the Network list and filter engine work with.
type Summary struct {
	ID           string    `json:"id"`
	Seq          int64     `json:"seq"`
	Kind         Kind      `json:"kind"`
	Category     Category  `json:"category"`
	State        State     `json:"state"`
	Initiator    Initiator `json:"initiator"`
	StartedAt    time.Time `json:"timestamp"`
	DurationMs   float64   `json:"durationMs"`
	Package      string    `json:"package,omitempty"`
	Protocol     string    `json:"protocol,omitempty"`
	Method       string    `json:"method,omitempty"`
	Scheme       string    `json:"scheme,omitempty"`
	Host         string    `json:"host,omitempty"`
	Port         int       `json:"port,omitempty"`
	Path         string    `json:"path,omitempty"`
	Query        string    `json:"query,omitempty"`
	Status       int       `json:"status,omitempty"`
	MIME         string    `json:"mime,omitempty"`
	RequestSize  int64     `json:"requestSize"`
	ResponseSize int64     `json:"responseSize"`
	Encrypted    bool      `json:"encrypted,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// Summary projects an Event onto its list row.
func (e *Event) Summary() Summary {
	return Summary{
		ID: e.ID, Seq: e.Seq, Kind: e.Kind, Category: e.Category, State: e.State,
		Initiator: e.Initiator, StartedAt: e.StartedAt, DurationMs: e.DurationMs,
		Package: e.Package, Protocol: e.Protocol, Method: e.Method, Scheme: e.Scheme,
		Host: e.Host, Port: e.Port, Path: e.Path, Query: e.Query, Status: e.Status,
		MIME: e.MIME, RequestSize: e.RequestSize, ResponseSize: e.ResponseSize,
		Encrypted: e.Encrypted, Error: e.Error,
	}
}

// Session is one run of the sandbox.
type Session struct {
	ID        string     `json:"id"`
	Number    int64      `json:"number"`
	Name      string     `json:"name,omitempty"`
	APK       string     `json:"apk,omitempty"`     // file name of the APK under test
	Package   string     `json:"package,omitempty"` // its package name
	Runtime   string     `json:"runtime,omitempty"` // runtime profile (x86_64 / arm64)
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Saved     bool       `json:"saved"` // protected from retention cleanup
	Requests  int64      `json:"requests"`
	Domains   int64      `json:"domains"`
	Bytes     int64      `json:"bytes"`
}

// Duration returns the session's duration up to now for running sessions.
func (s *Session) Duration(now time.Time) time.Duration {
	end := now
	if s.EndedAt != nil {
		end = *s.EndedAt
	}
	if end.Before(s.StartedAt) {
		return 0
	}
	return end.Sub(s.StartedAt)
}
