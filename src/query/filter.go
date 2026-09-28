// Package query implements the Network filter language and the Network Query
// Service used by the UI (UI → Query Service → Event Store).
//
// Filter syntax (see NETWORK.md#filter-language):
//
//	api.example.com                  free text: substring of URL (case-insensitive)
//	host:api.example.com             field match (host: exact or glob "*.example.com")
//	host contains "example.com"      explicit operator (contains/startswith/endswith/equals/matches)
//	method:POST status:401           implicit AND
//	status:4xx  status>=400          status class / comparisons
//	-host:cdn.example.com  NOT x     negation
//	(method:GET OR method:HEAD)      grouping and OR
//	path:/v1/  mime:json             substring fields
//	size>10k  duration>=500ms        numeric fields with units
//	is:error is:pending is:encrypted is:replay
//	path:/^\/v[0-9]+\//              regular expressions between slashes
package query

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/droidpector/apkinspector/src/model"
)

// Matcher reports whether a summary satisfies a compiled filter.
type Matcher func(*model.Summary) bool

// MatchAll matches every event.
func MatchAll(*model.Summary) bool { return true }

// ParseError describes an invalid filter with the byte offset of the problem.
type ParseError struct {
	Pos int
	Msg string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("filter error at position %d: %s", e.Pos+1, e.Msg)
}

// Compile parses a filter expression. An empty/blank expression matches all.
func Compile(expr string) (Matcher, error) {
	toks, err := lex(expr)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return MatchAll, nil
	}
	p := &parser{toks: toks}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		t := p.toks[p.pos]
		return nil, &ParseError{t.pos, fmt.Sprintf("unexpected %q", t.text)}
	}
	return n, nil
}

// ---- fields -----------------------------------------------------------------

type fieldKind int

const (
	fText     fieldKind = iota // default op: contains
	fExact                     // default op: equals (case-insensitive), glob with *
	fNumber                    // numeric comparisons
	fStatus                    // numeric + classes like 4xx
	fSize                      // numeric with byte units
	fDuration                  // numeric with time units (ms)
	fIs                        // boolean flags
)

type field struct {
	kind fieldKind
	str  func(*model.Summary) string
	num  func(*model.Summary) float64
}

var fields = map[string]field{
	"host":     {kind: fExact, str: func(s *model.Summary) string { return s.Host }},
	"method":   {kind: fExact, str: func(s *model.Summary) string { return s.Method }},
	"scheme":   {kind: fExact, str: func(s *model.Summary) string { return s.Scheme }},
	"protocol": {kind: fExact, str: func(s *model.Summary) string { return s.Protocol }},
	"kind":     {kind: fExact, str: func(s *model.Summary) string { return string(s.Kind) }},
	"type":     {kind: fExact, str: func(s *model.Summary) string { return string(s.Category) }},
	"package":  {kind: fText, str: func(s *model.Summary) string { return s.Package }},
	"path":     {kind: fText, str: func(s *model.Summary) string { return s.Path }},
	"query":    {kind: fText, str: func(s *model.Summary) string { return s.Query }},
	"url":      {kind: fText, str: summaryURL},
	"mime":     {kind: fText, str: func(s *model.Summary) string { return s.MIME }},
	"error":    {kind: fText, str: func(s *model.Summary) string { return s.Error }},
	"status":   {kind: fStatus, num: func(s *model.Summary) float64 { return float64(s.Status) }},
	"port":     {kind: fNumber, num: func(s *model.Summary) float64 { return float64(s.Port) }},
	"size":     {kind: fSize, num: func(s *model.Summary) float64 { return float64(s.ResponseSize) }},
	"reqsize":  {kind: fSize, num: func(s *model.Summary) float64 { return float64(s.RequestSize) }},
	"duration": {kind: fDuration, num: func(s *model.Summary) float64 { return s.DurationMs }},
	"is":       {kind: fIs},
}

var fieldAliases = map[string]string{
	"domain": "host", "content-type": "mime", "contenttype": "mime", "status-code": "status",
	"larger-than": "size>", "pkg": "package", "time": "duration",
}

func summaryURL(s *model.Summary) string {
	e := model.Event{Scheme: s.Scheme, Host: s.Host, Port: s.Port, Path: s.Path, Query: s.Query}
	if u := e.URL(); u != "" {
		return u
	}
	return s.Host
}

var isFlags = map[string]Matcher{
	"error":     func(s *model.Summary) bool { return s.State == model.StateError || s.Error != "" || s.Status >= 400 },
	"failed":    func(s *model.Summary) bool { return s.State == model.StateError || s.Error != "" },
	"pending":   func(s *model.Summary) bool { return s.State == model.StatePending },
	"complete":  func(s *model.Summary) bool { return s.State == model.StateComplete },
	"encrypted": func(s *model.Summary) bool { return s.Encrypted },
	"replay":    func(s *model.Summary) bool { return s.Initiator == model.InitiatorReplay },
	"https":     func(s *model.Summary) bool { return s.Scheme == "https" || s.Scheme == "wss" },
}

// ---- lexer ------------------------------------------------------------------

type tokKind int

const (
	tTerm tokKind = iota
	tLParen
	tRParen
	tOr
	tAnd
	tNot
)

type token struct {
	kind   tokKind
	pos    int
	text   string // raw text for messages
	neg    bool   // leading '-'
	value  string // value for terms
	quoted bool   // value came from a quoted string
}

func isDelim(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '(' || c == ')'
}

func lex(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, token{kind: tLParen, pos: i, text: "("})
			i++
		case c == ')':
			toks = append(toks, token{kind: tRParen, pos: i, text: ")"})
			i++
		default:
			start := i
			neg := false
			if (c == '-' || c == '!') && i+1 < len(s) && !isDelim(s[i+1]) {
				neg = true
				i++
			}
			var raw strings.Builder
			quoted := false
			for i < len(s) && !isDelim(s[i]) {
				if s[i] == '"' || s[i] == '\'' {
					q, n, err := readQuoted(s, i)
					if err != nil {
						return nil, err
					}
					raw.WriteString(q)
					quoted = true
					i = n
					continue
				}
				if s[i] == '/' && startsValue(raw.String()) {
					if end := regexEnd(s, i); end > 0 {
						raw.WriteString(s[i:end])
						i = end
						continue
					}
				}
				raw.WriteByte(s[i])
				i++
			}
			text := s[start:i]
			word := raw.String()
			if !quoted && !neg {
				switch strings.ToUpper(word) {
				case "OR", "||", "|":
					toks = append(toks, token{kind: tOr, pos: start, text: text})
					continue
				case "AND", "&&":
					toks = append(toks, token{kind: tAnd, pos: start, text: text})
					continue
				case "NOT":
					toks = append(toks, token{kind: tNot, pos: start, text: text})
					continue
				}
			}
			toks = append(toks, token{kind: tTerm, pos: start, text: text, neg: neg, value: word, quoted: quoted})
		}
	}
	return toks, nil
}

// startsValue reports whether a '/' at this point begins a value (regex literal):
// at the start of a term or right after a field operator.
func startsValue(prefix string) bool {
	return prefix == "" || strings.ContainsAny(prefix[len(prefix)-1:], ":<>=!~")
}

// regexEnd returns the index just past a /regex/ literal starting at i, or -1.
// The closing slash must be followed by a delimiter or the end of input.
func regexEnd(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '/':
			if j+1 == len(s) || s[j+1] == ' ' || s[j+1] == '\t' || s[j+1] == ')' {
				return j + 1
			}
		}
	}
	return -1
}

func readQuoted(s string, i int) (string, int, error) {
	q := s[i]
	var b strings.Builder
	j := i + 1
	for j < len(s) {
		switch s[j] {
		case '\\':
			if j+1 < len(s) {
				b.WriteByte(s[j+1])
				j += 2
				continue
			}
			j++
		case q:
			return b.String(), j + 1, nil
		default:
			b.WriteByte(s[j])
			j++
		}
	}
	return "", 0, &ParseError{i, "unterminated quoted string"}
}

// ---- parser -----------------------------------------------------------------

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() *token {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *parser) parseOr() (Matcher, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for t := p.peek(); t != nil && t.kind == tOr; t = p.peek() {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(s *model.Summary) bool { return l(s) || r(s) }
	}
	return left, nil
}

func (p *parser) parseAnd() (Matcher, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind == tOr || t.kind == tRParen {
			return left, nil
		}
		if t.kind == tAnd {
			p.pos++
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(s *model.Summary) bool { return l(s) && r(s) }
	}
}

func (p *parser) parseUnary() (Matcher, error) {
	t := p.peek()
	if t == nil {
		pos := 0
		if n := len(p.toks); n > 0 {
			last := p.toks[n-1]
			pos = last.pos + len(last.text)
		}
		return nil, &ParseError{pos, "expression expected"}
	}
	switch t.kind {
	case tNot:
		p.pos++
		m, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return func(s *model.Summary) bool { return !m(s) }, nil
	case tLParen:
		p.pos++
		m, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if c := p.peek(); c == nil || c.kind != tRParen {
			return nil, &ParseError{t.pos, "missing closing parenthesis"}
		}
		p.pos++
		return m, nil
	case tTerm:
		p.pos++
		m, err := p.parseTerm(t)
		if err != nil {
			return nil, err
		}
		if t.neg {
			inner := m
			m = func(s *model.Summary) bool { return !inner(s) }
		}
		return m, nil
	default:
		return nil, &ParseError{t.pos, fmt.Sprintf("unexpected %q", t.text)}
	}
}

var wordOps = map[string]string{
	"contains": "~", "startswith": "^=", "starts-with": "^=", "endswith": "$=", "ends-with": "$=",
	"equals": "=", "is": "=", "matches": "~re", "=": "=", "==": "=", "!=": "!=", ">": ">", ">=": ">=",
	"<": "<", "<=": "<=", "~": "~",
}

// parseTerm handles "field:value", "field<op>value", "field <wordop> value" and free text.
func (p *parser) parseTerm(t *token) (Matcher, error) {
	name, op, value, ok := splitFieldTerm(t.value)
	if !ok && !t.quoted {
		// "host contains x" form: bare field name followed by an operator word.
		if f := canonicalField(strings.ToLower(t.value)); f != "" {
			if nt := p.peek(); nt != nil && nt.kind == tTerm && !nt.quoted {
				if wop, isOp := wordOps[strings.ToLower(nt.value)]; isOp {
					vt := p.peekAt(1)
					if vt == nil || vt.kind != tTerm {
						return nil, &ParseError{nt.pos, fmt.Sprintf("value expected after %q", nt.value)}
					}
					p.pos += 2
					return buildFieldMatcher(f, wop, vt.value, vt.pos)
				}
			}
		}
	}
	if ok {
		if f := canonicalField(strings.ToLower(name)); f != "" {
			if strings.HasSuffix(f, ">") { // alias like larger-than:
				f, op = strings.TrimSuffix(f, ">"), ">"
			}
			if value == "" {
				return nil, &ParseError{t.pos, fmt.Sprintf("value expected for %q", name)}
			}
			return buildFieldMatcher(f, op, value, t.pos)
		}
	}
	// Free text over the URL.
	return textMatcher(summaryURL, "~", t.value, t.pos)
}

func (p *parser) peekAt(off int) *token {
	if p.pos+off < len(p.toks) {
		return &p.toks[p.pos+off]
	}
	return nil
}

func canonicalField(name string) string {
	if a, ok := fieldAliases[name]; ok {
		return a
	}
	if _, ok := fields[name]; ok {
		return name
	}
	return ""
}

// splitFieldTerm splits "name:value", "name>=value", "name:>=value", "name~value".
func splitFieldTerm(s string) (name, op, value string, ok bool) {
	i := strings.IndexAny(s, ":<>=!~")
	if i <= 0 {
		return "", "", "", false
	}
	name, rest := s[:i], s[i:]
	if strings.HasPrefix(rest, ":") {
		rest = rest[1:]
		op = ":"
	}
	for _, o := range []string{">=", "<=", "!=", "==", ">", "<", "=", "~"} {
		if strings.HasPrefix(rest, o) {
			op, rest = o, rest[len(o):]
			break
		}
	}
	if op == "" {
		return "", "", "", false
	}
	if op == "==" {
		op = "="
	}
	return name, op, rest, true
}

func buildFieldMatcher(name, op, value string, pos int) (Matcher, error) {
	f := fields[name]
	switch f.kind {
	case fIs:
		m, ok := isFlags[strings.ToLower(value)]
		if !ok {
			return nil, &ParseError{pos, fmt.Sprintf("unknown flag is:%s (valid: error, failed, pending, complete, encrypted, replay, https)", value)}
		}
		if op == "!=" {
			return func(s *model.Summary) bool { return !m(s) }, nil
		}
		return m, nil
	case fNumber, fStatus, fSize, fDuration:
		return numMatcher(f, op, value, pos)
	case fExact:
		if op == ":" {
			op = "="
		}
		return textMatcher(f.str, op, value, pos)
	default:
		if op == ":" {
			op = "~"
		}
		return textMatcher(f.str, op, value, pos)
	}
}

func textMatcher(get func(*model.Summary) string, op, value string, pos int) (Matcher, error) {
	slashed := len(value) >= 2 && strings.HasPrefix(value, "/") && strings.HasSuffix(value, "/")
	if slashed || op == "~re" {
		pat := value
		if slashed {
			pat = value[1 : len(value)-1]
		}
		re, err := regexp.Compile("(?i)" + pat)
		if err != nil {
			return nil, &ParseError{pos, fmt.Sprintf("invalid regular expression: %v", err)}
		}
		neg := op == "!="
		return func(s *model.Summary) bool { return re.MatchString(get(s)) != neg }, nil
	}
	v := strings.ToLower(value)
	switch op {
	case "~", ":":
		return func(s *model.Summary) bool { return strings.Contains(strings.ToLower(get(s)), v) }, nil
	case "^=":
		return func(s *model.Summary) bool { return strings.HasPrefix(strings.ToLower(get(s)), v) }, nil
	case "$=":
		return func(s *model.Summary) bool { return strings.HasSuffix(strings.ToLower(get(s)), v) }, nil
	case "=", "!=":
		neg := op == "!="
		if strings.Contains(v, "*") {
			re := globToRegexp(v)
			return func(s *model.Summary) bool { return re.MatchString(strings.ToLower(get(s))) != neg }, nil
		}
		return func(s *model.Summary) bool { return (strings.ToLower(get(s)) == v) != neg }, nil
	default:
		return nil, &ParseError{pos, fmt.Sprintf("operator %q is not valid for text fields", op)}
	}
}

func globToRegexp(g string) *regexp.Regexp {
	parts := strings.Split(g, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
}

func numMatcher(f field, op, value string, pos int) (Matcher, error) {
	if op == ":" || op == "~" {
		op = "="
	}
	if f.kind == fStatus {
		v := strings.ToLower(value)
		if len(v) == 3 && v[1:] == "xx" && v[0] >= '1' && v[0] <= '5' {
			lo := float64(v[0]-'0') * 100
			neg := op == "!="
			if op != "=" && op != "!=" {
				return nil, &ParseError{pos, "status classes like 4xx only support ':' or '!='"}
			}
			return func(s *model.Summary) bool {
				st := f.num(s)
				return (st >= lo && st < lo+100) != neg
			}, nil
		}
	}
	n, err := parseNumber(f.kind, value)
	if err != nil {
		return nil, &ParseError{pos, err.Error()}
	}
	var cmp func(a float64) bool
	switch op {
	case "=":
		cmp = func(a float64) bool { return a == n }
	case "!=":
		cmp = func(a float64) bool { return a != n }
	case ">":
		cmp = func(a float64) bool { return a > n }
	case ">=":
		cmp = func(a float64) bool { return a >= n }
	case "<":
		cmp = func(a float64) bool { return a < n }
	case "<=":
		cmp = func(a float64) bool { return a <= n }
	default:
		return nil, &ParseError{pos, fmt.Sprintf("operator %q is not valid for numeric fields", op)}
	}
	return func(s *model.Summary) bool { return cmp(f.num(s)) }, nil
}

func parseNumber(kind fieldKind, v string) (float64, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	mult := 1.0
	switch kind {
	case fSize:
		for _, u := range []struct {
			suf string
			m   float64
		}{{"kib", 1024}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"kb", 1000}, {"mb", 1e6}, {"gb", 1e9}, {"k", 1024}, {"m", 1 << 20}, {"g", 1 << 30}, {"b", 1}} {
			if strings.HasSuffix(s, u.suf) {
				s, mult = strings.TrimSuffix(s, u.suf), u.m
				break
			}
		}
	case fDuration:
		switch {
		case strings.HasSuffix(s, "ms"):
			s = strings.TrimSuffix(s, "ms")
		case strings.HasSuffix(s, "s"):
			s, mult = strings.TrimSuffix(s, "s"), 1000
		case strings.HasSuffix(s, "m"):
			s, mult = strings.TrimSuffix(s, "m"), 60000
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", v)
	}
	return n * mult, nil
}

// Quick filter categories exposed as UI buttons.
var quickFilters = map[string]func(*model.Summary) bool{
	"all":       MatchAll,
	"api":       func(s *model.Summary) bool { return s.Category == model.CatAPI },
	"document":  func(s *model.Summary) bool { return s.Category == model.CatDocument },
	"image":     func(s *model.Summary) bool { return s.Category == model.CatImage },
	"media":     func(s *model.Summary) bool { return s.Category == model.CatMedia },
	"websocket": func(s *model.Summary) bool { return s.Category == model.CatWebSocket },
	"dns":       func(s *model.Summary) bool { return s.Category == model.CatDNS },
	"other": func(s *model.Summary) bool {
		switch s.Category {
		case model.CatOther, model.CatScript, model.CatStyle, model.CatFont:
			return true
		}
		return false
	},
}

// QuickFilter returns the matcher for a quick filter name ("" or "all" = all).
func QuickFilter(name string) (Matcher, error) {
	if name == "" {
		return MatchAll, nil
	}
	m, ok := quickFilters[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("unknown quick filter %q", name)
	}
	return m, nil
}
