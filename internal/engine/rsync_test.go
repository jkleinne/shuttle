package engine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

func newTestLogger(t *testing.T) *log.Logger {
	t.Helper()
	logger, _ := newTestLoggerWithPath(t)
	return logger
}

func newTestLoggerWithPath(t *testing.T) (*log.Logger, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(os.Stdout, logPath, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
	if err != nil {
		t.Fatalf("creating test logger: %v", err)
	}
	t.Cleanup(func() { logger.Close() })
	return logger, logPath
}

func assertPrimaryLogFrames(t *testing.T, logPath, tool string) string {
	t.Helper()
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading primary log: %v", err)
	}
	text := string(content)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("primary log has no trusted records")
	}
	frame := regexp.MustCompile(
		`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\] \[` + regexp.QuoteMeta(tool) + `\] `,
	)
	for lineNumber, line := range lines {
		if !frame.MatchString(line) {
			t.Errorf("primary log line %d is not a trusted %s frame: %q", lineNumber+1, tool, line)
		}
		for _, value := range line {
			if unicode.IsControl(value) {
				t.Errorf("primary log line %d retains control U+%04X: %q", lineNumber+1, value, line)
			}
		}
	}
	return text
}

func TestRsyncExec_TransfersFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	defaults := &config.RsyncDefaults{Flags: []string{"-a", "-v", "-h", "-P"}}
	job := config.Job{}
	args := BuildRsyncArgs(RsyncArgsRequest{Defaults: defaults, Job: job, Source: src + "/", Destination: dst + "/"})

	executor := NewRsyncExecutor(newTestLogger(t))
	result := executor.Exec(context.Background(), args, nil)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Stats.FilesTransferred != 1 {
		t.Errorf("FilesTransferred = %d, want 1", result.Stats.FilesTransferred)
	}
	content, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("reading synced file: %v", err)
	}
	if string(content) != "world" {
		t.Errorf("file content = %q, want world", string(content))
	}
}

func TestRsyncExec_HumanReadableProgressCallsBack(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	const payloadSizeWithHumanReadableUnit = 128 * 1024
	content := bytes.Repeat([]byte("x"), payloadSizeWithHumanReadableUnit)
	if err := os.WriteFile(filepath.Join(src, "payload.bin"), content, 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	args := BuildRsyncArgs(RsyncArgsRequest{
		Defaults:    &config.RsyncDefaults{Flags: []string{"-a", "-h"}},
		Source:      src + "/",
		Destination: dst + "/",
	})
	var progress []string

	result := NewRsyncExecutor(newTestLogger(t)).Exec(
		context.Background(),
		args,
		func(text string) { progress = append(progress, text) },
	)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", result.Status, StatusOK)
	}
	if len(progress) == 0 {
		t.Fatal("human-readable rsync progress produced no callback")
	}
}

func TestRsyncExec_DryRun_DoesNotTransfer(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	defaults := &config.RsyncDefaults{Flags: []string{"-a", "-v", "-h", "-P"}}
	args := BuildRsyncArgs(RsyncArgsRequest{Defaults: defaults, Source: src + "/", Destination: dst + "/", DryRun: true})

	executor := NewRsyncExecutor(newTestLogger(t))
	result := executor.Exec(context.Background(), args, nil)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("reading dst directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dst has %d entries, want 0 (dry run)", len(entries))
	}
}

func TestRsyncExec_DeleteAfter_ForDirectories(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("remove me"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	defaults := &config.RsyncDefaults{Flags: []string{"-a", "-v", "-h", "-P"}}
	job := config.Job{Delete: true}
	args := BuildRsyncArgs(RsyncArgsRequest{Defaults: defaults, Job: job, Source: src + "/", Destination: dst + "/", IsDeleteDir: true})

	executor := NewRsyncExecutor(newTestLogger(t))
	result := executor.Exec(context.Background(), args, nil)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Error("stale.txt should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.txt")); err != nil {
		t.Error("keep.txt should exist")
	}
}

func TestRsyncExec_ExtraOpts_Applied(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "include.txt"), []byte("yes"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, ".hidden"), []byte("no"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	defaults := &config.RsyncDefaults{Flags: []string{"-a", "-v", "-h", "-P"}}
	job := config.Job{ExtraFlags: []string{"--exclude=.*"}}
	args := BuildRsyncArgs(RsyncArgsRequest{Defaults: defaults, Job: job, Source: src + "/", Destination: dst + "/"})

	executor := NewRsyncExecutor(newTestLogger(t))
	result := executor.Exec(context.Background(), args, nil)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if _, err := os.Stat(filepath.Join(dst, ".hidden")); !os.IsNotExist(err) {
		t.Error(".hidden should have been excluded")
	}
	if _, err := os.Stat(filepath.Join(dst, "include.txt")); err != nil {
		t.Error("include.txt should exist")
	}
}

func TestRsyncExec_HostileFilenameTransfersWithTrustedLogFrames(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	hostileNames := []string{
		"trusted\n[2099-01-01 00:00:00] [ERROR] forged\x1b\u009b\x7f.txt",
		"report 45% (xfr#999).txt",
	}
	for _, hostileName := range hostileNames {
		if err := os.WriteFile(filepath.Join(src, hostileName), []byte("content"), 0o600); err != nil {
			t.Fatalf("writing hostile filename %q: %v", hostileName, err)
		}
	}
	logger, logPath := newTestLoggerWithPath(t)
	args := BuildRsyncArgs(RsyncArgsRequest{
		Defaults:    &config.RsyncDefaults{Flags: []string{"-a"}},
		Source:      src + "/",
		Destination: dst + "/",
	})
	var progress []string

	result := NewRsyncExecutor(logger).Exec(
		context.Background(),
		args,
		func(text string) { progress = append(progress, text) },
	)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", result.Status, StatusOK)
	}
	if result.Stats.FilesTransferred != len(hostileNames) {
		t.Errorf("FilesTransferred = %d, want %d", result.Stats.FilesTransferred, len(hostileNames))
	}
	for _, hostileName := range hostileNames {
		if _, err := os.Stat(filepath.Join(dst, hostileName)); err != nil {
			t.Errorf("hostile filename %q was not transferred: %v", hostileName, err)
		}
	}
	for _, update := range progress {
		if strings.Contains(update, "45%") {
			t.Errorf("hostile filename produced a false progress callback: %q", update)
		}
	}
	logged := assertPrimaryLogFrames(t, logPath, "RSYNC")
	if !strings.Contains(logged, "trusted") {
		t.Errorf("primary log lacks rsync per-item detail: %q", logged)
	}
	if got := strings.Count(logged, "report 45% (xfr#999).txt"); got != 1 {
		t.Errorf("progress-like itemized filename detail count = %d, want 1: %q", got, logged)
	}
	if strings.Contains(logged, "\x1b") || strings.Contains(logged, "\u009b") || strings.Contains(logged, "\x7f") {
		t.Errorf("primary log retains hostile controls: %q", logged)
	}
}

func TestRsyncStdout_ProgressUpdatesButOnlyDiagnosticsReachToolLog(t *testing.T) {
	logger, logPath := newTestLoggerWithPath(t)
	executor := NewRsyncExecutor(logger)
	statsTail := newTailBuffer(statisticsTailBytes)
	var progress []string

	executor.captureStdoutRecord(
		"  1,234  45%   2.30MB/s  0:01:23 (xfr#1, to-chk=1/2)",
		statsTail,
		func(text string) { progress = append(progress, text) },
	)
	executor.captureStdoutRecord("Number of files: 10", statsTail, nil)

	if len(progress) != 1 || !strings.Contains(progress[0], "45%") {
		t.Errorf("progress callbacks = %q, want one 45%% update", progress)
	}
	if got := string(statsTail.Bytes()); !strings.Contains(got, "45%") ||
		!strings.Contains(got, "Number of files: 10\n") {
		t.Errorf("statistics tail = %q, want both normalized records", got)
	}
	logged := assertPrimaryLogFrames(t, logPath, "RSYNC")
	if strings.Contains(logged, "45%") {
		t.Errorf("progress repaint leaked into primary tool log: %q", logged)
	}
	if !strings.Contains(logged, "Number of files: 10") {
		t.Errorf("diagnostic record missing from primary tool log: %q", logged)
	}
}

func TestParseRsyncProgress_TypicalLine(t *testing.T) {
	tests := []struct {
		name    string
		segment string
		want    string
	}{
		{
			"full progress",
			"  1,234,567  45%   2.30MB/s    0:01:23 (xfr#12, to-chk=88/100)",
			"45%, 2.30MB/s, 0:01:23 remaining",
		},
		{
			"100% complete",
			"  5,678,901 100%    5.00MB/s    0:00:00 (xfr#42, to-chk=0/100)",
			"100%, 5.00MB/s",
		},
		{
			"zero speed",
			"          0   0%    0.00kB/s    0:00:00 (xfr#0, ir-chk=1/2)",
			"0%, 0.00kB/s",
		},
		{
			"human-readable counter",
			"      4.19M 100%  709.82MB/s    0:00:00 (xfr#1, to-chk=0/2)",
			"100%, 709.82MB/s",
		},
		{
			"empty segment",
			"",
			"",
		},
		{
			"non-progress text",
			"receiving file list ... done",
			"",
		},
		{
			"out-format filename containing progress-like fields",
			">f+++++++++ report 45% speed/s 0:00:01 (xfr#999).txt",
			"",
		},
		{
			"arbitrary token with human-readable prefix",
			"4.19M-report 45% speed/s 0:00:01 (xfr#999).txt",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRsyncProgress(tt.segment)
			if got != tt.want {
				t.Errorf("parseRsyncProgress(%q) = %q, want %q", tt.segment, got, tt.want)
			}
		})
	}
}

func TestScanRsyncProgress_PopulatesCapture(t *testing.T) {
	input := "Number of files: 10\nsome output\n"
	r := strings.NewReader(input)
	var capture bytes.Buffer

	scanRsyncProgress(r, &capture, nil)

	if capture.String() != input {
		t.Errorf("capture buffer = %q, want %q", capture.String(), input)
	}
}

func TestScanRsyncProgress_CallsOnProgress(t *testing.T) {
	input := "  1,234  45%   2.30MB/s    0:01:23 (xfr#1, to-chk=1/2)\r\n"
	r := strings.NewReader(input)
	var capture bytes.Buffer

	var called []string
	onProgress := func(text string) {
		called = append(called, text)
	}

	scanRsyncProgress(r, &capture, onProgress)

	if len(called) == 0 {
		t.Fatal("onProgress was never called")
	}
	if !strings.Contains(called[len(called)-1], "45%") {
		t.Errorf("last progress = %q, want something containing 45%%", called[len(called)-1])
	}
}

func TestRsyncExec_ExpiredContext_ReturnsTimedOut(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	// A context whose deadline is already in the past will cause exec.CommandContext
	// to kill the process immediately, producing a DeadlineExceeded context error.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	defaults := &config.RsyncDefaults{Flags: []string{"-a"}}
	args := BuildRsyncArgs(RsyncArgsRequest{Defaults: defaults, Source: src + "/", Destination: dst + "/"})

	executor := NewRsyncExecutor(newTestLogger(t))
	result := executor.Exec(ctx, args, nil)

	if result.Status != StatusTimedOut {
		t.Errorf("Status = %q, want %q", result.Status, StatusTimedOut)
	}
}

func TestTailBuffer_UnderCapacity_KeepsEverything(t *testing.T) {
	tb := newTailBuffer(8)
	_, _ = tb.Write([]byte("abc"))
	_, _ = tb.Write([]byte("de"))
	if got := string(tb.Bytes()); got != "abcde" {
		t.Errorf("Bytes() = %q, want %q", got, "abcde")
	}
}

func TestTailBuffer_ExactlyAtCapacity_KeepsEverything(t *testing.T) {
	tb := newTailBuffer(4)
	_, _ = tb.Write([]byte("abcd"))
	if got := string(tb.Bytes()); got != "abcd" {
		t.Errorf("Bytes() = %q, want %q", got, "abcd")
	}
}

func TestTailBuffer_SingleWriteOverCapacity_KeepsTailOfWrite(t *testing.T) {
	tb := newTailBuffer(4)
	_, _ = tb.Write([]byte("abcdef"))
	if got := string(tb.Bytes()); got != "cdef" {
		t.Errorf("Bytes() = %q, want %q", got, "cdef")
	}
}

func TestTailBuffer_MultiWriteWrap_KeepsLastBytes(t *testing.T) {
	tb := newTailBuffer(5)
	_, _ = tb.Write([]byte("abc"))
	_, _ = tb.Write([]byte("def"))
	if got := string(tb.Bytes()); got != "bcdef" {
		t.Errorf("Bytes() = %q, want %q", got, "bcdef")
	}
	_, _ = tb.Write([]byte("XYZ"))
	if got := string(tb.Bytes()); got != "efXYZ" {
		t.Errorf("Bytes() = %q, want %q", got, "efXYZ")
	}
}

func TestTailBuffer_EmptyWrite_LeavesContentUnchanged(t *testing.T) {
	tb := newTailBuffer(4)
	if got := tb.Bytes(); len(got) != 0 {
		t.Errorf("Bytes() before any write = %q, want empty", got)
	}
	_, _ = tb.Write([]byte("ab"))
	_, _ = tb.Write(nil)
	if got := string(tb.Bytes()); got != "ab" {
		t.Errorf("Bytes() = %q, want %q", got, "ab")
	}
}

func TestScanRsyncProgress_OverCapacityStream_StatsStillParsed(t *testing.T) {
	// Simulates a -v run whose file listing exceeds the capture bound: only
	// the head may be dropped; the trailing stats block must survive.
	listing := strings.Repeat("verbose-file-listing-line.txt\n", 4000) // ~120 KiB
	stats := readFixture(t, "rsync_stats_transferred.txt")
	r := strings.NewReader(listing + string(stats))
	capture := newTailBuffer(statisticsTailBytes)

	scanRsyncProgress(r, capture, nil)

	parsed := ParseRsyncStats(capture.Bytes())
	if parsed.FilesTransferred != 8 {
		t.Errorf("FilesTransferred = %d, want 8", parsed.FilesTransferred)
	}
	if parsed.FilesChecked != 250 {
		t.Errorf("FilesChecked = %d, want 250", parsed.FilesChecked)
	}
	if parsed.BytesSent != "45.35M" {
		t.Errorf("BytesSent = %q, want %q", parsed.BytesSent, "45.35M")
	}
}
