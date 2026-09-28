package adb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Shell protocol v2 packet IDs (adb/shell_protocol.h).
const (
	shellStdin       = 0
	shellStdout      = 1
	shellStderr      = 2
	shellExit        = 3
	shellCloseStdin  = 4
	shellWindowSize  = 5
	shellHeaderSize  = 5
	maxShellPacket   = 1 << 20
	maxShellOutput   = 64 << 20
	legacyExitMarker = "__APKINSPECTOR_EXIT_"
)

// ErrOutputTooLarge is returned when a shell command produces more output
// than the client buffers.
var ErrOutputTooLarge = fmt.Errorf("adb: shell output exceeds %d bytes", maxShellOutput)

// Shell runs cmd through /system/bin/sh on the device and returns its
// output and exit code. With the shell_v2 feature stdout and stderr are
// separated; otherwise stderr is merged into stdout and the exit code is
// recovered with an appended sentinel.
func (c *Conn) Shell(ctx context.Context, cmd string) (stdout, stderr []byte, exitCode int, err error) {
	if c.info.HasFeature("shell_v2") {
		return c.shellV2(ctx, cmd)
	}
	return c.shellLegacy(ctx, cmd)
}

// withStream opens service and runs fn, closing the stream when ctx ends.
func (c *Conn) withStream(ctx context.Context, service string, fn func(*Stream) error) error {
	s, err := c.Open(ctx, service)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	defer s.Close()
	err = fn(s)
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("adb: %s: %w", firstWord(service), ctx.Err())
	}
	return err
}

func firstWord(service string) string {
	if i := strings.IndexByte(service, ':'); i >= 0 {
		return service[:i]
	}
	return service
}

func (c *Conn) shellV2(ctx context.Context, cmd string) ([]byte, []byte, int, error) {
	var out, errb bytes.Buffer
	code := -1
	err := c.withStream(ctx, "shell,v2,raw:"+cmd, func(s *Stream) error {
		// No stdin: tell the shell right away so commands reading it see EOF.
		if _, err := s.Write([]byte{shellCloseStdin, 0, 0, 0, 0}); err != nil {
			return fmt.Errorf("adb: shell: %w", err)
		}
		var hdr [shellHeaderSize]byte
		for {
			if _, err := io.ReadFull(s, hdr[:]); err != nil {
				if errors.Is(err, io.EOF) {
					if code < 0 {
						return fmt.Errorf("%w: shell ended without an exit status", ErrProtocol)
					}
					return nil
				}
				return fmt.Errorf("adb: shell: %w", err)
			}
			n := binary.LittleEndian.Uint32(hdr[1:])
			if n > maxShellPacket {
				return fmt.Errorf("%w: shell packet of %d bytes", ErrProtocol, n)
			}
			var dst io.Writer = io.Discard
			switch hdr[0] {
			case shellStdout:
				dst = &out
			case shellStderr:
				dst = &errb
			}
			if out.Len()+errb.Len()+int(n) > maxShellOutput {
				return ErrOutputTooLarge
			}
			if hdr[0] == shellExit {
				var b [1]byte
				if n != 1 {
					return fmt.Errorf("%w: exit packet of %d bytes", ErrProtocol, n)
				}
				if _, err := io.ReadFull(s, b[:]); err != nil {
					return fmt.Errorf("adb: shell: %w", err)
				}
				code = int(b[0])
				continue
			}
			if _, err := io.CopyN(dst, s, int64(n)); err != nil {
				return fmt.Errorf("adb: shell: %w", unexpected(err))
			}
		}
	})
	if err != nil {
		return out.Bytes(), errb.Bytes(), -1, err
	}
	return out.Bytes(), errb.Bytes(), code, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (c *Conn) shellLegacy(ctx context.Context, cmd string) ([]byte, []byte, int, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, -1, fmt.Errorf("adb: shell: %w", err)
	}
	marker := legacyExitMarker + hex.EncodeToString(nonce[:]) + "_"
	// A newline (not ';') keeps trailing comments or '&' in cmd harmless.
	full := cmd + "\necho -n \"" + marker + "$?\""
	var out bytes.Buffer
	err := c.withStream(ctx, "shell:"+full, func(s *Stream) error {
		_, err := io.Copy(&limitedWriter{w: &out, n: maxShellOutput}, s)
		if err != nil {
			return fmt.Errorf("adb: shell: %w", err)
		}
		return nil
	})
	if err != nil {
		return out.Bytes(), nil, -1, err
	}
	b := out.Bytes()
	i := bytes.LastIndex(b, []byte(marker))
	if i < 0 {
		return b, nil, -1, fmt.Errorf("%w: legacy shell output has no exit status marker", ErrProtocol)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(b[i+len(marker):])))
	if err != nil {
		return b[:i], nil, -1, fmt.Errorf("%w: legacy shell exit status %q", ErrProtocol, b[i+len(marker):])
	}
	return b[:i], nil, code, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, ErrOutputTooLarge
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// Exec opens an "exec:" stream running cmd without a shell PTY; stdout is
// read from and stdin written to the returned stream. The context only
// bounds opening the stream; close the stream to stop the command.
func (c *Conn) Exec(ctx context.Context, cmd string) (*Stream, error) {
	return c.Open(ctx, "exec:"+cmd)
}

// Root asks adbd to restart itself as root (the "root:" service, available on
// userdebug/eng builds). adbd closes the transport while restarting, so the
// caller must reconnect afterwards. It returns adbd's message, e.g.
// "restarting adbd as root" or "adbd is already running as root".
func (c *Conn) Root(ctx context.Context) (string, error) {
	var out []byte
	err := c.withStream(ctx, "root:", func(s *Stream) error {
		var err error
		out, err = io.ReadAll(io.LimitReader(s, 4096))
		return err
	})
	msg := strings.TrimSpace(string(out))
	if err != nil && msg == "" {
		return "", fmt.Errorf("adb: root: %w", err)
	}
	if strings.Contains(msg, "cannot run as root in production builds") {
		return msg, fmt.Errorf("adb: root: %s", msg)
	}
	return msg, nil
}
