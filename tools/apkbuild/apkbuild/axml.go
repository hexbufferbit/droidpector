// Package apkbuild builds installable APKs without the Android SDK: it encodes
// AndroidManifest.xml to binary XML (AXML), builds a minimal resources.arsc
// with string resources, assembles a zipaligned archive and signs it with APK
// Signature Scheme v2.
//
// The encoders are written independently of src/android/apk so that tests of
// either package cross-validate the other.
package apkbuild

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"unicode/utf16"
)

// AndroidNS is the android: attribute namespace URI.
const AndroidNS = "http://schemas.android.com/apk/res/android"

// Res_value data types used by the encoder.
const (
	TypeReference  uint8 = 0x01
	TypeString     uint8 = 0x03
	TypeIntDec     uint8 = 0x10
	TypeIntHex     uint8 = 0x11
	TypeIntBoolean uint8 = 0x12
)

// Value is a typed attribute value.
type Value struct {
	Type   uint8
	Data   uint32
	String string // for TypeString
}

// Str returns a string value.
func Str(s string) Value { return Value{Type: TypeString, String: s} }

// Int returns a decimal integer value.
func Int(n int64) Value { return Value{Type: TypeIntDec, Data: uint32(n)} }

// Bool returns a boolean value (encoded as 0xFFFFFFFF / 0 like aapt2).
func Bool(b bool) Value {
	if b {
		return Value{Type: TypeIntBoolean, Data: 0xFFFFFFFF}
	}
	return Value{Type: TypeIntBoolean}
}

// Ref returns a resource reference value.
func Ref(id uint32) Value { return Value{Type: TypeReference, Data: id} }

// Attr is an XML attribute. ResourceID is filled automatically for well-known
// android: attributes when zero.
type Attr struct {
	Namespace  string
	Name       string
	ResourceID uint32
	Value      Value
}

// Element is an XML element.
type Element struct {
	Namespace string
	Name      string
	Attrs     []Attr
	Children  []*Element
}

// El is a convenience constructor for Element.
func El(name string, attrs []Attr, children ...*Element) *Element {
	return &Element{Name: name, Attrs: attrs, Children: children}
}

// A returns an android: attribute.
func A(name string, v Value) Attr { return Attr{Namespace: AndroidNS, Name: name, Value: v} }

// Plain returns an attribute without namespace.
func Plain(name string, v Value) Attr { return Attr{Name: name, Value: v} }

// androidAttrIDs are framework attribute resource IDs (public.xml).
var androidAttrIDs = map[string]uint32{
	"label":             0x01010001,
	"icon":              0x01010002,
	"name":              0x01010003,
	"hasCode":           0x0101000c,
	"enabled":           0x0101000e,
	"debuggable":        0x0101000f,
	"exported":          0x01010010,
	"targetActivity":    0x01010202,
	"minSdkVersion":     0x0101020c,
	"versionCode":       0x0101021b,
	"versionName":       0x0101021c,
	"targetSdkVersion":  0x01010270,
	"maxSdkVersion":     0x01010271,
	"testOnly":          0x01010272,
	"extractNativeLibs": 0x010104ea,
	"isFeatureSplit":    0x0101055b,
	"compileSdkVersion": 0x01010572,
	"versionCodeMajor":  0x01010576,
}

// AXMLOptions tunes the binary XML encoding.
type AXMLOptions struct {
	UTF8 bool // encode the string pool as UTF-8 (aapt2 default) instead of UTF-16
	// StripAttrNames writes empty names for attributes with resource IDs, like
	// some obfuscators do (tests the decoder's resource-map fallback).
	StripAttrNames bool
}

// EncodeAXML encodes root as Android binary XML.
func EncodeAXML(root *Element, opts AXMLOptions) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("encode AXML: nil root element")
	}
	e := &axmlEncoder{opts: opts, index: map[string]uint32{}}
	// Pass 1: attribute names with resource IDs must come first, in the same
	// order as the resource map.
	var resIDs []uint32
	seenRes := map[string]bool{}
	var walk func(el *Element) error
	walk = func(el *Element) error {
		for i := range el.Attrs {
			a := &el.Attrs[i]
			if a.ResourceID == 0 && a.Namespace == AndroidNS {
				a.ResourceID = androidAttrIDs[a.Name]
			}
			if a.ResourceID != 0 {
				key := e.attrNameKey(*a)
				if !seenRes[key] {
					seenRes[key] = true
					e.intern(key)
					resIDs = append(resIDs, a.ResourceID)
				}
			}
		}
		for _, c := range el.Children {
			if c == nil {
				return fmt.Errorf("encode AXML: nil child of <%s>", el.Name)
			}
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	usesAndroid := len(resIDs) > 0
	if usesAndroid {
		e.intern("android")
		e.intern(AndroidNS)
	}

	var body bytes.Buffer
	if usesAndroid {
		body.Write(nsChunk(chunkStartNS, e.intern("android"), e.intern(AndroidNS)))
	}
	e.encodeElement(&body, root)
	if usesAndroid {
		body.Write(nsChunk(chunkEndNS, e.intern("android"), e.intern(AndroidNS)))
	}

	var out bytes.Buffer
	pool := encodeStringPool(e.strings, opts.UTF8)
	resMap := make([]byte, 8+4*len(resIDs))
	putHeader(resMap, 0x0180, 8, len(resMap))
	for i, id := range resIDs {
		binary.LittleEndian.PutUint32(resMap[8+4*i:], id)
	}
	total := 8 + len(pool) + len(resMap) + body.Len()
	hdr := make([]byte, 8)
	putHeader(hdr, 0x0003, 8, total)
	out.Write(hdr)
	out.Write(pool)
	out.Write(resMap)
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

const (
	chunkStartNS   = 0x0100
	chunkEndNS     = 0x0101
	chunkStartElem = 0x0102
	chunkEndElem   = 0x0103
	none           = 0xFFFFFFFF
)

type axmlEncoder struct {
	opts    AXMLOptions
	strings []string
	index   map[string]uint32
	line    uint32
}

func (e *axmlEncoder) attrNameKey(a Attr) string {
	if e.opts.StripAttrNames {
		// Distinct placeholder per ID so the resource map stays one-to-one;
		// the string itself is emitted empty (see encodeStringPool caller).
		return fmt.Sprintf("\x00res:%08x", a.ResourceID)
	}
	return a.Name
}

func (e *axmlEncoder) intern(s string) uint32 {
	if i, ok := e.index[s]; ok {
		return i
	}
	i := uint32(len(e.strings))
	e.strings = append(e.strings, s)
	e.index[s] = i
	return i
}

func (e *axmlEncoder) str(s string) uint32 {
	if s == "" {
		return none
	}
	return e.intern(s)
}

func (e *axmlEncoder) encodeElement(w *bytes.Buffer, el *Element) {
	e.line++
	attrs := append([]Attr(nil), el.Attrs...)
	sort.SliceStable(attrs, func(i, j int) bool {
		ai, aj := attrs[i].ResourceID, attrs[j].ResourceID
		if (ai == 0) != (aj == 0) {
			return ai != 0
		}
		if ai != aj {
			return ai < aj
		}
		return attrs[i].Name < attrs[j].Name
	})
	b := make([]byte, 16+20+20*len(attrs))
	putHeader(b, chunkStartElem, 16, len(b))
	binary.LittleEndian.PutUint32(b[8:], e.line)
	binary.LittleEndian.PutUint32(b[12:], none)
	binary.LittleEndian.PutUint32(b[16:], e.str(el.Namespace))
	binary.LittleEndian.PutUint32(b[20:], e.str(el.Name))
	binary.LittleEndian.PutUint16(b[24:], 20)
	binary.LittleEndian.PutUint16(b[26:], 20)
	binary.LittleEndian.PutUint16(b[28:], uint16(len(attrs)))
	for i, a := range attrs {
		p := b[36+20*i:]
		binary.LittleEndian.PutUint32(p[0:], e.str(a.Namespace))
		var nameIdx uint32
		if a.ResourceID != 0 {
			nameIdx = e.intern(e.attrNameKey(a))
		} else {
			nameIdx = e.str(a.Name)
		}
		binary.LittleEndian.PutUint32(p[4:], nameIdx)
		raw, data := uint32(none), a.Value.Data
		if a.Value.Type == TypeString {
			raw = e.intern(a.Value.String)
			data = raw
		}
		binary.LittleEndian.PutUint32(p[8:], raw)
		binary.LittleEndian.PutUint16(p[12:], 8)
		p[14] = 0
		p[15] = a.Value.Type
		binary.LittleEndian.PutUint32(p[16:], data)
	}
	w.Write(b)
	for _, c := range el.Children {
		e.encodeElement(w, c)
	}
	end := make([]byte, 24)
	putHeader(end, chunkEndElem, 16, 24)
	binary.LittleEndian.PutUint32(end[8:], e.line)
	binary.LittleEndian.PutUint32(end[12:], none)
	binary.LittleEndian.PutUint32(end[16:], e.str(el.Namespace))
	binary.LittleEndian.PutUint32(end[20:], e.str(el.Name))
	w.Write(end)
}

func nsChunk(typ uint16, prefix, uri uint32) []byte {
	b := make([]byte, 24)
	putHeader(b, typ, 16, 24)
	binary.LittleEndian.PutUint32(b[8:], 1)
	binary.LittleEndian.PutUint32(b[12:], none)
	binary.LittleEndian.PutUint32(b[16:], prefix)
	binary.LittleEndian.PutUint32(b[20:], uri)
	return b
}

func putHeader(b []byte, typ uint16, headerSize, size int) {
	binary.LittleEndian.PutUint16(b[0:], typ)
	binary.LittleEndian.PutUint16(b[2:], uint16(headerSize))
	binary.LittleEndian.PutUint32(b[4:], uint32(size))
}

// encodeStringPool encodes a ResStringPool chunk. Strings starting with a NUL
// byte are placeholders and are emitted as empty strings.
func encodeStringPool(strs []string, utf8 bool) []byte {
	var data bytes.Buffer
	offsets := make([]uint32, len(strs))
	for i, s := range strs {
		if len(s) > 0 && s[0] == 0 {
			s = ""
		}
		offsets[i] = uint32(data.Len())
		if utf8 {
			u16 := len(utf16.Encode([]rune(s)))
			writeLen8(&data, u16)
			writeLen8(&data, len(s))
			data.WriteString(s)
			data.WriteByte(0)
		} else {
			u := utf16.Encode([]rune(s))
			if len(u) > 0x7FFF {
				binary.Write(&data, binary.LittleEndian, uint16(0x8000|len(u)>>16))
			}
			binary.Write(&data, binary.LittleEndian, uint16(len(u)&0xFFFF))
			for _, c := range u {
				binary.Write(&data, binary.LittleEndian, c)
			}
			data.Write([]byte{0, 0})
		}
	}
	for data.Len()%4 != 0 {
		data.WriteByte(0)
	}
	const hs = 28
	start := hs + 4*len(strs)
	b := make([]byte, start, start+data.Len())
	putHeader(b, 0x0001, hs, start+data.Len())
	binary.LittleEndian.PutUint32(b[8:], uint32(len(strs)))
	binary.LittleEndian.PutUint32(b[12:], 0)
	var flags uint32
	if utf8 {
		flags |= 1 << 8
	}
	binary.LittleEndian.PutUint32(b[16:], flags)
	binary.LittleEndian.PutUint32(b[20:], uint32(start))
	binary.LittleEndian.PutUint32(b[24:], 0)
	for i, o := range offsets {
		binary.LittleEndian.PutUint32(b[hs+4*i:], o)
	}
	return append(b, data.Bytes()...)
}

func writeLen8(w *bytes.Buffer, n int) {
	if n > 0x7F {
		w.WriteByte(byte(0x80 | n>>8))
	}
	w.WriteByte(byte(n))
}
