package apk

import (
	"fmt"
)

// ResTable_type flags and entry flags.
const (
	typeFlagSparse   = 0x01
	typeFlagOffset16 = 0x02

	entryFlagComplex = 0x0001
	entryFlagCompact = 0x0008

	noEntry32 = 0xFFFFFFFF
	noEntry16 = 0xFFFF

	maxRefDepth = 8
)

// resourceTable is a minimal resources.arsc index able to resolve simple
// (non-bag) resource values by ID.
type resourceTable struct {
	values   *stringPool
	packages map[uint8]*resPackage
}

type resPackage struct {
	id    uint8
	name  string
	types map[uint8][]resType // type id -> configurations in file order
}

type resType struct {
	c           chunk
	defaultConf bool
	entryCount  uint32
	entriesAt   int
	flags       uint8
}

// parseResourceTable indexes a resources.arsc blob. The blob is retained and
// entries are decoded on demand.
func parseResourceTable(b []byte) (*resourceTable, error) {
	root, err := readChunk(b, 0)
	if err != nil {
		return nil, fmt.Errorf("resources.arsc: %w", err)
	}
	if root.typ != chunkTable {
		return nil, fmt.Errorf("resources.arsc: unexpected root chunk type 0x%04x (want 0x0002)", root.typ)
	}
	t := &resourceTable{packages: map[uint8]*resPackage{}}
	for off := root.headerSize; off < len(root.data); {
		c, err := readChunk(root.data, off)
		if err != nil {
			return nil, fmt.Errorf("resources.arsc: %w", err)
		}
		off += len(c.data)
		switch c.typ {
		case chunkStringPool:
			if t.values == nil {
				if t.values, err = parseStringPool(c); err != nil {
					return nil, fmt.Errorf("resources.arsc value strings: %w", err)
				}
			}
		case chunkTablePackage:
			p, err := parsePackage(c)
			if err != nil {
				return nil, fmt.Errorf("resources.arsc: %w", err)
			}
			if _, dup := t.packages[p.id]; !dup {
				t.packages[p.id] = p
			}
		}
	}
	return t, nil
}

func parsePackage(c chunk) (*resPackage, error) {
	id, ok := c.u32(8)
	if !ok || c.headerSize < 12 {
		return nil, fmt.Errorf("package header: %w", errTruncated)
	}
	p := &resPackage{id: uint8(id), types: map[uint8][]resType{}}
	// Package name: char16_t[128] at offset 12.
	var name []rune
	for i := 0; i < 128; i++ {
		v, ok := c.u16(12 + 2*i)
		if !ok || v == 0 {
			break
		}
		name = append(name, rune(v))
	}
	p.name = string(name)
	nTypes := 0
	for off := c.headerSize; off < len(c.data); {
		sub, err := readChunk(c.data, off)
		if err != nil {
			return nil, fmt.Errorf("package %q: %w", p.name, err)
		}
		off += len(sub.data)
		if sub.typ != chunkTableType {
			continue // type/key string pools, specs, libraries, overlayables ...
		}
		if nTypes++; nTypes > 1<<16 {
			return nil, fmt.Errorf("package %q: too many type chunks", p.name)
		}
		rt, err := parseType(sub)
		if err != nil {
			return nil, fmt.Errorf("package %q: %w", p.name, err)
		}
		tid := sub.data[8]
		p.types[tid] = append(p.types[tid], rt)
	}
	return p, nil
}

func parseType(c chunk) (resType, error) {
	// ResTable_type: header(8) id u8, flags u8, reserved u16, entryCount u32,
	// entriesStart u32, ResTable_config config.
	if c.headerSize < 20+4 || len(c.data) < c.headerSize {
		return resType{}, fmt.Errorf("type chunk header: %w", errTruncated)
	}
	flags := c.data[9]
	count, _ := c.u32(12)
	start, _ := c.u32(16)
	confSize, _ := c.u32(20)
	if int64(20)+int64(confSize) > int64(c.headerSize) || confSize < 4 {
		confSize = uint32(c.headerSize - 20)
	}
	def := true
	for _, b := range c.data[24 : 20+int(confSize)] {
		if b != 0 {
			def = false
			break
		}
	}
	if int64(start) > int64(len(c.data)) {
		return resType{}, fmt.Errorf("type chunk entries start %d out of range", start)
	}
	return resType{c: c, defaultConf: def, entryCount: count, entriesAt: int(start), flags: flags}, nil
}

// entryValue returns the simple value of entry idx in this configuration.
func (rt resType) entryValue(idx uint16) (Value, bool) {
	c := rt.c
	var off uint32
	switch {
	case rt.flags&typeFlagSparse != 0:
		// Sorted pairs of {u16 index, u16 offset/4}; binary search.
		lo, hi := 0, int(rt.entryCount)
		if int64(c.headerSize)+int64(hi)*4 > int64(len(c.data)) {
			return Value{}, false
		}
		found := false
		for lo < hi {
			m := (lo + hi) / 2
			ei, _ := c.u16(c.headerSize + 4*m)
			switch {
			case ei == idx:
				o, _ := c.u16(c.headerSize + 4*m + 2)
				off, found = uint32(o)*4, true
				lo = hi
			case ei < idx:
				lo = m + 1
			default:
				hi = m
			}
		}
		if !found {
			return Value{}, false
		}
	case rt.flags&typeFlagOffset16 != 0:
		if uint32(idx) >= rt.entryCount {
			return Value{}, false
		}
		o, ok := c.u16(c.headerSize + 2*int(idx))
		if !ok || o == noEntry16 {
			return Value{}, false
		}
		off = uint32(o) * 4
	default:
		if uint32(idx) >= rt.entryCount {
			return Value{}, false
		}
		o, ok := c.u32(c.headerSize + 4*int(idx))
		if !ok || o == noEntry32 {
			return Value{}, false
		}
		off = o
	}
	e := int64(rt.entriesAt) + int64(off)
	if e+8 > int64(len(c.data)) {
		return Value{}, false
	}
	ep := int(e)
	size, _ := c.u16(ep)
	flags, _ := c.u16(ep + 2)
	if flags&entryFlagCompact != 0 {
		data, _ := c.u32(ep + 4)
		return Value{Type: uint8(flags >> 8), Data: data}, true
	}
	if flags&entryFlagComplex != 0 {
		return Value{}, false // bags (styles, plurals, arrays) are not simple values
	}
	vp := ep + int(size)
	if size < 8 || vp+8 > len(c.data) {
		return Value{}, false
	}
	data, _ := c.u32(vp + 4)
	return Value{Type: c.data[vp+3], Data: data}, true
}

// lookup returns the value of resource id, preferring the default configuration
// and falling back to the first configuration that defines the entry.
func (t *resourceTable) lookup(id uint32) (Value, bool) {
	if t == nil {
		return Value{}, false
	}
	p := t.packages[uint8(id>>24)]
	if p == nil {
		return Value{}, false
	}
	types := p.types[uint8(id>>16)]
	entry := uint16(id)
	for _, rt := range types {
		if rt.defaultConf {
			if v, ok := rt.entryValue(entry); ok {
				return t.withString(v), true
			}
		}
	}
	for _, rt := range types {
		if v, ok := rt.entryValue(entry); ok {
			return t.withString(v), true
		}
	}
	return Value{}, false
}

func (t *resourceTable) withString(v Value) Value {
	if v.Type == TypeString {
		v.String, _ = t.values.get(v.Data)
	}
	return v
}

// resolve follows references (up to a small depth) and returns the final value.
func (t *resourceTable) resolve(v Value) (Value, bool) {
	for i := 0; v.IsReference(); i++ {
		if i >= maxRefDepth || t == nil {
			return v, false
		}
		nv, ok := t.lookup(v.Data)
		if !ok {
			return v, false
		}
		v = nv
	}
	return v, true
}
