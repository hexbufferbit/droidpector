package adb

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"
)

// Sync protocol constants (adb/file_sync_protocol.h).
const (
	syncDataMax = 64 * 1024
	syncMaxPath = 1024
	sIFREG      = 0o100000
	sIFMT       = 0o170000
	sIFDIR      = 0o040000
)

// SyncError is a FAIL reply from the sync service.
type SyncError struct {
	Op      string // "push" or "pull"
	Path    string
	Message string // adbd's message, e.g. "open failed: Permission denied"
}

func (e *SyncError) Error() string {
	return fmt.Sprintf("adb: %s %s: %s", e.Op, e.Path, e.Message)
}

// FileInfo is the result of Stat.
type FileInfo struct {
	Mode    uint32 // raw st_mode
	Size    int64
	ModTime time.Time
}

// IsDir reports whether the path is a directory.
func (f FileInfo) IsDir() bool { return f.Mode&sIFMT == sIFDIR }

// IsRegular reports whether the path is a regular file.
func (f FileInfo) IsRegular() bool { return f.Mode&sIFMT == sIFREG }

// Perm returns the permission bits.
func (f FileInfo) Perm() fs.FileMode { return fs.FileMode(f.Mode & 0o777) }

func syncRequest(id string, arg []byte) []byte {
	b := make([]byte, 8+len(arg))
	copy(b, id)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(arg)))
	copy(b[8:], arg)
	return b
}

func checkRemotePath(p string) error {
	if p == "" || len(p) > syncMaxPath {
		return fmt.Errorf("adb: invalid remote path %q (must be 1..%d bytes)", p, syncMaxPath)
	}
	return nil
}

func readSyncFail(r io.Reader, n uint32, op, path string) error {
	if n > syncDataMax {
		return fmt.Errorf("%w: sync FAIL message of %d bytes", ErrProtocol, n)
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return fmt.Errorf("adb: %s %s: %w", op, path, unexpected(err))
	}
	return &SyncError{Op: op, Path: path, Message: string(msg)}
}

// Push uploads size bytes from r to remotePath with the given permission
// bits and modification time. It fails if r yields fewer than size bytes.
func (c *Conn) Push(ctx context.Context, r io.Reader, size int64, remotePath string, mode fs.FileMode, mtime time.Time) error {
	if err := checkRemotePath(remotePath); err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("adb: push %s: negative size", remotePath)
	}
	return c.withStream(ctx, "sync:", func(s *Stream) error {
		bw := bufio.NewWriterSize(s, int(c.info.MaxPayload))
		spec := fmt.Sprintf("%s,%d", remotePath, sIFREG|uint32(mode.Perm()))
		if _, err := bw.Write(syncRequest("SEND", []byte(spec))); err != nil {
			return fmt.Errorf("adb: push %s: %w", remotePath, err)
		}
		buf := make([]byte, 8+syncDataMax)
		var sent int64
		lr := io.LimitReader(r, size)
		for {
			n, rerr := io.ReadFull(lr, buf[8:])
			if n > 0 {
				copy(buf, "DATA")
				binary.LittleEndian.PutUint32(buf[4:], uint32(n))
				if _, err := bw.Write(buf[:8+n]); err != nil {
					return fmt.Errorf("adb: push %s: %w", remotePath, err)
				}
				sent += int64(n)
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			if rerr != nil {
				return fmt.Errorf("adb: push %s: reading local data: %w", remotePath, rerr)
			}
		}
		if sent != size {
			return fmt.Errorf("adb: push %s: local data ended after %d of %d bytes", remotePath, sent, size)
		}
		done := make([]byte, 8)
		copy(done, "DONE")
		binary.LittleEndian.PutUint32(done[4:], uint32(mtime.Unix()))
		if _, err := bw.Write(done); err != nil {
			return fmt.Errorf("adb: push %s: %w", remotePath, err)
		}
		if err := bw.Flush(); err != nil {
			return fmt.Errorf("adb: push %s: %w", remotePath, err)
		}
		var resp [8]byte
		if _, err := io.ReadFull(s, resp[:]); err != nil {
			return fmt.Errorf("adb: push %s: waiting for result: %w", remotePath, unexpected(err))
		}
		n := binary.LittleEndian.Uint32(resp[4:])
		switch string(resp[:4]) {
		case "OKAY":
			s.Write(syncRequest("QUIT", nil))
			return nil
		case "FAIL":
			return readSyncFail(s, n, "push", remotePath)
		}
		return fmt.Errorf("%w: unexpected sync reply %q", ErrProtocol, resp[:4])
	})
}

// Stat returns information about remotePath. A missing path yields an error
// wrapping fs.ErrNotExist.
func (c *Conn) Stat(ctx context.Context, remotePath string) (FileInfo, error) {
	if err := checkRemotePath(remotePath); err != nil {
		return FileInfo{}, err
	}
	var fi FileInfo
	err := c.withStream(ctx, "sync:", func(s *Stream) error {
		if _, err := s.Write(syncRequest("STAT", []byte(remotePath))); err != nil {
			return fmt.Errorf("adb: stat %s: %w", remotePath, err)
		}
		var resp [16]byte
		if _, err := io.ReadFull(s, resp[:]); err != nil {
			return fmt.Errorf("adb: stat %s: %w", remotePath, unexpected(err))
		}
		if string(resp[:4]) != "STAT" {
			return fmt.Errorf("%w: unexpected sync reply %q", ErrProtocol, resp[:4])
		}
		fi = FileInfo{
			Mode:    binary.LittleEndian.Uint32(resp[4:]),
			Size:    int64(binary.LittleEndian.Uint32(resp[8:])),
			ModTime: time.Unix(int64(binary.LittleEndian.Uint32(resp[12:])), 0),
		}
		s.Write(syncRequest("QUIT", nil))
		return nil
	})
	if err != nil {
		return FileInfo{}, err
	}
	if fi.Mode == 0 {
		return FileInfo{}, fmt.Errorf("adb: stat %s: %w", remotePath, fs.ErrNotExist)
	}
	return fi, nil
}

// Pull downloads remotePath into w and returns the number of bytes written.
func (c *Conn) Pull(ctx context.Context, remotePath string, w io.Writer) (int64, error) {
	if err := checkRemotePath(remotePath); err != nil {
		return 0, err
	}
	var total int64
	err := c.withStream(ctx, "sync:", func(s *Stream) error {
		if _, err := s.Write(syncRequest("RECV", []byte(remotePath))); err != nil {
			return fmt.Errorf("adb: pull %s: %w", remotePath, err)
		}
		br := bufio.NewReaderSize(s, syncDataMax+8)
		var hdr [8]byte
		for {
			if _, err := io.ReadFull(br, hdr[:]); err != nil {
				return fmt.Errorf("adb: pull %s: %w", remotePath, unexpected(err))
			}
			n := binary.LittleEndian.Uint32(hdr[4:])
			switch string(hdr[:4]) {
			case "DATA":
				if n > syncDataMax {
					return fmt.Errorf("%w: sync DATA chunk of %d bytes", ErrProtocol, n)
				}
				m, err := io.CopyN(w, br, int64(n))
				total += m
				if err != nil {
					if errors.Is(err, io.EOF) {
						err = io.ErrUnexpectedEOF
					}
					return fmt.Errorf("adb: pull %s: %w", remotePath, err)
				}
			case "DONE":
				s.Write(syncRequest("QUIT", nil))
				return nil
			case "FAIL":
				return readSyncFail(br, n, "pull", remotePath)
			default:
				return fmt.Errorf("%w: unexpected sync reply %q", ErrProtocol, hdr[:4])
			}
		}
	})
	return total, err
}
