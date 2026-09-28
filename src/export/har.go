package export

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"time"

	"github.com/droidpector/apkinspector/src/content"
	"github.com/droidpector/apkinspector/src/model"
)

// BodySource loads stored body bytes (as transmitted, possibly content-encoded).
type BodySource interface {
	ReadBody(ref *model.BodyRef) ([]byte, error)
}

// HAR 1.2 structures (http://www.softwareishard.com/blog/har-12-spec/).
// Fields prefixed with '_' are the custom extensions Chrome DevTools also uses.

type harCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type harNV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
}

type harPostData struct {
	MimeType string  `json:"mimeType"`
	Text     string  `json:"text"`
	Params   []harNV `json:"params,omitempty"`
	Encoding string  `json:"_encoding,omitempty"`
	Comment  string  `json:"comment,omitempty"`
}

type harRequest struct {
	Method      string       `json:"method"`
	URL         string       `json:"url"`
	HTTPVersion string       `json:"httpVersion"`
	Cookies     []harCookie  `json:"cookies"`
	Headers     []harNV      `json:"headers"`
	QueryString []harNV      `json:"queryString"`
	PostData    *harPostData `json:"postData,omitempty"`
	HeadersSize int64        `json:"headersSize"`
	BodySize    int64        `json:"bodySize"`
}

type harContent struct {
	Size        int64  `json:"size"`
	Compression int64  `json:"compression,omitempty"`
	MimeType    string `json:"mimeType"`
	Text        string `json:"text,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

type harResponse struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []harCookie `json:"cookies"`
	Headers     []harNV     `json:"headers"`
	Content     harContent  `json:"content"`
	RedirectURL string      `json:"redirectURL"`
	HeadersSize int64       `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
	Error       string      `json:"_error,omitempty"`
}

type harTimings struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
	SSL     float64 `json:"ssl"`
}

type harWSMessage struct {
	Type   string  `json:"type"`
	Time   float64 `json:"time"`
	Opcode int     `json:"opcode"`
	Data   string  `json:"data"`
}

// HAREntry is one HAR 1.2 log entry.
type HAREntry struct {
	StartedDateTime string         `json:"startedDateTime"`
	Time            float64        `json:"time"`
	Request         harRequest     `json:"request"`
	Response        harResponse    `json:"response"`
	Cache           struct{}       `json:"cache"`
	Timings         harTimings     `json:"timings"`
	ServerIPAddress string         `json:"serverIPAddress,omitempty"`
	Connection      string         `json:"connection,omitempty"`
	Comment         string         `json:"comment,omitempty"`
	ResourceType    string         `json:"_resourceType,omitempty"`
	WSMessages      []harWSMessage `json:"_webSocketMessages,omitempty"`
}

// HAROptions controls HAR generation.
type HAROptions struct {
	Creator       string
	Version       string
	MaxBodyBytes  int64 // decoded body limit per entry (default 50 MiB)
	IncludeBodies bool
}

// WriteHAR streams a HAR 1.2 document for the HTTP and WebSocket events in
// events. Non-HTTP events (DNS, raw TCP, uninspected TLS) have no HAR
// representation and are skipped. Returns the number of entries written.
func WriteHAR(w io.Writer, opts HAROptions, events iter.Seq2[*model.Event, error], bodies BodySource) (int, error) {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 50 << 20
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	creator, _ := json.Marshal(harCreator{Name: opts.Creator, Version: opts.Version})
	if _, err := fmt.Fprintf(bw, `{"log":{"version":"1.2","creator":%s,"pages":[],"entries":[`, creator); err != nil {
		return 0, err
	}
	n := 0
	for e, err := range events {
		if err != nil {
			return n, err
		}
		if e.Kind != model.KindHTTP && e.Kind != model.KindWebSocket || e.Encrypted || e.Method == "" {
			continue
		}
		entry := BuildHAREntry(e, bodies, opts)
		b, err := json.Marshal(entry)
		if err != nil {
			return n, err
		}
		if n > 0 {
			bw.WriteByte(',')
		}
		if _, err := bw.Write(b); err != nil {
			return n, err
		}
		n++
	}
	if _, err := bw.WriteString("]}}"); err != nil {
		return n, err
	}
	return n, bw.Flush()
}

// BuildHAREntry converts a single event (exported for "Save Request").
func BuildHAREntry(e *model.Event, bodies BodySource, opts HAROptions) HAREntry {
	httpVersion := e.Protocol
	if httpVersion == "" {
		httpVersion = "HTTP/1.1"
	}
	u := e.URL()
	entry := HAREntry{
		StartedDateTime: e.StartedAt.UTC().Format(time.RFC3339Nano),
		Request: harRequest{
			Method: e.Method, URL: u, HTTPVersion: httpVersion,
			Cookies:     requestCookies(e.RequestHeaders),
			Headers:     nv(e.RequestHeaders),
			QueryString: queryNV(e.Query),
			HeadersSize: headersSize(e.RequestHeaders),
			BodySize:    e.RequestSize,
		},
		Response: harResponse{
			Status: e.Status, StatusText: statusText(e), HTTPVersion: httpVersion,
			Cookies:     responseCookies(e.ResponseHeaders),
			Headers:     nv(e.ResponseHeaders),
			RedirectURL: e.ResponseHeaders.Get("Location"),
			HeadersSize: headersSize(e.ResponseHeaders),
			BodySize:    e.ResponseSize,
			Content:     harContent{MimeType: e.ResponseHeaders.Get("Content-Type")},
			Error:       e.Error,
		},
	}
	if e.Conn != nil {
		if host, _ := model.SplitHostPort(e.Conn.RemoteAddr, 0); host != "" {
			entry.ServerIPAddress = host
		}
		entry.Connection = e.Conn.ID
	}
	entry.Timings, entry.Time = harTimingsOf(e)
	if e.Initiator == model.InitiatorReplay {
		entry.Comment = "Replayed by droidpector from request " + e.ReplayOf
	}
	if opts.IncludeBodies && bodies != nil {
		if e.RequestBody != nil && e.RequestBody.Size > 0 {
			entry.Request.PostData = postData(e, bodies)
		}
		if e.ResponseBody != nil {
			entry.Response.Content = responseContent(e, bodies, opts.MaxBodyBytes)
		}
	}
	if e.Kind == model.KindWebSocket {
		entry.ResourceType = "websocket"
		for _, f := range e.Frames {
			typ := "receive"
			if f.Outgoing {
				typ = "send"
			}
			data := string(f.Data)
			if f.Opcode == 2 {
				data = base64.StdEncoding.EncodeToString(f.Data)
			}
			entry.WSMessages = append(entry.WSMessages, harWSMessage{
				Type: typ, Time: float64(f.Time.UnixNano()) / 1e9, Opcode: f.Opcode, Data: data,
			})
		}
	}
	return entry
}

func statusText(e *model.Event) string {
	if e.StatusText != "" {
		return e.StatusText
	}
	return http.StatusText(e.Status)
}

func nv(hs model.Headers) []harNV {
	out := make([]harNV, 0, len(hs))
	for _, h := range hs {
		out = append(out, harNV{Name: h.Name, Value: h.Value})
	}
	return out
}

func queryNV(raw string) []harNV {
	out := []harNV{}
	for _, p := range model.ParseQuery(raw) {
		out = append(out, harNV{Name: p.Name, Value: p.Value})
	}
	return out
}

func headersSize(hs model.Headers) int64 {
	if len(hs) == 0 {
		return -1
	}
	return hs.Size()
}

func requestCookies(hs model.Headers) []harCookie {
	out := []harCookie{}
	r := http.Request{Header: http.Header{"Cookie": hs.Values("Cookie")}}
	for _, c := range r.Cookies() {
		out = append(out, harCookie{Name: c.Name, Value: c.Value})
	}
	return out
}

func responseCookies(hs model.Headers) []harCookie {
	out := []harCookie{}
	r := http.Response{Header: http.Header{"Set-Cookie": hs.Values("Set-Cookie")}}
	for _, c := range r.Cookies() {
		hc := harCookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, HTTPOnly: c.HttpOnly, Secure: c.Secure}
		if !c.Expires.IsZero() {
			hc.Expires = c.Expires.UTC().Format(time.RFC3339)
		}
		out = append(out, hc)
	}
	return out
}

func harTimingsOf(e *model.Event) (harTimings, float64) {
	t := e.Timing
	if t == nil {
		d := e.DurationMs
		if d < 0 {
			d = 0
		}
		return harTimings{Blocked: -1, DNS: -1, Connect: -1, SSL: -1, Send: 0, Wait: d, Receive: 0}, d
	}
	nonNeg := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		return v
	}
	ht := harTimings{
		Blocked: t.Blocked, DNS: t.DNS, Connect: t.Connect, SSL: t.TLS,
		Send: nonNeg(t.Send), Wait: nonNeg(t.Wait), Receive: nonNeg(t.Receive),
	}
	// HAR: connect includes ssl time.
	if ht.Connect >= 0 && ht.SSL > 0 {
		ht.Connect += ht.SSL
	}
	total := ht.Send + ht.Wait + ht.Receive
	for _, v := range []float64{ht.Blocked, ht.DNS, ht.Connect} {
		if v > 0 {
			total += v
		}
	}
	if total == 0 {
		total = nonNeg(e.DurationMs)
	}
	return ht, total
}

func postData(e *model.Event, bodies BodySource) *harPostData {
	raw, err := bodies.ReadBody(e.RequestBody)
	ct := e.RequestHeaders.Get("Content-Type")
	pd := &harPostData{MimeType: ct}
	if err != nil {
		pd.Comment = "request body unavailable: " + err.Error()
		return pd
	}
	if e.RequestBody.Truncated {
		pd.Comment = fmt.Sprintf("truncated: %d of %d bytes captured", e.RequestBody.Stored, e.RequestBody.Size)
	}
	body := raw
	if enc := e.RequestHeaders.Get("Content-Encoding"); enc != "" {
		if dec, err := content.Decode(enc, raw, 50<<20); err == nil {
			body = dec
		}
	}
	if content.IsPrintableText(body) {
		pd.Text = string(body)
		if model.MediaType(ct) == "application/x-www-form-urlencoded" {
			if vals, err := url.ParseQuery(pd.Text); err == nil {
				for _, k := range sortedKeys(vals) {
					for _, v := range vals[k] {
						pd.Params = append(pd.Params, harNV{Name: k, Value: v})
					}
				}
			}
		}
	} else {
		pd.Text = base64.StdEncoding.EncodeToString(body)
		pd.Encoding = "base64"
	}
	return pd
}

func responseContent(e *model.Event, bodies BodySource, limit int64) harContent {
	ct := e.ResponseHeaders.Get("Content-Type")
	c := harContent{MimeType: ct, Size: e.ResponseBody.Size}
	raw, err := bodies.ReadBody(e.ResponseBody)
	if err != nil {
		c.Comment = "response body unavailable: " + err.Error()
		return c
	}
	body := raw
	if enc := e.ResponseBody.Encoding; enc != "" {
		dec, derr := content.Decode(enc, raw, limit)
		if derr != nil && dec == nil {
			c.Comment = "could not decode " + enc + ": " + derr.Error()
		} else {
			body = dec
		}
	}
	if !e.ResponseBody.Truncated {
		c.Size = int64(len(body))
		if comp := e.ResponseBody.Size - c.Size; comp != 0 {
			c.Compression = comp
		}
	} else {
		c.Comment = fmt.Sprintf("truncated: %d of %d bytes captured", e.ResponseBody.Stored, e.ResponseBody.Size)
	}
	if model.IsTextual(ct) && content.IsPrintableText(body) || ct == "" && content.IsPrintableText(body) {
		c.Text = string(body)
	} else if len(body) > 0 {
		c.Text = base64.StdEncoding.EncodeToString(body)
		c.Encoding = "base64"
	}
	if c.MimeType == "" {
		c.MimeType = "x-unknown"
	}
	return c
}
