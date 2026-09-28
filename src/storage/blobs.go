package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ErrBlobNotFound is returned when a referenced body no longer exists
// (for example removed by retention).
var ErrBlobNotFound = errors.New("body not found in blob store")

// BlobStore is a content-addressed store for request/response bodies:
// <root>/<aa>/<sha256>[.zst]. Identical bodies are stored once. Bodies that
// compress well are stored zstd-compressed.
type BlobStore struct {
	root string
	enc  *zstd.Encoder
	dec  *zstd.Decoder
}

const compressThreshold = 512

// OpenBlobStore creates the store directory if needed.
func OpenBlobStore(root string) (*BlobStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("creating blob store %s: %w", root, err)
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(1<<30))
	if err != nil {
		return nil, err
	}
	return &BlobStore{root: root, enc: enc, dec: dec}, nil
}

// Close releases codec resources.
func (b *BlobStore) Close() { b.dec.Close() }

// Hash returns the content address of data.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (b *BlobStore) path(hash string, compressed bool) string {
	p := filepath.Join(b.root, hash[:2], hash)
	if compressed {
		p += ".zst"
	}
	return p
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// Put stores data and returns its hash. It is safe for concurrent use; writes
// are atomic (temp file + rename) so readers never see partial blobs.
func (b *BlobStore) Put(data []byte) (string, error) {
	hash := Hash(data)
	if b.exists(hash) {
		return hash, nil
	}
	payload, compressed := data, false
	if len(data) >= compressThreshold {
		if c := b.enc.EncodeAll(data, make([]byte, 0, len(data)/2)); len(c) < len(data)*9/10 {
			payload, compressed = c, true
		}
	}
	final := b.path(hash, compressed)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return "", fmt.Errorf("creating blob directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("creating blob: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("writing blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("writing blob: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		os.Remove(tmp.Name())
		if b.exists(hash) { // a concurrent writer won the race
			return hash, nil
		}
		return "", fmt.Errorf("committing blob: %w", err)
	}
	return hash, nil
}

func (b *BlobStore) exists(hash string) bool {
	for _, c := range []bool{false, true} {
		if _, err := os.Stat(b.path(hash, c)); err == nil {
			return true
		}
	}
	return false
}

// Get returns the original bytes for hash and verifies their integrity.
func (b *BlobStore) Get(hash string) ([]byte, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("invalid blob id %q", hash)
	}
	data, err := os.ReadFile(b.path(hash, false))
	if errors.Is(err, fs.ErrNotExist) {
		c, cerr := os.ReadFile(b.path(hash, true))
		if errors.Is(cerr, fs.ErrNotExist) {
			return nil, ErrBlobNotFound
		}
		if cerr != nil {
			return nil, fmt.Errorf("reading blob: %w", cerr)
		}
		if data, err = b.dec.DecodeAll(c, nil); err != nil {
			return nil, fmt.Errorf("blob %s is corrupt: %w", hash[:12], err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("reading blob: %w", err)
	}
	if Hash(data) != hash {
		return nil, fmt.Errorf("blob %s failed integrity check", hash[:12])
	}
	return data, nil
}

// Sweep deletes blobs not in keep that are older than grace (the grace period
// protects blobs written by in-flight captures not yet committed to the DB).
// It returns the number of files and bytes removed.
func (b *BlobStore) Sweep(keep map[string]bool, grace time.Duration) (int, int64, error) {
	cutoff := time.Now().Add(-grace)
	var n int
	var freed int64
	err := filepath.WalkDir(b.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // keep sweeping other directories
		}
		if d.IsDir() {
			return nil
		}
		name := strings.TrimSuffix(d.Name(), ".zst")
		info, ierr := d.Info()
		if ierr != nil || info.ModTime().After(cutoff) {
			return nil
		}
		if strings.HasPrefix(name, ".tmp-") || validHash(name) && !keep[name] {
			if os.Remove(p) == nil {
				n++
				freed += info.Size()
			}
		}
		return nil
	})
	return n, freed, err
}

// DiskUsage returns the total bytes used by the store.
func (b *BlobStore) DiskUsage() int64 {
	var total int64
	_ = filepath.WalkDir(b.root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
