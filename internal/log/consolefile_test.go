package log

import (
	"path/filepath"
	"strings"
	"testing"
)

// disableConsoleFileForTest closes the store a test enabled and restores
// the package globals, so sibling tests keep running ring-only.
func disableConsoleFileForTest() {
	consoleFileMu.Lock()
	defer consoleFileMu.Unlock()
	if consoleFileWriter != nil {
		_ = consoleFileWriter.Close()
		consoleFileWriter = nil
	}
	consoleFilePath = ""
}

func TestConsoleFile_PersistSearchHydrateClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.jsonl")
	if err := EnableConsoleFileLog(path); err != nil {
		t.Fatalf("EnableConsoleFileLog: %v", err)
	}
	t.Cleanup(disableConsoleFileForTest)
	if !ConsoleFileEnabled() {
		t.Fatal("console file store not enabled")
	}
	ClearConsoleLogs() // fresh file + empty ring

	Info("test", "trail start", "req_id", "req_filetest_1")
	Info("test", "unrelated line")
	Warn("test", "trail end", "req_id", "req_filetest_1")

	found, ok := SearchConsoleFile("req_filetest_1")
	if !ok {
		t.Fatal("SearchConsoleFile reports store disabled")
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 trail lines from file, got %d: %v", len(found), found)
	}
	if !strings.Contains(found[0].Line, "trail start") || !strings.Contains(found[1].Line, "trail end") {
		t.Errorf("file search out of order or incomplete: %q, %q", found[0].Line, found[1].Line)
	}
	if found[1].Level != LevelWarn {
		t.Errorf("level round-trip = %s, want warn", found[1].Level)
	}

	// Restart shape: an empty ring rehydrated from disk sees the trail.
	consoleMu.Lock()
	consoleLogs = nil
	consoleMu.Unlock()
	hydrateConsoleRing(readConsoleFileEntries(path))
	if !contains(ConsoleEntries(), "unrelated line") {
		t.Errorf("hydrated ring missing persisted lines: %v", ConsoleEntries())
	}

	// Clear wipes the file too: the trail must not come back.
	ClearConsoleLogs()
	found, ok = SearchConsoleFile("req_filetest_1")
	if !ok {
		t.Fatal("SearchConsoleFile reports store disabled after clear")
	}
	if len(found) != 0 {
		t.Errorf("expected no matches after clear, got %d", len(found))
	}
}

// The operator rule is one number for every log rotation in the tree:
// 4 MiB per file. Pin both rotating writers to it.
func TestLogRotation_Is4MiB(t *testing.T) {
	if consoleFileMaxBytes != 4<<20 {
		t.Errorf("consoleFileMaxBytes = %d, want 4 MiB", consoleFileMaxBytes)
	}
	if defaultLogFileMaxBytes != 4<<20 {
		t.Errorf("defaultLogFileMaxBytes = %d, want 4 MiB", defaultLogFileMaxBytes)
	}
}
