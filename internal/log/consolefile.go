package log

import (
	"bufio"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Console file store: the console ring buffer holds only the last
// consoleMaxLines entries, so a restart — or one busy hour — erases the
// trail an operator came for. Every captured entry is therefore also
// appended, in its exact {time, level, line} wire shape, to a
// size-rotated JSONL file under the data dir. All log rotation in this
// tree is 4 MiB per file by operator rule; five backups keep ~24 MiB.
// The boot path rehydrates the ring from the retained files, and the
// console-logs handler searches the files when a request trail is asked
// for, so a req_id stays findable for as long as its lines are retained
// instead of for as long as the ring happens to remember them.
//
// Writes ride the same rotating writer as the routing-log mirror and
// fail silent: logging must never break the path that emitted the line.

const (
	consoleFileMaxBytes  = 4 << 20 // 4 MiB per file
	consoleFileBackups   = 5
	consoleSearchMaxHits = 1000 // newest matches a file search returns
	consoleScanLineCap   = 8 << 20
)

var (
	consoleFileMu     sync.Mutex
	consoleFileWriter *rotatingFileWriter
	consoleFilePath   string
)

// EnableConsoleFileLog starts the console file store at path and loads
// the retained history into the ring buffer. Idempotent for the same
// path. A failure is returned to the caller and never breaks logging
// itself: the in-memory ring keeps working either way.
func EnableConsoleFileLog(path string) error {
	consoleFileMu.Lock()
	if consoleFileWriter != nil && consoleFilePath == path {
		consoleFileMu.Unlock()
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			consoleFileMu.Unlock()
			return fmt.Errorf("create console log dir: %w", err)
		}
	}
	w, err := newRotatingFileWriter(path, consoleFileMaxBytes, consoleFileBackups)
	if err != nil {
		consoleFileMu.Unlock()
		return err
	}
	if consoleFileWriter != nil {
		_ = consoleFileWriter.Close()
	}
	consoleFileWriter = w
	consoleFilePath = path
	consoleFileMu.Unlock()

	hydrateConsoleRing(readConsoleFileEntries(path))
	return nil
}

// ConsoleFileEnabled reports whether the console file store is live.
func ConsoleFileEnabled() bool {
	consoleFileMu.Lock()
	defer consoleFileMu.Unlock()
	return consoleFileWriter != nil
}

// persistConsoleEntry appends one captured entry to the console file.
// Called from captureConsole for every entry; a nil writer (file store
// disabled, or a test process) makes it a no-op.
func persistConsoleEntry(e ConsoleEntry) {
	consoleFileMu.Lock()
	w := consoleFileWriter
	consoleFileMu.Unlock()
	if w == nil {
		return
	}
	b, err := e.MarshalJSON()
	if err != nil {
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

// SearchConsoleFile returns the retained entries whose line contains
// needle, oldest first, capped at the newest consoleSearchMaxHits
// matches. The second result reports whether the file store is enabled;
// when it is not, the caller falls back to the ring buffer.
func SearchConsoleFile(needle string) ([]ConsoleEntry, bool) {
	consoleFileMu.Lock()
	path, enabled := consoleFilePath, consoleFileWriter != nil
	consoleFileMu.Unlock()
	if !enabled {
		return nil, false
	}
	entries := readConsoleFileEntries(path)
	out := make([]ConsoleEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(e.Line, needle) {
			out = append(out, e)
		}
	}
	if len(out) > consoleSearchMaxHits {
		out = out[len(out)-consoleSearchMaxHits:]
	}
	return out, true
}

// resetConsoleFile wipes the retained console files and reopens a fresh
// writer, so a dashboard clear means the trail is gone from disk too —
// not quietly resurrected by the next file search or restart. Called
// from ClearConsoleLogs; a nil writer makes it a no-op.
func resetConsoleFile() {
	consoleFileMu.Lock()
	defer consoleFileMu.Unlock()
	if consoleFileWriter == nil {
		return
	}
	_ = consoleFileWriter.Close()
	consoleFileWriter = nil
	for _, p := range rotatedPaths(consoleFilePath) {
		_ = os.Remove(p)
	}
	w, err := newRotatingFileWriter(consoleFilePath, consoleFileMaxBytes, consoleFileBackups)
	if err != nil {
		return // store stays disabled; the ring still works
	}
	consoleFileWriter = w
}

// hydrateConsoleRing prepends retained file entries ahead of whatever
// the ring already holds (boot lines emitted before the store was
// enabled) and keeps the newest consoleMaxLines, mirroring the ring's
// own eviction rule.
func hydrateConsoleRing(fileEntries []ConsoleEntry) {
	if len(fileEntries) == 0 {
		return
	}
	consoleMu.Lock()
	defer consoleMu.Unlock()
	merged := make([]ConsoleEntry, 0, len(fileEntries)+len(consoleLogs))
	merged = append(merged, fileEntries...)
	merged = append(merged, consoleLogs...)
	if len(merged) > consoleMaxLines {
		merged = merged[len(merged)-consoleMaxLines:]
	}
	consoleLogs = merged
}

// rotatedPaths lists the retained files for path oldest first: the
// highest backup number is the oldest, the live file the newest.
func rotatedPaths(path string) []string {
	out := make([]string, 0, consoleFileBackups+1)
	for i := consoleFileBackups; i >= 1; i-- {
		out = append(out, fmt.Sprintf("%s.%d", path, i))
	}
	return append(out, path)
}

// readConsoleFileEntries parses every retained console file oldest
// first. Lines that fail to parse (a torn tail from a crash mid-write)
// are skipped, never fatal.
func readConsoleFileEntries(path string) []ConsoleEntry {
	var out []ConsoleEntry
	for _, p := range rotatedPaths(path) {
		out = append(out, readConsoleFile(p)...)
	}
	return out
}

func readConsoleFile(path string) []ConsoleEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []ConsoleEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), consoleScanLineCap)
	for sc.Scan() {
		var wire struct {
			Time  string `json:"time"`
			Level string `json:"level"`
			Line  string `json:"line"`
		}
		if err := json.Unmarshal(sc.Bytes(), &wire); err != nil {
			continue
		}
		lvl, ok := ParseLevel(wire.Level)
		if !ok {
			lvl = LevelInfo
		}
		ts, _ := time.Parse(time.RFC3339Nano, wire.Time)
		out = append(out, ConsoleEntry{Time: ts, Level: lvl, Line: wire.Line})
	}
	return out
}
