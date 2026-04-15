package log_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jkleinne/shuttle/internal/log"
)

func TestLogger_WritesToBothStreams(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, false)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Info("hello world")

	termOut := termBuf.String()
	if !strings.Contains(termOut, "hello world") {
		t.Errorf("terminal missing message, got: %q", termOut)
	}

	fileBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	fileOut := string(fileBytes)
	if !strings.Contains(fileOut, "[INFO] hello world") {
		t.Errorf("log file missing message, got: %q", fileOut)
	}
}

func TestLogger_FileOutput_NoAnsiCodes(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, true)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Error("something broke")

	fileBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	fileOut := string(fileBytes)
	if strings.Contains(fileOut, "\033[") {
		t.Errorf("log file contains ANSI codes: %q", fileOut)
	}
	if !strings.Contains(fileOut, "[ERROR] something broke") {
		t.Errorf("log file missing message, got: %q", fileOut)
	}
}

func TestLogger_TerminalColor_WhenEnabled(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, true)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Error("fail")

	termOut := termBuf.String()
	if !strings.Contains(termOut, "\033[") {
		t.Errorf("terminal missing ANSI codes when color enabled: %q", termOut)
	}
}

func TestLogger_AllMethods(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, false)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Header("section")
	logger.Info("info msg")
	logger.Success("ok msg")
	logger.Warn("warn msg")
	logger.Error("err msg")

	fileBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	fileOut := string(fileBytes)

	for _, want := range []string{"==> section", "[INFO] info msg", "[OK] ok msg", "[WARN] warn msg", "[ERROR] err msg"} {
		if !strings.Contains(fileOut, want) {
			t.Errorf("log file missing %q", want)
		}
	}
}

func TestNew_CreatesDirectoryAndFile(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "nested", "logs")
	logger, logPath, err := log.New(logDir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	if logPath == "" {
		t.Fatal("logPath is empty")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	// Verify it's writable.
	logger.Info("test message")
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if !strings.Contains(string(content), "[INFO] test message") {
		t.Errorf("log content missing message: %q", string(content))
	}
}

func TestLogPath_ReturnsFilePath(t *testing.T) {
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")
	logger, err := log.NewWithWriter(&bytes.Buffer{}, logFile, false)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()
	if logger.LogPath() != logFile {
		t.Errorf("LogPath() = %q, want %q", logger.LogPath(), logFile)
	}
}

func TestPruneOldLogs_EmptyDir_NoOp(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	deleted, warnings, err := log.PruneOldLogs(dir, 30, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}

func TestPruneOldLogs_RetentionZero_SkipsPruning(t *testing.T) {
	dir := t.TempDir()
	// Create an ancient log file; with retention=0, nothing should be deleted.
	ancient := filepath.Join(dir, "2000-01-01_000000.log")
	if err := os.WriteFile(ancient, []byte("old"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	deleted, _, err := log.PruneOldLogs(dir, 0, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 when retention is 0", deleted)
	}
	if _, err := os.Stat(ancient); err != nil {
		t.Errorf("ancient log should still exist, stat error: %v", err)
	}
}

func TestPruneOldLogs_DeletesOnlyStaleMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	files := map[string]bool{
		// Within retention (keep)
		"2026-04-10_120000.log": true,
		"2026-04-14_235959.log": true,
		// Older than retention (delete)
		"2026-03-15_120000.log": false,
		"2025-12-01_000000.log": false,
		// Non-matching names (leave alone regardless of age)
		"README.txt":                 true,
		"random.log":                 true,
		"2026-04-10.log":             true, // missing time component
		"2026-04-10_12000.log":       true, // wrong time length
		"2026-04-10_120000.log.bak":  true,
	}
	for name := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	deleted, warnings, err := log.PruneOldLogs(dir, 30, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	for name, shouldExist := range files {
		_, statErr := os.Stat(filepath.Join(dir, name))
		exists := statErr == nil
		if exists != shouldExist {
			t.Errorf("%s: exists=%v, want exists=%v", name, exists, shouldExist)
		}
	}
}

func TestPruneOldLogs_BoundaryAtRetentionEdge(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	// Exactly 30 days old at 00:00:00 → age == retention; keep (not strictly greater).
	edgeKeep := "2026-03-16_120000.log"
	// 30 days + 1 second old → strictly older than retention; delete.
	edgeDelete := "2026-03-16_115959.log"

	for _, name := range []string{edgeKeep, edgeDelete} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	deleted, _, err := log.PruneOldLogs(dir, 30, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, edgeKeep)); err != nil {
		t.Errorf("edge-keep file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, edgeDelete)); !os.IsNotExist(err) {
		t.Errorf("edge-delete file still present, stat err: %v", err)
	}
}

func TestPruneOldLogs_UnreadableDir_ReturnsError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	_, _, err := log.PruneOldLogs(dir, 30, now)
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
}

func TestLogger_FileOnly_SkipsTerminal(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, false)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.FileHeader("section")
	logger.FileInfo("info msg")
	logger.FileError("err msg")

	termOut := termBuf.String()
	if termOut != "" {
		t.Errorf("terminal should be empty for file-only methods, got: %q", termOut)
	}

	fileBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	fileOut := string(fileBytes)

	for _, want := range []string{"==> section", "[INFO] info msg", "[ERROR] err msg"} {
		if !strings.Contains(fileOut, want) {
			t.Errorf("log file missing %q", want)
		}
	}
}
