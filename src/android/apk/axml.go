package apk

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// AndroidNS is the XML namespace URI of the android: attribute prefix.
const AndroidNS = "http://schemas.android.com/apk/res/android"

// Res_value data types.
const (
	TypeNull          uint8 = 0x00
	TypeReference     uint8 = 0x01
	TypeAttribute     uint8 = 0x02
	TypeString        uint8 = 0x03
	TypeFloat         uint8 = 0x04
	TypeDimension     uint8 = 0x05
	TypeFraction      uint8 = 0x06
	TypeDynamicRef    uint8 = 0x07
	TypeIntDec        uint8 = 0x10
	TypeIntHex        uint8 = 0x11
	TypeIntBoolean    uint8 = 0x12
	TypeIntColorARGB8 uint8 = 0x1c
	TypeIntColorRGB8  uint8 = 0x1d
	TypeIntColorARGB4 uint8 = 0x1e
	TypeIntColorRGB4  uint8 = 0x1f
)

// Limits protecting the parser from hostile manifests.
const (
	maxXMLElements = 200_000
	maxXMLDepth    = 256
	maxXMLAttrs    = 1024
)

// Value is a typed attribute value (Res_value) as stored in binary XML.
type Value struct {
	Type   uint8
	Data   uint32
	String string // the raw string value, when present
}

// IsReference reports whether the value refers to a resource ID.
func (v Value) IsReference() bool {
	return v.Type == TypeReference || v.Type == TypeDynamicRef
}

// Int returns the value as an integer, parsing string values if needed.
func (v Value) Int() (int64, bool) {
	switch {
	case v.Type >= TypeIntDec && v.Type <= TypeIntColorRGB4:
		if v.Type == TypeIntDec {
			return int64(int32(v.Data)), true
		}
		return int64(v.Data), true
	case v.Type == TypeString:
		n, err := strconv.ParseInt(strings.TrimSpace(v.String), 0, 64)
		return n, err == nil
	case v.Type == TypeFloat:
		f := math.Float32frombits(v.Data)
		if f != f || f > math.MaxInt32 || f < math.MinInt32 {
			return 0, false
		}
		return int64(f), true
	}
	return 0, false
}

// Bool returns the value as a boolean.
func (v Value) Bool() (bool, bool) {
	switch v.Type {
	case TypeIntBoolean, TypeIntDec, TypeIntHex:
		return v.Data != 0, true
	case TypeString:
		b, err := strconv.ParseBool(strings.TrimSpace(v.String))
		return b, err == nil
	}
	return false, false
}

// Text renders the value as a human-readable string (references as "@0x7f010000").
func (v Value) Text() string {
	switch v.Type {
	case TypeString:
		return v.String
	case TypeReference, TypeDynamicRef:
		return fmt.Sprintf("@0x%08x", v.Data)
	case TypeAttribute:
		return fmt.Sprintf("?0x%08x", v.Data)
	case TypeIntBoolean:
		return strconv.FormatBool(v.Data != 0)
	case TypeIntHex:
		return fmt.Sprintf("0x%x", v.Data)
	case TypeFloat:
		return strconv.FormatFloat(float64(math.Float32frombits(v.Data)), 'g', -1, 32)
	case TypeNull:
		return ""
	}
	if n, ok := v.Int(); ok {
		return strconv.FormatInt(n, 10)
	}
	if v.String != "" {
		return v.String
	}
	return fmt.Sprintf("0x%08x", v.Data)
}

// XMLAttr is a decoded binary-XML attribute.
type XMLAttr struct {
	Namespace  string
	Name       string
	ResourceID uint32 // from the resource map; 0 when unknown
	Value      Value
}

// XMLElement is a decoded binary-XML element.
type XMLElement struct {
	Namespace string
	Name      string
	Attrs     []XMLAttr
	Children  []*XMLElement
}

// Attr returns the attribute with the given namespace and local name. For the
// android namespace, obfuscated manifests (empty attribute names) are matched
// through the well-known framework resource IDs.
func (e *XMLElement) Attr(ns, name string) (Value, bool) {
	if e == nil {
		return Value{}, false
	}
	for _, a := range e.Attrs {
		if a.Name == name && a.Namespace == ns {
			return a.Value, true
		}
	}
	if ns == AndroidNS {
		if id, ok := androidAttrIDs[name]; ok {
			for _, a := range e.Attrs {
				if a.ResourceID == id {
					return a.Value, true
				}
			}
		}
	}
	return Value{}, false
}

// ChildrenNamed returns the direct children with the given element name.
func (e *XMLElement) ChildrenNamed(name string) []*XMLElement {
	if e == nil {
		return nil
	}
	var out []*XMLElement
	for _, c := range e.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// androidAttrIDs maps android: attribute names to their framework resource IDs
// (frameworks/base/core/res/res/values/public.xml).
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

// AndroidAttrID returns the framework resource ID of a well-known android:
// attribute name (used by encoders to emit a resource map).
func AndroidAttrID(name string) (uint32, bool) {
	id, ok := androidAttrIDs[name]
	return id, ok
}

// ParseAXML decodes an Android binary XML document (e.g. a compiled
// AndroidManifest.xml) and returns its root element. It never panics on
// malformed input; all offsets are bounds-checked and allocations capped.
func ParseAXML(b []byte) (*XMLElement, error) {
	root, err := readChunk(b, 0)
	if err != nil {
		return nil, fmt.Errorf("binary XML: %w", err)
	}
	if root.typ != chunkXML {
		return nil, fmt.Errorf("binary XML: unexpected root chunk type 0x%04x (want 0x0003)", root.typ)
	}
	var (
		pool   *stringPool
		resMap []uint32
		stack  []*XMLElement
		top    *XMLElement
		count  int
	)
	for off := root.headerSize; off < len(root.data); {
		c, err := readChunk(root.data, off)
		if err != nil {
			return nil, fmt.Errorf("binary XML: %w", err)
		}
		off += len(c.data)
		switch c.typ {
		case chunkStringPool:
			if pool != nil {
				continue // only the first pool is meaningful
			}
			if pool, err = parseStringPool(c); err != nil {
				return nil, fmt.Errorf("binary XML: %w", err)
			}
		case chunkXMLResMap:
			n := (len(c.data) - c.headerSize) / 4
			if n > maxPoolStrings {
				n = maxPoolStrings
			}
			resMap = make([]uint32, n)
			for i := range resMap {
				resMap[i], _ = c.u32(c.headerSize + 4*i)
			}
		case chunkXMLStartElem:
			if count++; count > maxXMLElements {
				return nil, fmt.Errorf("binary XML: more than %d elements", maxXMLElements)
			}
			if len(stack) >= maxXMLDepth {
				return nil, fmt.Errorf("binary XML: nesting deeper than %d", maxXMLDepth)
			}
			el, err := parseStartElement(c, pool, resMap)
			if err != nil {
				return nil, fmt.Errorf("binary XML: %w", err)
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, el)
			} else if top == nil {
				top = el
			} else {
				return nil, fmt.Errorf("binary XML: multiple root elements")
			}
			stack = append(stack, el)
		case chunkXMLEndElem:
			if len(stack) == 0 {
				return nil, fmt.Errorf("binary XML: unbalanced end element")
			}
			stack = stack[:len(stack)-1]
		default:
			// Namespaces, CDATA and unknown chunks are not needed.
		}
	}
	if top == nil {
		return nil, fmt.Errorf("binary XML: document has no root element")
	}
	return top, nil
}

func parseStartElement(c chunk, pool *stringPool, resMap []uint32) (*XMLElement, error) {
	ext := c.headerSize
	nsIdx, ok1 := c.u32(ext)
	nameIdx, ok2 := c.u32(ext + 4)
	attrStart, ok3 := c.u16(ext + 8)
	attrSize, ok4 := c.u16(ext + 10)
	attrCount, ok5 := c.u16(ext + 12)
	if !(ok1 && ok2 && ok3 && ok4 && ok5) {
		return nil, fmt.Errorf("start element: %w", errTruncated)
	}
	el := &XMLElement{}
	el.Namespace, _ = pool.get(nsIdx)
	el.Name, _ = pool.get(nameIdx)
	if attrCount > maxXMLAttrs {
		return nil, fmt.Errorf("start element %q has %d attributes (limit %d)", el.Name, attrCount, maxXMLAttrs)
	}
	if attrCount > 0 && attrSize < 20 {
		return nil, fmt.Errorf("start element %q: invalid attribute size %d", el.Name, attrSize)
	}
	base := ext + int(attrStart)
	if int64(base)+int64(attrCount)*int64(attrSize) > int64(len(c.data)) {
		return nil, fmt.Errorf("start element %q attributes: %w", el.Name, errTruncated)
	}
	el.Attrs = make([]XMLAttr, 0, attrCount)
	for i := 0; i < int(attrCount); i++ {
		a := base + i*int(attrSize)
		ns, _ := c.u32(a)
		name, _ := c.u32(a + 4)
		raw, _ := c.u32(a + 8)
		dt := c.data[a+15]
		data, _ := c.u32(a + 16)
		attr := XMLAttr{Value: Value{Type: dt, Data: data}}
		attr.Namespace, _ = pool.get(ns)
		attr.Name, _ = pool.get(name)
		if name != noIndex && uint64(name) < uint64(len(resMap)) {
			attr.ResourceID = resMap[name]
		}
		if attr.Name == "" && attr.ResourceID != 0 {
			attr.Name = androidAttrName(attr.ResourceID)
			if attr.Name != "" && attr.Namespace == "" {
				attr.Namespace = AndroidNS
			}
		}
		if dt == TypeString {
			attr.Value.String, _ = pool.get(data)
		} else if s, ok := pool.get(raw); ok {
			attr.Value.String = s
		}
		el.Attrs = append(el.Attrs, attr)
	}
	return el, nil
}

func androidAttrName(id uint32) string {
	for n, v := range androidAttrIDs {
		if v == id {
			return n
		}
	}
	return ""
}
