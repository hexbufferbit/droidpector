package content

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

var plain = []byte(`{"hello":"world","n":[1,2,3]}`)

func enc(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	switch name {
	case "gzip":
		w := gzip.NewWriter(&b)
		w.Write(data)
		w.Close()
	case "zlib":
		w := zlib.NewWriter(&b)
		w.Write(data)
		w.Close()
	case "rawdeflate":
		w, _ := flate.NewWriter(&b, 5)
		w.Write(data)
		w.Close()
	case "br":
		w := brotli.NewWriter(&b)
		w.Write(data)
		w.Close()
	case "zstd":
		w, _ := zstd.NewWriter(&b)
		w.Write(data)
		w.Close()
	}
	return b.Bytes()
}

func TestDecode(t *testing.T) {
	cases := []struct{ header, codec string }{
		{"gzip", "gzip"}, {"x-gzip", "gzip"}, {"deflate", "zlib"}, {"deflate", "rawdeflate"},
		{"br", "br"}, {"zstd", "zstd"}, {"GZIP ", "gzip"},
	}
	for _, c := range cases {
		got, err := Decode(c.header, enc(t, c.codec, plain), 1<<20)
		if err != nil || !bytes.Equal(got, plain) {
			t.Errorf("Decode(%s via %s) = %q, %v", c.header, c.codec, got, err)
		}
	}
	// Stacked encodings: applied gzip then br → header "gzip, br".
	stacked := enc(t, "br", enc(t, "gzip", plain))
	if got, err := Decode("gzip, br", stacked, 1<<20); err != nil || !bytes.Equal(got, plain) {
		t.Errorf("stacked decode = %q, %v", got, err)
	}
	if got, err := Decode("", plain, 1<<20); err != nil || !bytes.Equal(got, plain) {
		t.Error("identity decode failed")
	}
	if _, err := Decode("compress", plain, 1<<20); err == nil {
		t.Error("unknown encoding must fail")
	}
	if _, err := Decode("gzip", []byte("not gzip"), 1<<20); err == nil {
		t.Error("corrupt gzip must fail")
	}
	big := bytes.Repeat([]byte("a"), 10000)
	if got, err := Decode("gzip", enc(t, "gzip", big), 100); !errors.Is(err, ErrTooLarge) || len(got) != 100 {
		t.Errorf("limit: len=%d err=%v", len(got), err)
	}
}

func TestClassify(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	cases := []struct {
		ct   string
		body []byte
		want Kind
	}{
		{"application/json", plain, KindJSON},
		{"application/json", []byte("{broken"), KindText},
		{"application/problem+json", []byte(`{"a":1}`), KindJSON},
		{"text/plain", []byte(`[1,2]`), KindJSON},
		{"text/plain", []byte("hello"), KindText},
		{"text/html; charset=utf-8", []byte("<p>x</p>"), KindHTML},
		{"application/xml", []byte("<a/>"), KindXML},
		{"image/svg+xml", []byte("<svg/>"), KindXML},
		{"image/png", png, KindImage},
		{"", png, KindImage},
		{"", []byte("<!DOCTYPE html><html>"), KindHTML},
		{"application/octet-stream", []byte(`{"a":1}`), KindJSON},
		{"application/octet-stream", []byte{0, 1, 2, 3, 0xff}, KindBinary},
		{"text/plain", []byte{0xff, 0xfe, 0}, KindBinary},
		{"application/json", nil, KindEmpty},
	}
	for _, c := range cases {
		if got := Classify(c.ct, c.body); got != c.want {
			t.Errorf("Classify(%q, %q) = %s, want %s", c.ct, c.body, got, c.want)
		}
	}
}
