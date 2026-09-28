package apk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// Android resource chunk types (frameworks/base/libs/androidfw/include/androidfw/ResourceTypes.h).
const (
	chunkStringPool   = 0x0001
	chunkTable        = 0x0002
	chunkXML          = 0x0003
	chunkXMLStartNS   = 0x0100
	chunkXMLEndNS     = 0x0101
	chunkXMLStartElem = 0x0102
	chunkXMLEndElem   = 0x0103
	chunkXMLCData     = 0x0104
	chunkXMLResMap    = 0x0180
	chunkTablePackage = 0x0200
	chunkTableType    = 0x0201
	chunkTableSpec    = 0x0202
	chunkTableLibrary = 0x0203
)

const (
	stringPoolUTF8 = 1 << 8
	noIndex        = 0xFFFFFFFF

	// maxPoolStrings caps the number of strings a pool may declare so a hostile
	// header cannot make us allocate huge offset tables.
	maxPoolStrings = 1 << 20
	// maxStringLen caps a single decoded string (characters or bytes).
	maxStringLen = 1 << 16
)

var errTruncated = errors.New("truncated data")

// chunk is a bounds-checked view of one ResChunk_header-framed chunk.
type chunk struct {
	typ        uint16
	headerSize int
	data       []byte // the whole chunk, including its header
}

// readChunk parses the chunk header at b[off:] and returns the chunk. The
// returned chunk is guaranteed to lie entirely within b.
func readChunk(b []byte, off int) (chunk, error) {
	if off < 0 || off+8 > len(b) {
		return chunk{}, fmt.Errorf("chunk header at offset %d: %w", off, errTruncated)
	}
	typ := binary.LittleEndian.Uint16(b[off:])
	hs := int(binary.LittleEndian.Uint16(b[off+2:]))
	size := int64(binary.LittleEndian.Uint32(b[off+4:]))
	if hs < 8 || int64(hs) > size {
		return chunk{}, fmt.Errorf("chunk 0x%04x at offset %d has invalid header size %d (chunk size %d)", typ, off, hs, size)
	}
	if int64(off)+size > int64(len(b)) {
		return chunk{}, fmt.Errorf("chunk 0x%04x at offset %d declares size %d beyond end of data (%d): %w", typ, off, size, len(b), errTruncated)
	}
	return chunk{typ: typ, headerSize: hs, data: b[off : off+int(size)]}, nil
}

func (c chunk) u16(off int) (uint16, bool) {
	if off < 0 || off+2 > len(c.data) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(c.data[off:]), true
}

func (c chunk) u32(off int) (uint32, bool) {
	if off < 0 || off+4 > len(c.data) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(c.data[off:]), true
}

// stringPool is a lazily decoded ResStringPool.
type stringPool struct {
	utf8    bool
	offsets []uint32 // relative to strings start
	strData []byte
	cache   map[uint32]string
}

func parseStringPool(c chunk) (*stringPool, error) {
	if c.typ != chunkStringPool {
		return nil, fmt.Errorf("expected string pool chunk, got 0x%04x", c.typ)
	}
	if c.headerSize < 28 {
		return nil, fmt.Errorf("string pool header too small (%d bytes)", c.headerSize)
	}
	count, _ := c.u32(8)
	flags, _ := c.u32(16)
	stringsStart, _ := c.u32(20)
	if count > maxPoolStrings {
		return nil, fmt.Errorf("string pool declares %d strings (limit %d)", count, maxPoolStrings)
	}
	offTable := c.headerSize
	if int64(offTable)+int64(count)*4 > int64(len(c.data)) {
		return nil, fmt.Errorf("string pool offset table: %w", errTruncated)
	}
	p := &stringPool{utf8: flags&stringPoolUTF8 != 0, offsets: make([]uint32, count), cache: map[uint32]string{}}
	for i := range p.offsets {
		p.offsets[i] = binary.LittleEndian.Uint32(c.data[offTable+4*i:])
	}
	if count > 0 {
		if int64(stringsStart) > int64(len(c.data)) || int(stringsStart) < offTable {
			return nil, fmt.Errorf("string pool strings start %d out of range", stringsStart)
		}
		p.strData = c.data[stringsStart:]
	}
	return p, nil
}

// get returns string idx. Invalid indexes (including noIndex) yield "" and false.
func (p *stringPool) get(idx uint32) (string, bool) {
	if p == nil || idx == noIndex || uint64(idx) >= uint64(len(p.offsets)) {
		return "", false
	}
	if s, ok := p.cache[idx]; ok {
		return s, true
	}
	var (
		s  string
		ok bool
	)
	if p.utf8 {
		s, ok = decodeUTF8Str(p.strData, int(p.offsets[idx]))
	} else {
		s, ok = decodeUTF16Str(p.strData, int(p.offsets[idx]))
	}
	if ok && len(p.cache) < 4096 {
		p.cache[idx] = s
	}
	return s, ok
}

func decodeUTF8Str(b []byte, off int) (string, bool) {
	// UTF-16 length (ignored), then UTF-8 byte length; each 1 or 2 bytes.
	_, n, ok := utf8Len(b, off)
	if !ok {
		return "", false
	}
	off += n
	l, n, ok := utf8Len(b, off)
	if !ok {
		return "", false
	}
	off += n
	if l > maxStringLen || off+l > len(b) {
		return "", false
	}
	raw := b[off : off+l]
	if !utf8.Valid(raw) {
		// aapt's "modified UTF-8" can contain CESU surrogates; be lenient.
		return string([]rune(string(raw))), true
	}
	return string(raw), true
}

func utf8Len(b []byte, off int) (int, int, bool) {
	if off < 0 || off >= len(b) {
		return 0, 0, false
	}
	v := int(b[off])
	if v&0x80 == 0 {
		return v, 1, true
	}
	if off+1 >= len(b) {
		return 0, 0, false
	}
	return (v&0x7F)<<8 | int(b[off+1]), 2, true
}

func decodeUTF16Str(b []byte, off int) (string, bool) {
	if off < 0 || off+2 > len(b) {
		return "", false
	}
	l := int(binary.LittleEndian.Uint16(b[off:]))
	off += 2
	if l&0x8000 != 0 {
		if off+2 > len(b) {
			return "", false
		}
		l = (l&0x7FFF)<<16 | int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
	}
	if l > maxStringLen || off+2*l > len(b) {
		return "", false
	}
	u := make([]uint16, l)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[off+2*i:])
	}
	return string(utf16.Decode(u)), true
}
