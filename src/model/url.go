package model

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// URLParts are the normalized components of an absolute URL.
type URLParts struct {
	Scheme string
	Host   string // lower-cased, IPv6 without brackets
	Port   int    // explicit or scheme default
	Path   string // escaped path, "/" when empty
	Query  string // raw query without '?'
}

// ParseURL parses an absolute http(s)/ws(s) URL into normalized parts.
func ParseURL(raw string) (URLParts, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return URLParts{}, fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "ws", "wss":
	default:
		return URLParts{}, fmt.Errorf("unsupported URL scheme %q (expected http, https, ws or wss)", u.Scheme)
	}
	if u.Host == "" {
		return URLParts{}, fmt.Errorf("URL %q has no host", raw)
	}
	port := DefaultPort(scheme)
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return URLParts{}, fmt.Errorf("URL %q has an invalid port", raw)
		}
		port = n
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return URLParts{
		Scheme: scheme,
		Host:   strings.ToLower(u.Hostname()),
		Port:   port,
		Path:   path,
		Query:  u.RawQuery,
	}, nil
}

// QueryParam is one decoded query-string parameter (order preserved).
type QueryParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ParseQuery decodes a raw query string preserving parameter order and
// duplicates. Undecodable components are returned verbatim.
func ParseQuery(raw string) []QueryParam {
	var out []QueryParam
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' || r == ';' }) {
		k, v, _ := strings.Cut(part, "=")
		out = append(out, QueryParam{Name: unescape(k), Value: unescape(v)})
	}
	return out
}

func unescape(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return d
	}
	return s
}

// NewID returns a 26-char, lexicographically time-sortable unique identifier
// (ULID layout: 48-bit millisecond time + 80 bits of randomness, Crockford base32).
func NewID() string { return newIDAt(time.Now()) }

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newIDAt(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	if _, err := rand.Read(b[6:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	// Encode 128 bits as 26 base32 chars (first char carries 3 bits).
	var out [26]byte
	var acc uint64
	bits := 0
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && idx >= 0 {
			out[idx] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			idx--
		}
	}
	for idx >= 0 {
		out[idx] = crockford[acc&31]
		acc >>= 5
		idx--
	}
	return string(out[:])
}
