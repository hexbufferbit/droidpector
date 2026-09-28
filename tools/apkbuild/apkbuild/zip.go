package apkbuild

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"sort"
	"strings"
)

// zipalignExtraID is the extra field ID Android's zipalign uses for padding.
const zipalignExtraID = 0xD935

// File is one archive entry.
type File struct {
	Name string
	Data []byte
	// Store disables compression. Stored entries are aligned to Align bytes.
	Store bool
	// Align is the alignment of stored data; 0 selects 4096 for lib/*.so and 4
	// for everything else (like `zipalign -p 4`).
	Align int
}

// dosEpoch is the fixed timestamp written to every entry (reproducible builds).
const (
	dosDate = (2008-1980)<<9 | 1<<5 | 1 // 2008-01-01
	dosTime = 0
)

// BuildZip assembles an archive from files in the given order, compressing
// with Deflate unless File.Store is set and zip-aligning stored entries.
// AndroidManifest.xml is placed first if present.
func BuildZip(files []File) ([]byte, error) {
	files = append([]File(nil), files...)
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Name == "AndroidManifest.xml" && files[j].Name != "AndroidManifest.xml"
	})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	seen := map[string]bool{}
	for _, f := range files {
		if f.Name == "" || strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "\\") || strings.Contains(f.Name, "..") {
			return nil, fmt.Errorf("build zip: invalid entry name %q", f.Name)
		}
		if seen[f.Name] {
			return nil, fmt.Errorf("build zip: duplicate entry %q", f.Name)
		}
		seen[f.Name] = true
		// The raw MS-DOS date/time fields are used on purpose: setting
		// Modified would add an extended-timestamp extra field and change the
		// byte layout that alignment and reproducibility rely on.
		fh := &zip.FileHeader{
			Name: f.Name,
			//lint:ignore SA1019 fixed DOS timestamp, see comment above
			ModifiedDate: dosDate,
			//lint:ignore SA1019 fixed DOS timestamp, see comment above
			ModifiedTime:       dosTime,
			CRC32:              crc32.ChecksumIEEE(f.Data),
			UncompressedSize64: uint64(len(f.Data)),
		}
		payload := f.Data
		if f.Store {
			fh.Method = zip.Store
			if err := zw.Flush(); err != nil {
				return nil, err
			}
			align := f.Align
			if align <= 0 {
				align = 4
				if strings.HasPrefix(f.Name, "lib/") && strings.HasSuffix(f.Name, ".so") {
					align = 4096
				}
			}
			fh.Extra = alignExtra(buf.Len()+30+len(f.Name), align)
		} else {
			fh.Method = zip.Deflate
			var cb bytes.Buffer
			fw, err := flate.NewWriter(&cb, flate.BestCompression)
			if err != nil {
				return nil, err
			}
			if _, err := fw.Write(f.Data); err != nil {
				return nil, err
			}
			if err := fw.Close(); err != nil {
				return nil, err
			}
			payload = cb.Bytes()
		}
		fh.CompressedSize64 = uint64(len(payload))
		w, err := zw.CreateRaw(fh)
		if err != nil {
			return nil, fmt.Errorf("build zip: %s: %w", f.Name, err)
		}
		if _, err := w.Write(payload); err != nil {
			return nil, fmt.Errorf("build zip: %s: %w", f.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("build zip: %w", err)
	}
	return buf.Bytes(), nil
}

// alignExtra returns a zipalign extra field making data that would start at
// dataOff (without extra) start at a multiple of align.
func alignExtra(dataOff, align int) []byte {
	if dataOff%align == 0 {
		return nil
	}
	n := align - dataOff%align
	for n < 6 {
		n += align
	}
	b := make([]byte, n)
	binary.LittleEndian.PutUint16(b[0:], zipalignExtraID)
	binary.LittleEndian.PutUint16(b[2:], uint16(n-4))
	binary.LittleEndian.PutUint16(b[4:], uint16(align))
	return b
}
