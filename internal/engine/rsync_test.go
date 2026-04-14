package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

func newTestLogger(t *testing.T) *log.Logger {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(os.Stdout, logPath, false)
	if err != nil {
		t.Fatalf("creating test logger: %v", err)
	}
	t.Cleanup(func() { logger.Close() })
	return logger
}

func TestRsyncExec_TransfersFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	defaults := &config.RsyncDefaults{Flags: []string{"-a", "-v", "-h", "-P"}}
	job := config.Job{}
	args := BuildRsyncArgs(defaults, job, src+"/", dst+"/", false, false, "")

	executor := NewRsyncExecutor(newTestLogger(t))
	result := executor.Exec(context.Background(), args, nil)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Stats.FilesTransferred != 1 {
		t.Errorf("FilesTransferred = %d, want 1", result.Stats.FilesTransferred)
	}
	content, _ := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if string(content) != "world" {
		t.Errorf("file content = %q, want world", string(content))
	}
}

func TestRsyncExec_DryRun_DoesNotTransfer(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	defaults := &config.RsyncDefaults{Flags: []string{"-a", "-v", "-h", "-P"}}
	args := BuildRsyncArgs(defaults, config.Job{}, src+"/", dst+"/", false, true, "")

	executor := NewRsyncExecutor(newTestLogger(t))
	result := executor.Exec(context.Background(), args, nil)

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	entries, _ := os.ReadDir(dst)
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
	args := BuildRsyncArgs(defaults, job, src+"/", dst+"/", true, false, "")

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
	args := BuildRsyncArgs(defaults, job, src+"/", dst+"/", false, false, "")

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
			"empty segment",
			"",
			"",
		},
		{
			"non-progress text",
			"receiving file list ... done",
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
