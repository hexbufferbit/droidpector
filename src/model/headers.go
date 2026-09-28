package model

import (
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
)

// Header is a single header field. Order and original case are preserved.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Headers is an ordered header list (multiple fields with the same name allowed).
type Headers []Header

// FromHTTP converts a net/http header map into a deterministic, name-sorted
// list (the order DevTools shows by default). Pseudo-order within one name is kept.
func FromHTTP(h http.Header) Headers {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make(Headers, 0, len(h))
	for _, k := range names {
		for _, v := range h[k] {
			out = append(out, Header{Name: k, Value: v})
		}
	}
	return out
}

// HTTP converts back into a net/http header map.
func (hs Headers) HTTP() http.Header {
	h := make(http.Header, len(hs))
	for _, f := range hs {
		h[http.CanonicalHeaderKey(f.Name)] = append(h[http.CanonicalHeaderKey(f.Name)], f.Value)
	}
	return h
}

// Get returns the first value for name (case-insensitive) or "".
func (hs Headers) Get(name string) string {
	for _, f := range hs {
		if strings.EqualFold(f.Name, name) {
			return f.Value
		}
	}
	return ""
}

// Values returns every value for name (case-insensitive).
func (hs Headers) Values(name string) []string {
	var out []string
	for _, f := range hs {
		if strings.EqualFold(f.Name, name) {
			out = append(out, f.Value)
		}
	}
	return out
}

// Size approximates the serialized size of the header block ("Name: Value\r\n").
func (hs Headers) Size() int64 {
	var n int64
	for _, f := range hs {
		n += int64(len(f.Name) + len(f.Value) + 4)
	}
	return n
}

// ParseHeaderLines parses raw "Name: value" lines (e.g. a pasted header block).
// Lines without a colon and HTTP/2 pseudo headers are skipped; obsolete line
// folding (leading whitespace) is joined onto the previous value.
func ParseHeaderLines(block string) Headers {
	var out Headers
	for _, line := range strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && len(out) > 0 {
			out[len(out)-1].Value += " " + strings.TrimSpace(line)
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		out = append(out, Header{Name: name, Value: strings.TrimSpace(line[i+1:])})
	}
	return out
}

// MediaType returns the lower-cased media type of a Content-Type value
// ("application/json; charset=utf-8" → "application/json").
func MediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	}
	return strings.ToLower(mt)
}

// IsTextual reports whether a media type is human-readable text.
func IsTextual(mediaType string) bool {
	mt := MediaType(mediaType)
	switch {
	case strings.HasPrefix(mt, "text/"):
		return true
	case mt == "application/json", mt == "application/xml", mt == "application/javascript",
		mt == "application/x-www-form-urlencoded", mt == "application/graphql",
		mt == "application/ecmascript", mt == "application/x-javascript",
		mt == "image/svg+xml":
		return true
	case strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return true
	}
	return false
}

// Categorize assigns a quick-filter category from the response media type, and
// falls back to the URL path extension when the type is missing or generic.
func Categorize(kind Kind, mediaType, urlPath string) Category {
	switch kind {
	case KindWebSocket:
		return CatWebSocket
	case KindDNS:
		return CatDNS
	case KindTCP, KindUDP, KindTLS:
		return CatOther
	}
	mt := MediaType(mediaType)
	switch {
	case mt == "text/html", mt == "application/xhtml+xml":
		return CatDocument
	case strings.HasPrefix(mt, "image/"):
		return CatImage
	case strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"),
		mt == "application/vnd.apple.mpegurl", mt == "application/x-mpegurl", mt == "application/dash+xml":
		return CatMedia
	case strings.HasPrefix(mt, "font/"), mt == "application/font-woff", mt == "application/x-font-ttf":
		return CatFont
	case mt == "text/css":
		return CatStyle
	case mt == "application/javascript", mt == "text/javascript", mt == "application/x-javascript", mt == "application/ecmascript":
		return CatScript
	case mt == "application/json", strings.HasSuffix(mt, "+json"), mt == "application/xml", mt == "text/xml",
		strings.HasSuffix(mt, "+xml"), strings.HasPrefix(mt, "application/grpc"), mt == "application/x-protobuf",
		mt == "application/protobuf", mt == "application/x-www-form-urlencoded", mt == "text/plain",
		mt == "application/graphql", mt == "application/x-ndjson", mt == "text/event-stream":
		return CatAPI
	}
	switch strings.ToLower(path.Ext(urlPath)) {
	case ".html", ".htm":
		return CatDocument
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico", ".bmp", ".avif":
		return CatImage
	case ".mp4", ".webm", ".mp3", ".m4a", ".aac", ".ogg", ".wav", ".m3u8", ".ts", ".mpd":
		return CatMedia
	case ".woff", ".woff2", ".ttf", ".otf":
		return CatFont
	case ".css":
		return CatStyle
	case ".js", ".mjs":
		return CatScript
	case ".json", ".xml":
		return CatAPI
	}
	if mt == "" && kind == KindHTTP {
		// Bodiless API calls (204, redirects to APIs, …) are most useful under XHR/API.
		return CatAPI
	}
	return CatOther
}
