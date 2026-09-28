package apkbuild

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// PackageID is the resource package ID of application resources.
const PackageID = 0x7f

const (
	stringTypeID   = 1
	resConfigSize  = 64
	typeHeaderSize = 20 + resConfigSize
)

// LocalizedValue is one configuration of a string resource. An empty Locale
// is the default configuration; otherwise a two-letter language code.
type LocalizedValue struct {
	Locale string
	Value  string
}

// StringResource is a string resource with one or more configurations.
type StringResource struct {
	Name   string
	Values []LocalizedValue
}

// ResourceTable is the output of EncodeStringTable.
type ResourceTable struct {
	ARSC []byte
	// IDs maps "string/<name>" to the assigned resource ID.
	IDs map[string]uint32
}

// EncodeStringTable encodes a resources.arsc for package pkg containing only
// string resources. IDs are assigned as 0x7f01xxxx in declaration order.
func EncodeStringTable(pkg string, strs []StringResource, utf8 bool) (*ResourceTable, error) {
	if len(strs) > 0xFFFF {
		return nil, fmt.Errorf("encode resources.arsc: too many strings (%d)", len(strs))
	}
	// Collect configurations, default first.
	var locales []string
	seen := map[string]bool{}
	addLocale := func(l string) {
		if !seen[l] {
			seen[l] = true
			locales = append(locales, l)
		}
	}
	for _, s := range strs {
		for _, v := range s.Values {
			if v.Locale == "" {
				addLocale("")
			}
		}
	}
	for _, s := range strs {
		for _, v := range s.Values {
			if v.Locale != "" && len(v.Locale) != 2 {
				return nil, fmt.Errorf("encode resources.arsc: locale %q must be a two-letter language code", v.Locale)
			}
			addLocale(v.Locale)
		}
	}

	var values []string
	valueIdx := map[string]uint32{}
	intern := func(s string) uint32 {
		if i, ok := valueIdx[s]; ok {
			return i
		}
		valueIdx[s] = uint32(len(values))
		values = append(values, s)
		return valueIdx[s]
	}
	ids := map[string]uint32{}
	keys := make([]string, len(strs))
	for i, s := range strs {
		if s.Name == "" {
			return nil, fmt.Errorf("encode resources.arsc: string resource %d has no name", i)
		}
		if _, dup := ids["string/"+s.Name]; dup {
			return nil, fmt.Errorf("encode resources.arsc: duplicate string resource %q", s.Name)
		}
		keys[i] = s.Name
		ids["string/"+s.Name] = PackageID<<24 | stringTypeID<<16 | uint32(i)
		for _, v := range s.Values {
			intern(v.Value)
		}
	}

	var pkgBody bytes.Buffer
	typeStrings := encodeStringPool([]string{"string"}, utf8)
	keyStrings := encodeStringPool(keys, utf8)
	const pkgHeader = 288
	pkgBody.Write(typeStrings)
	pkgBody.Write(keyStrings)

	// Type spec.
	spec := make([]byte, 16+4*len(strs))
	putHeader(spec, 0x0202, 16, len(spec))
	spec[8] = stringTypeID
	binary.LittleEndian.PutUint32(spec[12:], uint32(len(strs)))
	pkgBody.Write(spec)

	for _, loc := range locales {
		entriesStart := typeHeaderSize + 4*len(strs)
		var entries bytes.Buffer
		offsets := make([]uint32, len(strs))
		for i, s := range strs {
			offsets[i] = 0xFFFFFFFF
			for _, v := range s.Values {
				if v.Locale != loc {
					continue
				}
				offsets[i] = uint32(entries.Len())
				e := make([]byte, 16)
				binary.LittleEndian.PutUint16(e[0:], 8) // entry size
				binary.LittleEndian.PutUint16(e[2:], 0) // flags
				binary.LittleEndian.PutUint32(e[4:], uint32(i))
				binary.LittleEndian.PutUint16(e[8:], 8) // Res_value size
				e[11] = TypeString
				binary.LittleEndian.PutUint32(e[12:], valueIdx[v.Value])
				entries.Write(e)
				break
			}
		}
		t := make([]byte, entriesStart, entriesStart+entries.Len())
		putHeader(t, 0x0201, typeHeaderSize, entriesStart+entries.Len())
		t[8] = stringTypeID
		binary.LittleEndian.PutUint32(t[12:], uint32(len(strs)))
		binary.LittleEndian.PutUint32(t[16:], uint32(entriesStart))
		binary.LittleEndian.PutUint32(t[20:], resConfigSize)
		if loc != "" {
			copy(t[20+8:], loc) // ResTable_config.language
		}
		for i, o := range offsets {
			binary.LittleEndian.PutUint32(t[typeHeaderSize+4*i:], o)
		}
		pkgBody.Write(append(t, entries.Bytes()...))
	}

	ph := make([]byte, pkgHeader)
	putHeader(ph, 0x0200, pkgHeader, pkgHeader+pkgBody.Len())
	binary.LittleEndian.PutUint32(ph[8:], PackageID)
	name := utf16.Encode([]rune(pkg))
	if len(name) > 127 {
		return nil, fmt.Errorf("encode resources.arsc: package name too long")
	}
	for i, c := range name {
		binary.LittleEndian.PutUint16(ph[12+2*i:], c)
	}
	binary.LittleEndian.PutUint32(ph[268:], pkgHeader)                          // typeStrings
	binary.LittleEndian.PutUint32(ph[272:], 1)                                  // lastPublicType
	binary.LittleEndian.PutUint32(ph[276:], uint32(pkgHeader+len(typeStrings))) // keyStrings
	binary.LittleEndian.PutUint32(ph[280:], uint32(len(keys)))                  // lastPublicKey
	binary.LittleEndian.PutUint32(ph[284:], 0)                                  // typeIdOffset

	pool := encodeStringPool(values, utf8)
	var out bytes.Buffer
	th := make([]byte, 12)
	putHeader(th, 0x0002, 12, 12+len(pool)+len(ph)+pkgBody.Len())
	binary.LittleEndian.PutUint32(th[8:], 1)
	out.Write(th)
	out.Write(pool)
	out.Write(ph)
	out.Write(pkgBody.Bytes())
	return &ResourceTable{ARSC: out.Bytes(), IDs: ids}, nil
}
