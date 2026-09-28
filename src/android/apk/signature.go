package apk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"strings"
)

// APK Signing Block constants (https://source.android.com/docs/security/features/apksigning/v2).
const (
	sigBlockMagic = "APK Sig Block 42"

	SigBlockIDV2  uint32 = 0x7109871a
	SigBlockIDV3  uint32 = 0xf05368c0
	SigBlockIDV31 uint32 = 0x1b93ad61

	eocdSig     = 0x06054b50
	eocdMinSize = 22
	maxComment  = 0xFFFF

	maxSigBlockSize = 64 << 20
)

// Signature describes which APK signature schemes are present. Presence is
// detected structurally; cryptographic verification is left to the device.
type Signature struct {
	V1  bool `json:"v1"`  // JAR signature (META-INF/*.RSA|DSA|EC)
	V2  bool `json:"v2"`  // APK Signature Scheme v2
	V3  bool `json:"v3"`  // APK Signature Scheme v3
	V31 bool `json:"v31"` // APK Signature Scheme v3.1
}

// Signed reports whether any signature scheme is present.
func (s Signature) Signed() bool { return s.V1 || s.V2 || s.V3 || s.V31 }

// Schemes lists the present schemes, e.g. ["v1", "v2"].
func (s Signature) Schemes() []string {
	var out []string
	for _, x := range []struct {
		ok   bool
		name string
	}{{s.V1, "v1"}, {s.V2, "v2"}, {s.V3, "v3"}, {s.V31, "v3.1"}} {
		if x.ok {
			out = append(out, x.name)
		}
	}
	return out
}

// isV1SignatureFile reports whether a zip entry name is a JAR signature block.
func isV1SignatureFile(name string) bool {
	dir, file := path.Split(name)
	if !strings.EqualFold(dir, "META-INF/") {
		return false
	}
	switch strings.ToUpper(path.Ext(file)) {
	case ".RSA", ".DSA", ".EC":
		return true
	}
	return false
}

// EOCD holds the fields of the ZIP End of Central Directory record needed for
// APK signing.
type EOCD struct {
	Offset   int64 // offset of the EOCD record itself
	CDOffset int64 // offset of the central directory
	CDSize   int64
}

// FindEOCD locates the End of Central Directory record of a ZIP archive.
func FindEOCD(r io.ReaderAt, size int64) (EOCD, error) {
	if size < eocdMinSize {
		return EOCD{}, fmt.Errorf("file too small to be a ZIP archive (%d bytes)", size)
	}
	n := int64(eocdMinSize + maxComment)
	if n > size {
		n = size
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return EOCD{}, fmt.Errorf("reading ZIP trailer: %w", err)
	}
	for i := len(buf) - eocdMinSize; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) != eocdSig {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(buf[i+20:]))
		if i+eocdMinSize+commentLen != len(buf) {
			continue // signature bytes inside a comment
		}
		e := EOCD{
			Offset:   size - n + int64(i),
			CDSize:   int64(binary.LittleEndian.Uint32(buf[i+12:])),
			CDOffset: int64(binary.LittleEndian.Uint32(buf[i+16:])),
		}
		if e.CDOffset+e.CDSize > e.Offset {
			return EOCD{}, fmt.Errorf("ZIP central directory (offset %d, size %d) overlaps the EOCD record", e.CDOffset, e.CDSize)
		}
		return e, nil
	}
	return EOCD{}, fmt.Errorf("ZIP End of Central Directory record not found")
}

// SigningBlock is a parsed APK Signing Block.
type SigningBlock struct {
	Offset int64             // file offset of the block start
	Pairs  map[uint32][]byte // ID -> value
}

// ReadSigningBlock reads the APK Signing Block that precedes the central
// directory. It returns (nil, nil) when the APK has no signing block.
func ReadSigningBlock(r io.ReaderAt, eocd EOCD) (*SigningBlock, error) {
	if eocd.CDOffset < 32 {
		return nil, nil
	}
	var footer [24]byte
	if _, err := r.ReadAt(footer[:], eocd.CDOffset-24); err != nil {
		return nil, fmt.Errorf("reading APK signing block footer: %w", err)
	}
	if string(footer[8:]) != sigBlockMagic {
		return nil, nil
	}
	blockSize := binary.LittleEndian.Uint64(footer[:8])
	if blockSize < 24 || blockSize > maxSigBlockSize || int64(blockSize)+8 > eocd.CDOffset {
		return nil, fmt.Errorf("APK signing block has invalid size %d", blockSize)
	}
	start := eocd.CDOffset - int64(blockSize) - 8
	block := make([]byte, blockSize+8)
	if _, err := r.ReadAt(block, start); err != nil {
		return nil, fmt.Errorf("reading APK signing block: %w", err)
	}
	if binary.LittleEndian.Uint64(block) != blockSize {
		return nil, fmt.Errorf("APK signing block header and footer sizes differ")
	}
	sb := &SigningBlock{Offset: start, Pairs: map[uint32][]byte{}}
	pairs := block[8 : len(block)-24]
	for len(pairs) > 0 {
		if len(pairs) < 12 {
			return nil, fmt.Errorf("APK signing block: truncated ID-value pair")
		}
		l := binary.LittleEndian.Uint64(pairs)
		if l < 4 || l > uint64(len(pairs)-8) {
			return nil, fmt.Errorf("APK signing block: pair length %d out of range", l)
		}
		id := binary.LittleEndian.Uint32(pairs[8:])
		sb.Pairs[id] = bytes.Clone(pairs[12 : 8+l])
		pairs = pairs[8+l:]
	}
	return sb, nil
}
