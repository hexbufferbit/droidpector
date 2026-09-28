package apkbuild

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// intAttrs are android: attributes aapt2 compiles to integers.
var intAttrs = map[string]bool{
	"versionCode":       true,
	"versionCodeMajor":  true,
	"minSdkVersion":     true,
	"targetSdkVersion":  true,
	"maxSdkVersion":     true,
	"compileSdkVersion": true,
}

// ParseManifestXML parses a textual AndroidManifest.xml into an Element tree,
// inferring typed values like aapt2 does for the common cases: integers for
// SDK/version attributes, booleans for "true"/"false", references for
// "@string/name" (resolved through resIDs, e.g. ResourceTable.IDs) and "@0x…"
// literals, and strings otherwise.
func ParseManifestXML(r io.Reader, resIDs map[string]uint32) (*Element, error) {
	dec := xml.NewDecoder(r)
	var stack []*Element
	var root *Element
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse manifest XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := &Element{Namespace: t.Name.Space, Name: t.Name.Local}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				v, err := inferValue(a.Name.Space, a.Name.Local, a.Value, resIDs)
				if err != nil {
					return nil, fmt.Errorf("parse manifest XML: <%s %s>: %w", t.Name.Local, a.Name.Local, err)
				}
				el.Attrs = append(el.Attrs, Attr{Namespace: a.Name.Space, Name: a.Name.Local, Value: v})
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("parse manifest XML: multiple root elements")
				}
				root = el
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, el)
			}
			stack = append(stack, el)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, fmt.Errorf("parse manifest XML: no root element")
	}
	return root, nil
}

func inferValue(ns, name, s string, resIDs map[string]uint32) (Value, error) {
	switch {
	case strings.HasPrefix(s, "@0x"):
		n, err := strconv.ParseUint(s[3:], 16, 32)
		if err != nil {
			return Value{}, fmt.Errorf("invalid reference %q", s)
		}
		return Ref(uint32(n)), nil
	case strings.HasPrefix(s, "@") && strings.Contains(s, "/"):
		id, ok := resIDs[strings.TrimPrefix(s, "@")]
		if !ok {
			return Value{}, fmt.Errorf("unknown resource %s (define it as a string resource)", s)
		}
		return Ref(id), nil
	case s == "true" || s == "false":
		return Bool(s == "true"), nil
	case ns == AndroidNS && intAttrs[name]:
		if n, err := strconv.ParseInt(s, 0, 64); err == nil {
			return Int(n), nil
		}
	}
	return Str(s), nil
}
