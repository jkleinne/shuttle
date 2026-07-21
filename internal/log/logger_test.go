package log_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jkleinne/shuttle/internal/log"
)

func TestLogger_WritesToBothStreams(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
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

func TestLogger_SanitizesMessageText(t *testing.T) {
	const hostile = "trusted\nforged\x1b]0;title\a\u009b31m\x7f\u061c\u200e\u202e\u2066\u2069"
	const sanitized = "trustedforged]0;title31m"
	tests := []struct {
		name         string
		verbosity    log.Verbosity
		fileFrame    string
		wantTerminal bool
		write        func(*log.Logger)
	}{
		{
			name:         "header",
			verbosity:    log.VerbosityNormal,
			fileFrame:    "==> ",
			wantTerminal: true,
			write:        func(logger *log.Logger) { logger.Header(hostile) },
		},
		{
			name:         "info",
			verbosity:    log.VerbosityNormal,
			fileFrame:    "[INFO] ",
			wantTerminal: true,
			write:        func(logger *log.Logger) { logger.Info(hostile) },
		},
		{
			name:         "success",
			verbosity:    log.VerbosityNormal,
			fileFrame:    "[OK] ",
			wantTerminal: true,
			write:        func(logger *log.Logger) { logger.Success(hostile) },
		},
		{
			name:         "warn",
			verbosity:    log.VerbosityNormal,
			fileFrame:    "[WARN] ",
			wantTerminal: true,
			write:        func(logger *log.Logger) { logger.Warn(hostile) },
		},
		{
			name:         "error",
			verbosity:    log.VerbosityNormal,
			fileFrame:    "[ERROR] ",
			wantTerminal: true,
			write:        func(logger *log.Logger) { logger.Error(hostile) },
		},
		{
			name:         "debug",
			verbosity:    log.VerbosityVerbose,
			fileFrame:    "[DEBUG] ",
			wantTerminal: true,
			write:        func(logger *log.Logger) { logger.Debug(hostile) },
		},
		{
			name:      "file header",
			verbosity: log.VerbosityNormal,
			fileFrame: "==> ",
			write:     func(logger *log.Logger) { logger.FileHeader(hostile) },
		},
		{
			name:      "file info",
			verbosity: log.VerbosityNormal,
			fileFrame: "[INFO] ",
			write:     func(logger *log.Logger) { logger.FileInfo(hostile) },
		},
		{
			name:      "file warn",
			verbosity: log.VerbosityNormal,
			fileFrame: "[WARN] ",
			write:     func(logger *log.Logger) { logger.FileWarn(hostile) },
		},
		{
			name:      "file error",
			verbosity: log.VerbosityNormal,
			fileFrame: "[ERROR] ",
			write:     func(logger *log.Logger) { logger.FileError(hostile) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var terminal bytes.Buffer
			logFile := filepath.Join(t.TempDir(), "test.log")
			logger, err := log.NewWithWriter(&terminal, logFile, log.Options{
				Verbosity: test.verbosity,
			})
			if err != nil {
				t.Fatalf("NewWithWriter: %v", err)
			}
			test.write(logger)
			logger.Close()

			terminalOutput := terminal.String()
			if test.wantTerminal && !strings.Contains(terminalOutput, sanitized) {
				t.Errorf("terminal output = %q, want sanitized message %q", terminalOutput, sanitized)
			}
			if !test.wantTerminal && terminalOutput != "" {
				t.Errorf("terminal output = %q, want empty", terminalOutput)
			}

			fileBytes, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatalf("reading log file: %v", err)
			}
			fileOutput := string(fileBytes)
			if want := test.fileFrame + sanitized + "\n"; !strings.HasSuffix(fileOutput, want) {
				t.Errorf("file output = %q, want suffix %q", fileOutput, want)
			}
			if got := strings.Count(fileOutput, "\n"); got != 1 {
				t.Errorf("file newline count = %d, want exactly 1; output: %q", got, fileOutput)
			}
			for _, control := range []string{
				"\nforged",
				"\x1b",
				"\a",
				"\u009b",
				"\x7f",
				"\u061c",
				"\u200e",
				"\u202e",
				"\u2066",
				"\u2069",
			} {
				if strings.Contains(terminalOutput, control) || strings.Contains(fileOutput, control) {
					t.Errorf("output retains hostile control %q: terminal=%q file=%q", control, terminalOutput, fileOutput)
				}
			}
		})
	}
}

func TestLogger_FileTool_SanitizesAndSerializesConcurrentRecords(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(&bytes.Buffer{}, logFile, log.Options{})
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	const recordsPerSource = 100
	sources := []struct {
		identity log.ToolSource
		name     string
	}{
		{identity: log.ToolRsync, name: "rsync"},
		{identity: log.ToolRclone, name: "rclone"},
	}
	expected := make(map[string]bool, recordsPerSource*2)
	for _, source := range sources {
		for sequence := range recordsPerSource {
			record := fmt.Sprintf(
				"[%s] record-%s-%03dforged",
				strings.ToUpper(source.name),
				source.name,
				sequence,
			)
			expected[record] = true
		}
	}
	var writers sync.WaitGroup
	for _, source := range sources {
		source := source
		writers.Add(1)
		go func() {
			defer writers.Done()
			for sequence := range recordsPerSource {
				logger.FileTool(
					source.identity,
					fmt.Sprintf("record-%s-%03d\nforged\x1b\u009b\x7f", source.name, sequence),
				)
			}
		}()
	}
	writers.Wait()
	logger.Close()

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if got, want := len(lines), recordsPerSource*2; got != want {
		t.Fatalf("line count = %d, want %d", got, want)
	}
	frame := regexp.MustCompile(
		`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\] (\[(?:RSYNC|RCLONE)\] record-(?:rsync|rclone)-\d{3}forged)$`,
	)
	seen := make(map[string]int, len(expected))
	for lineNumber, line := range lines {
		match := frame.FindStringSubmatch(line)
		if match == nil {
			t.Errorf("line %d is not one complete trusted tool frame: %q", lineNumber+1, line)
			continue
		}
		if got := strings.Count(line, "[RSYNC]") + strings.Count(line, "[RCLONE]"); got != 1 {
			t.Errorf("line %d tool frame count = %d, want exactly 1: %q", lineNumber+1, got, line)
		}
		seen[match[1]]++
	}
	for record := range expected {
		if got := seen[record]; got != 1 {
			t.Errorf("trusted record %q count = %d, want exactly 1", record, got)
		}
	}
}

func TestLogger_FileTool_UnexpectedSourceUsesTrustedErrorFrame(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(&bytes.Buffer{}, logFile, log.Options{})
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}

	logger.FileTool(log.ToolSource(0), "unknown\nrecord\x1b")
	logger.FileTool(log.ToolSource(255), "mutated\nrecord\u009b\x7f")
	logger.Close()

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if got, want := len(lines), 2; got != want {
		t.Fatalf("line count = %d, want %d", got, want)
	}
	frame := regexp.MustCompile(
		`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\] \[ERROR\] unexpected tool source (?:0|255): (?:unknownrecord|mutatedrecord)$`,
	)
	for lineNumber, line := range lines {
		if !frame.MatchString(line) {
			t.Errorf("line %d is not a trusted fallback frame: %q", lineNumber+1, line)
		}
		for _, forbidden := range []string{"[0]", "[255]"} {
			if strings.Contains(line, forbidden) {
				t.Errorf("line %d contains dynamic tool frame %q: %q", lineNumber+1, forbidden, line)
			}
		}
	}
}

func TestLogger_FileOutput_NoAnsiCodes(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, log.Options{
		UseColor:  true,
		Verbosity: log.VerbosityNormal,
	})
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

	logger, err := log.NewWithWriter(&termBuf, logFile, log.Options{
		UseColor:  true,
		Verbosity: log.VerbosityNormal,
	})
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

	logger, err := log.NewWithWriter(&termBuf, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
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
	logger, logPath, err := log.New(logDir, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
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

func TestNew_CreatesPrivateDirectoryAndFile(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	logger, logPath, err := log.New(logDir, log.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	directoryInfo, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("stating log directory: %v", err)
	}
	if got, want := directoryInfo.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Errorf("log directory permissions = %o, want %o", got, want)
	}

	fileInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stating log file: %v", err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("log file permissions = %o, want %o", got, want)
	}
}

func TestNew_TightensExistingOwnedDirectory(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(logDir, 0o755); err != nil {
		t.Fatalf("creating log directory fixture: %v", err)
	}
	if err := os.Chmod(logDir, 0o755); err != nil {
		t.Fatalf("setting log directory fixture permissions: %v", err)
	}

	logger, _, err := log.New(logDir, log.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("stating log directory: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Errorf("log directory permissions = %o, want %o", got, want)
	}
}

func TestNew_CreatesDistinctProductionPaths(t *testing.T) {
	logDir := t.TempDir()
	firstLogger, firstPath, err := log.New(logDir, log.Options{})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	firstLogger.Close()

	secondLogger, secondPath, err := log.New(logDir, log.Options{})
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	secondLogger.Close()

	if firstPath == secondPath {
		t.Errorf("production log paths collided: %q", firstPath)
	}
	filenamePattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{6}-[A-Za-z0-9]+\.log$`)
	for _, path := range []string{firstPath, secondPath} {
		if !filenamePattern.MatchString(filepath.Base(path)) {
			t.Errorf("production log filename %q does not match timestamp and alphanumeric suffix pattern", filepath.Base(path))
		}
	}
}

func TestNewWithWriter_RejectsCollisionWithoutTruncation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "exact.log")
	original := []byte("preserve this exact content")
	if err := os.WriteFile(logPath, original, 0o600); err != nil {
		t.Fatalf("writing existing log file: %v", err)
	}

	logger, err := log.NewWithWriter(&bytes.Buffer{}, logPath, log.Options{})
	if logger != nil {
		logger.Close()
	}
	if err == nil {
		t.Fatal("NewWithWriter returned nil error for an existing path")
	}
	if !strings.Contains(err.Error(), "creating log file") || !strings.Contains(err.Error(), logPath) {
		t.Errorf("collision error lacks creation context: %v", err)
	}

	after, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("reading existing log file after collision: %v", readErr)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("existing log file changed: got %q, want %q", after, original)
	}
}

func TestLogDirectory_NonDirectoryHasContext(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "not-a-directory")
	original := []byte("unchanged")
	if err := os.WriteFile(logPath, original, 0o600); err != nil {
		t.Fatalf("writing non-directory fixture: %v", err)
	}

	logger, _, newErr := log.New(logPath, log.Options{})
	if logger != nil {
		logger.Close()
	}
	if newErr == nil {
		t.Fatal("New returned nil error for a non-directory log path")
	}
	if !strings.Contains(newErr.Error(), "log directory") || !strings.Contains(newErr.Error(), "not a directory") {
		t.Errorf("New error lacks non-directory context: %v", newErr)
	}

	_, _, pruneErr := log.PruneOldLogs(logPath, 30, time.Now())
	if pruneErr == nil {
		t.Fatal("PruneOldLogs returned nil error for a non-directory log path")
	}
	if !strings.Contains(pruneErr.Error(), "log directory") || !strings.Contains(pruneErr.Error(), "not a directory") {
		t.Errorf("PruneOldLogs error lacks non-directory context: %v", pruneErr)
	}

	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading non-directory fixture after calls: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("non-directory fixture changed: got %q, want %q", after, original)
	}
}

func TestLogDirectory_RejectsSymlinkBeforeMutation(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("creating symlink target: %v", err)
	}
	logDir := filepath.Join(parent, "logs")
	if err := os.Symlink(target, logDir); err != nil {
		t.Fatalf("creating log directory symlink: %v", err)
	}

	logger, _, newErr := log.New(logDir, log.Options{})
	if logger != nil {
		logger.Close()
	}
	if newErr == nil || !strings.Contains(newErr.Error(), "symlink") {
		t.Errorf("New error = %v, want symlink context", newErr)
	}

	_, _, pruneErr := log.PruneOldLogs(logDir, 30, time.Now())
	if pruneErr == nil || !strings.Contains(pruneErr.Error(), "symlink") {
		t.Errorf("PruneOldLogs error = %v, want symlink context", pruneErr)
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("reading symlink target: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("symlink target mutated, entries = %v", entries)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("checking symlink target mode: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("symlink target mode = %o, want unchanged 755", got)
	}
}

func TestLogPath_ReturnsFilePath(t *testing.T) {
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")
	logger, err := log.NewWithWriter(&bytes.Buffer{}, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
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
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	deleted, warnings, err := log.PruneOldLogs(dir, 30, asOf)
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

func TestPruneOldLogs_TightensExistingOwnedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("setting log directory fixture permissions: %v", err)
	}

	_, _, err := log.PruneOldLogs(dir, 30, time.Now())
	if err != nil {
		t.Fatalf("PruneOldLogs: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stating log directory: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Errorf("log directory permissions = %o, want %o", got, want)
	}
}

func TestPruneOldLogs_RetentionZero_SkipsPruning(t *testing.T) {
	dir := t.TempDir()
	// Create an ancient log file; with retention=0, nothing should be deleted.
	ancient := filepath.Join(dir, "2000-01-01_000000.log")
	if err := os.WriteFile(ancient, []byte("old"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	deleted, _, err := log.PruneOldLogs(dir, 0, asOf)
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
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)

	// Files that must survive the prune — some because their name doesn't
	// match the shuttle log pattern, some because they are within retention.
	keep := []string{
		"2026-04-10_120000.log",     // within retention
		"2026-04-14_235959.log",     // within retention
		"README.txt",                // non-matching: wrong extension
		"random.log",                // non-matching: missing timestamp
		"2026-04-10.log",            // non-matching: missing time component
		"2026-04-10_12000.log",      // non-matching: wrong time length
		"2026-04-10_120000.log.bak", // non-matching: trailing suffix
	}
	// Files that must be deleted — match the pattern and are older than
	// retention.
	deleteExpected := []string{
		"2026-03-15_120000.log",
		"2025-12-01_000000.log",
	}

	for _, name := range append(keep, deleteExpected...) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	deleted, warnings, err := log.PruneOldLogs(dir, 30, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if deleted != len(deleteExpected) {
		t.Errorf("deleted = %d, want %d", deleted, len(deleteExpected))
	}

	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: expected to survive prune, stat err: %v", name, err)
		}
	}
	for _, name := range deleteExpected {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s: expected to be pruned, stat err: %v", name, err)
		}
	}
}

func TestPruneOldLogs_AcceptsLegacyAndUniqueNames(t *testing.T) {
	dir := t.TempDir()
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	deleteExpected := []string{
		"2025-12-01_000000.log",
		"2025-12-01_000000-aB9Z.log",
	}
	keepExpected := []string{
		"2026-04-14_000000-recent7.log",
		"2025-12-01_000000-under_score.log",
		"2025-12-01_000000-two-parts.log",
		"2025-12-01_000000-.log",
		"unrelated.log",
	}
	for _, name := range append(deleteExpected, keepExpected...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	deleted, warnings, err := log.PruneOldLogs(dir, 30, asOf)
	if err != nil {
		t.Fatalf("PruneOldLogs: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if deleted != len(deleteExpected) {
		t.Errorf("deleted = %d, want %d", deleted, len(deleteExpected))
	}
	for _, name := range deleteExpected {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(statErr) {
			t.Errorf("%s: expected deletion, stat error = %v", name, statErr)
		}
	}
	for _, name := range keepExpected {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Errorf("%s: expected preservation, stat error = %v", name, statErr)
		}
	}
}

func TestPruneOldLogs_BoundaryAtRetentionEdge(t *testing.T) {
	dir := t.TempDir()
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)

	// Exactly 30 days old at 00:00:00 → age == retention; keep (not strictly greater).
	edgeKeep := "2026-03-16_120000.log"
	// 30 days + 1 second old → strictly older than retention; delete.
	edgeDelete := "2026-03-16_115959.log"

	for _, name := range []string{edgeKeep, edgeDelete} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	deleted, _, err := log.PruneOldLogs(dir, 30, asOf)
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

// TestPruneOldLogs_NonUTCZone_RespectsAsOfZone asserts that a log
// filename is interpreted in the same zone the caller passes via asOf.
// Before the fix, parsing defaulted to UTC and shifted retention boundaries
// by the caller's UTC offset, causing up-to-24h skew for anyone outside UTC.
func TestPruneOldLogs_NonUTCZone_RespectsAsOfZone(t *testing.T) {
	dir := t.TempDir()
	loc := time.FixedZone("test", -8*60*60)

	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, loc)

	// 29d23h ago in the same zone: must survive a 30-day retention. Under
	// UTC-default parsing, the filename would be read 8h earlier than it
	// was written, pushing it past the 30-day boundary.
	writeTime := asOf.Add(-29*24*time.Hour - 23*time.Hour)
	name := writeTime.Format("2006-01-02_150405") + ".log"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	deleted, _, err := log.PruneOldLogs(dir, 30, asOf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 0 {
		t.Errorf("file aged 29d23h should not be pruned, deleted = %d", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Errorf("file should still exist: %v", err)
	}
}

func TestPruneOldLogs_NonexistentDir_IsNoOp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	deleted, warnings, err := log.PruneOldLogs(dir, 30, asOf)
	if err != nil {
		t.Fatalf("unexpected error for missing dir: %v", err)
	}
	if deleted != 0 || len(warnings) != 0 {
		t.Errorf("expected zero effect for missing dir, got deleted=%d warnings=%v", deleted, warnings)
	}
	if _, statErr := os.Lstat(dir); !os.IsNotExist(statErr) {
		t.Errorf("missing retention directory was created, lstat error = %v", statErr)
	}
}

func TestPruneOldLogs_UnreadableDir_ReturnsError(t *testing.T) {
	// A regular file standing in where a directory is expected: ReadDir
	// fails with ENOTDIR, which is not IsNotExist, so we surface an error.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing blocker: %v", err)
	}
	asOf := time.Date(2026, 4, 15, 12, 0, 0, 0, time.Local)
	_, _, err := log.PruneOldLogs(blocker, 30, asOf)
	if err == nil {
		t.Fatal("expected error when ReadDir fails on a non-directory, got nil")
	}
}

func TestLogger_Quiet_SuppressesAllButError(t *testing.T) {
	var stdoutBuf, stderrBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&stdoutBuf, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityQuiet,
	})
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()
	logger.SetStderr(&stderrBuf)

	logger.Header("section")
	logger.Info("info msg")
	logger.Success("ok msg")
	logger.Warn("warn msg")
	logger.Error("err msg")

	if stdoutBuf.Len() != 0 {
		t.Errorf("quiet stdout should be empty, got %q", stdoutBuf.String())
	}
	stderrOut := stderrBuf.String()
	if strings.Contains(stderrOut, "warn msg") {
		t.Errorf("quiet stderr should suppress warnings, got %q", stderrOut)
	}
	if !strings.Contains(stderrOut, "err msg") {
		t.Errorf("quiet stderr should still emit errors, got %q", stderrOut)
	}

	fileBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	fileOut := string(fileBytes)
	for _, wanted := range []string{"==> section", "[INFO] info msg", "[OK] ok msg", "[WARN] warn msg", "[ERROR] err msg"} {
		if !strings.Contains(fileOut, wanted) {
			t.Errorf("file should contain %q regardless of verbosity", wanted)
		}
	}
}

func TestLogger_WarnErrorGoToStderr(t *testing.T) {
	var stdoutBuf, stderrBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&stdoutBuf, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()
	logger.SetStderr(&stderrBuf)

	logger.Warn("heads up")
	logger.Error("broke")

	if strings.Contains(stdoutBuf.String(), "heads up") || strings.Contains(stdoutBuf.String(), "broke") {
		t.Errorf("warn/error should not land on stdout, got %q", stdoutBuf.String())
	}
	if !strings.Contains(stderrBuf.String(), "heads up") {
		t.Errorf("stderr should contain warning, got %q", stderrBuf.String())
	}
	if !strings.Contains(stderrBuf.String(), "broke") {
		t.Errorf("stderr should contain error, got %q", stderrBuf.String())
	}
}

func TestLogger_Debug_OnlyInVerboseMode(t *testing.T) {
	tests := []struct {
		name       string
		verbosity  log.Verbosity
		wantOnTerm bool
	}{
		{"normal suppresses Debug terminal", log.VerbosityNormal, false},
		{"quiet suppresses Debug terminal", log.VerbosityQuiet, false},
		{"verbose shows Debug on terminal", log.VerbosityVerbose, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var termBuf bytes.Buffer
			logDir := t.TempDir()
			logFile := filepath.Join(logDir, "test.log")

			logger, err := log.NewWithWriter(&termBuf, logFile, log.Options{
				UseColor:  false,
				Verbosity: tc.verbosity,
			})
			if err != nil {
				t.Fatalf("NewWithWriter: %v", err)
			}
			defer logger.Close()

			logger.Debug("exec: rsync -a src dst")

			termOut := termBuf.String()
			hasOnTerm := strings.Contains(termOut, "exec: rsync -a src dst")
			if hasOnTerm != tc.wantOnTerm {
				t.Errorf("terminal has debug output = %v, want %v; got: %q", hasOnTerm, tc.wantOnTerm, termOut)
			}

			fileBytes, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatalf("reading log file: %v", err)
			}
			if !strings.Contains(string(fileBytes), "[DEBUG] exec: rsync -a src dst") {
				t.Errorf("file should always contain debug line, got: %q", string(fileBytes))
			}
		})
	}
}

func TestLogger_FileOnly_SkipsTerminal(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.FileHeader("section")
	logger.FileInfo("info msg")
	logger.FileWarn("warn msg")
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

	for _, want := range []string{"==> section", "[INFO] info msg", "[WARN] warn msg", "[ERROR] err msg"} {
		if !strings.Contains(fileOut, want) {
			t.Errorf("log file missing %q", want)
		}
	}
}
