// Package export converts captured events into external formats: request code
// snippets (cURL today; Python/JavaScript/Go can be added as Generators) and
// HTTP Archive (HAR 1.2).
package export

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/droidpector/apkinspector/src/model"
)

// Request is everything a code generator needs to reproduce a request.
type Request struct {
	Method        string
	URL           string
	Headers       model.Headers
	Body          []byte // as transmitted (may be content-encoded)
	BodyTruncated bool
}

// RequestFromEvent builds a Request from a captured event and its stored body.
func RequestFromEvent(e *model.Event, body []byte) (Request, error) {
	if e.Kind != model.KindHTTP && e.Kind != model.KindWebSocket {
		return Request{}, fmt.Errorf("%s events cannot be exported as a request", e.Kind)
	}
	if e.Encrypted || e.Method == "" {
		return Request{}, fmt.Errorf("the request payload of this connection was not inspected (encrypted traffic)")
	}
	return Request{
		Method:        e.Method,
		URL:           e.URL(),
		Headers:       e.RequestHeaders,
		Body:          body,
		BodyTruncated: e.RequestBody != nil && e.RequestBody.Truncated,
	}, nil
}

// Generator renders a request as source code / a command line.
type Generator interface {
	ID() string    // stable identifier, e.g. "curl"
	Label() string // menu label, e.g. "Copy as cURL"
	Generate(r Request) (string, error)
}

// Generators returns every registered generator in menu order.
// Add Python/JavaScript/Go generators here.
func Generators() []Generator { return []Generator{Curl{}} }

// GeneratorByID finds a generator.
func GeneratorByID(id string) (Generator, bool) {
	for _, g := range Generators() {
		if g.ID() == id {
			return g, true
		}
	}
	return nil, false
}

// skipped by all generators: computed by the client or connection specific.
var hopByHop = map[string]bool{
	"content-length": true, "connection": true, "keep-alive": true, "proxy-connection": true,
	"transfer-encoding": true, "te": true, "upgrade": true, "trailer": true,
}

// Curl generates a POSIX-shell (bash/zsh) curl command.
type Curl struct{}

func (Curl) ID() string    { return "curl" }
func (Curl) Label() string { return "Copy as cURL" }

// Generate renders the request. Headers, method, query and body are preserved;
// non-printable bodies use ANSI-C quoting so bytes survive verbatim.
func (Curl) Generate(r Request) (string, error) {
	if r.URL == "" {
		return "", fmt.Errorf("request has no URL")
	}
	var lines []string
	if r.BodyTruncated {
		lines = append(lines, "# WARNING: the captured request body was truncated by the capture size limit;\n# the command below sends only the captured prefix.")
	}
	first := "curl " + shellQuote(r.URL)
	if strings.ContainsAny(r.URL, "[]{}") {
		first += " --globoff"
	}
	args := []string{first}

	method := strings.ToUpper(r.Method)
	switch {
	case method == "HEAD":
		args = append(args, "--head")
	case method != "" && method != "GET" || (method == "GET" && len(r.Body) > 0):
		args = append(args, "-X "+shellQuote(method))
	}

	compressed := false
	hostFromURL := ""
	if p, err := model.ParseURL(r.URL); err == nil {
		hostFromURL = model.HostPort(p.Scheme, p.Host, p.Port)
	}
	for _, h := range r.Headers {
		name := strings.ToLower(h.Name)
		if hopByHop[name] || strings.HasPrefix(h.Name, ":") {
			continue
		}
		if name == "host" && strings.EqualFold(h.Value, hostFromURL) {
			continue
		}
		if name == "accept-encoding" {
			compressed = true
		}
		args = append(args, "-H "+shellQuote(h.Name+": "+h.Value))
	}
	if compressed {
		args = append(args, "--compressed")
	}
	if len(r.Body) > 0 {
		if isShellSafeText(r.Body) {
			args = append(args, "--data-raw "+shellQuote(string(r.Body)))
		} else {
			args = append(args, "--data-binary "+ansiCQuote(r.Body))
		}
	}
	lines = append(lines, strings.Join(args, " \\\n  "))
	return strings.Join(lines, "\n"), nil
}

// shellQuote wraps s in single quotes, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isShellSafeText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// ansiCQuote renders arbitrary bytes as a bash $'...' string.
func ansiCQuote(b []byte) string {
	var sb strings.Builder
	sb.WriteString("$'")
	for _, c := range b {
		switch {
		case c == '\'':
			sb.WriteString(`\'`)
		case c == '\\':
			sb.WriteString(`\\`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, `\x%02x`, c)
		}
	}
	sb.WriteString("'")
	return sb.String()
}

// HeadersText renders headers as "Name: value" lines (Copy Headers).
func HeadersText(hs model.Headers) string {
	var sb strings.Builder
	for _, h := range hs {
		sb.WriteString(h.Name)
		sb.WriteString(": ")
		sb.WriteString(h.Value)
		sb.WriteString("\n")
	}
	return sb.String()
}

// sortedKeys is a small helper for deterministic map iteration.
func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
