// Package content decodes HTTP bodies (Content-Encoding) and classifies them
// for display (JSON, XML, HTML, text, image, binary).
package content

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"github.com/droidpector/apkinspector/src/model"
)

// ErrTooLarge is returned when a decoded body exceeds the caller's limit.
var ErrTooLarge = errors.New("decoded body exceeds the size limit")

// Decode removes Content-Encoding layers (applied in order, e.g. "gzip, br")
// and returns at most limit bytes of plaintext. Unknown encodings are an error.
func Decode(encoding string, data []byte, limit int64) ([]byte, error) {
	encs := splitEncodings(encoding)
	out := data
	for i := len(encs) - 1; i >= 0; i-- {
		r, err := decoder(encs[i], bytes.NewReader(out))
		if err != nil {
			return nil, err
		}
		dec, err := io.ReadAll(io.LimitReader(r, limit+1))
		if c, ok := r.(io.Closer); ok {
			_ = c.Close()
		}
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("decoding %s body: %w", encs[i], err)
		}
		if int64(len(dec)) > limit {
			return dec[:limit], ErrTooLarge
		}
		out = dec
	}
	return out, nil
}

func splitEncodings(enc string) []string {
	var out []string
	for _, e := range strings.Split(enc, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && e != "identity" {
			out = append(out, e)
		}
	}
	return out
}

func decoder(enc string, r io.Reader) (io.Reader, error) {
	switch enc {
	case "gzip", "x-gzip":
		return gzip.NewReader(r)
	case "deflate":
		// "deflate" is zlib-wrapped per RFC 9110, but many servers send raw deflate.
		br := bufioPeek(r)
		if hdr, err := br.Peek(2); err == nil && len(hdr) == 2 && (uint16(hdr[0])<<8|uint16(hdr[1]))%31 == 0 && hdr[0]&0x0f == 8 {
			return zlib.NewReader(br)
		}
		return flate.NewReader(br), nil
	case "br":
		return brotli.NewReader(r), nil
	case "zstd":
		d, err := zstd.NewReader(r, zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return nil, err
		}
		return d.IOReadCloser(), nil
	}
	return nil, fmt.Errorf("unsupported content encoding %q", enc)
}

// Kind is how a body should be displayed.
type Kind string

const (
	KindEmpty  Kind = "empty"
	KindJSON   Kind = "json"
	KindXML    Kind = "xml"
	KindHTML   Kind = "html"
	KindText   Kind = "text"
	KindImage  Kind = "image"
	KindBinary Kind = "binary"
)

// Classify decides the display kind from the declared media type and the
// (decoded) bytes, sniffing when the declared type is missing or generic.
func Classify(contentType string, body []byte) Kind {
	if len(body) == 0 {
		return KindEmpty
	}
	mt := model.MediaType(contentType)
	switch {
	case mt == "image/svg+xml":
		return KindXML
	case strings.HasPrefix(mt, "image/"):
		return KindImage
	case mt == "application/json" || strings.HasSuffix(mt, "+json") || mt == "application/x-ndjson":
		if json.Valid(bytes.TrimSpace(body)) {
			return KindJSON
		}
		return textOrBinary(body)
	case mt == "text/html" || mt == "application/xhtml+xml":
		return KindHTML
	case mt == "application/xml" || mt == "text/xml" || strings.HasSuffix(mt, "+xml"):
		return KindXML
	case model.IsTextual(mt):
		if t := bytes.TrimSpace(body); len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid(t) {
			return KindJSON
		}
		return textOrBinary(body)
	}
	sniffed := http.DetectContentType(body)
	switch {
	case strings.HasPrefix(sniffed, "image/"):
		return KindImage
	case strings.HasPrefix(sniffed, "text/html"):
		return KindHTML
	case strings.HasPrefix(sniffed, "text/xml"):
		return KindXML
	}
	if t := bytes.TrimSpace(body); len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid(t) {
		return KindJSON
	}
	return textOrBinary(body)
}

func textOrBinary(b []byte) Kind {
	if IsPrintableText(b) {
		return KindText
	}
	return KindBinary
}

// IsPrintableText reports whether b is valid UTF-8 without control characters
// other than tab, CR and LF.
func IsPrintableText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

func bufioPeek(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }
