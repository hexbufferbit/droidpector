package platform

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Extra levels beyond slog's DEBUG/INFO/WARN/ERROR.
const (
	LevelTrace = slog.Level(-8)
	LevelFatal = slog.Level(12)
)

// ParseLevel parses TRACE/DEBUG/INFO/WARN/ERROR/FATAL.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRACE":
		return LevelTrace, nil
	case "DEBUG":
		return slog.LevelDebug, nil
	case "", "INFO":
		return slog.LevelInfo, nil
	case "WARN", "WARNING":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	case "FATAL":
		return LevelFatal, nil
	}
	return 0, fmt.Errorf("unknown log level %q (use TRACE, DEBUG, INFO, WARN, ERROR or FATAL)", s)
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelDebug:
		return "TRACE"
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	case l < LevelFatal:
		return "ERROR"
	}
	return "FATAL"
}

// Loggers are the structured log channels: logs/app.log, vm.log, network.log.
type Loggers struct {
	App, VM, Network *slog.Logger
	closers          []io.Closer
}

// OpenLoggers creates rotating JSON-lines loggers in dir.
func OpenLoggers(dir string, level slog.Level, alsoStderr bool) (*Loggers, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Loggers{}
	mk := func(name string) *slog.Logger {
		w := &lumberjack.Logger{Filename: filepath.Join(dir, name), MaxSize: 20, MaxBackups: 5, Compress: true}
		l.closers = append(l.closers, w)
		var out io.Writer = w
		if alsoStderr {
			out = io.MultiWriter(w, os.Stderr)
		}
		return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.LevelKey {
					return slog.String(slog.LevelKey, levelName(a.Value.Any().(slog.Level)))
				}
				return a
			},
		})).With("component", strings.TrimSuffix(name, ".log"))
	}
	l.App, l.VM, l.Network = mk("app.log"), mk("vm.log"), mk("network.log")
	return l, nil
}

// Close flushes the log files.
func (l *Loggers) Close() {
	for _, c := range l.closers {
		c.Close()
	}
}

// Fatal logs at FATAL level.
func Fatal(log *slog.Logger, msg string, args ...any) {
	log.Log(context.Background(), LevelFatal, msg, args...)
}

// WriteLogBundle zips the log directory plus extra named files (diagnostics,
// config) into w. It is used by "Save diagnostic bundle" after a crash.
func WriteLogBundle(w io.Writer, logDir string, extra map[string][]byte) error {
	zw := zip.NewWriter(w)
	info := fmt.Sprintf("created: %s\nos: %s\narch: %s\ngo: %s\n", time.Now().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH, runtime.Version())
	if f, err := zw.Create("bundle-info.txt"); err == nil {
		io.WriteString(f, info)
	}
	for name, data := range extra {
		if f, err := zw.Create(name); err == nil {
			f.Write(data)
		}
	}
	err := filepath.WalkDir(logDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer src.Close()
		rel, _ := filepath.Rel(logDir, p)
		dst, err := zw.Create("logs/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = io.Copy(dst, src)
		return err
	})
	if err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}
