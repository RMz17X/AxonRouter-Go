package log

import (
	"fmt"
	"io"
	stdlog "log"
	"os"
	"path/filepath"
	"sync"
)

// File logging: the package logger and the standard library logger both
// emit through the std logger's output, so mirroring that output into a
// rotating file captures chat routing, quota and background-refresh lines
// alike — the durable record an operator (or an agent) needs for
// post-mortem analysis after an incident, when the in-memory console
// ring buffer is long gone.

const (
	defaultLogFileMaxBytes = 4 << 20 // 4 MiB per file (operator rule: every log rotation is 4 MB)
	defaultLogFileBackups  = 5       // ~24 MiB retained per node
)

var (
	fileLogMu     sync.Mutex
	fileLogWriter *rotatingFileWriter
)

// EnableFileLog mirrors all subsequent log output to a size-rotated file
// at path, creating parent directories as needed. It is idempotent for
// the same path. A failure is returned to the caller and never breaks
// logging itself: console output keeps working either way.
func EnableFileLog(path string) error {
	fileLogMu.Lock()
	defer fileLogMu.Unlock()
	if fileLogWriter != nil && fileLogWriter.path == path {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
	}
	w, err := newRotatingFileWriter(path, defaultLogFileMaxBytes, defaultLogFileBackups)
	if err != nil {
		return err
	}
	if fileLogWriter != nil {
		_ = fileLogWriter.Close()
	}
	fileLogWriter = w
	stdlog.SetOutput(io.MultiWriter(os.Stderr, w))
	return nil
}

// rotatingFileWriter is a minimal size-based rotating writer built on
// the stdlib only: when a write would exceed maxBytes, the current file
// is renamed to path.1, older backups shift up one slot, and anything
// beyond the configured backup count is dropped.
type rotatingFileWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

func newRotatingFileWriter(path string, maxBytes int64, backups int) (*rotatingFileWriter, error) {
	w := &rotatingFileWriter{path: path, maxBytes: maxBytes, backups: backups}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFileWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.file = f
	w.size = info.Size()
	return nil
}

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		// Closed or rotation failed: drop the line silently. Logging
		// must never fail (or panic) the code path that emitted it.
		return len(p), nil
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
		if w.file == nil {
			return len(p), nil
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFileWriter) rotate() {
	_ = w.file.Close()
	w.file = nil
	for i := w.backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if w.backups >= 1 {
		_ = os.Rename(w.path, w.path+".1")
	} else {
		_ = os.Remove(w.path)
	}
	// A failed reopen leaves file nil; Write then drops lines until the
	// next EnableFileLog call instead of crashing over a log file.
	_ = w.open()
}

func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
