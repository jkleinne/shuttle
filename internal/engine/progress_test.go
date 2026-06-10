package engine

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestProgressWriter_NonInteractive_FinishJob_OK(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, false, false)

	pw.StartJob(context.Background(), "photos")
	pw.FinishJob(ItemResult{
		Status: StatusOK,
		Stats:  TransferStats{FilesChecked: 14101, Elapsed: 38 * time.Second},
	})

	out := buf.String()
	if !strings.Contains(out, "\u2713") {
		t.Error("missing success symbol")
	}
	if !strings.Contains(out, "photos") {
		t.Error("missing job label")
	}
	if !strings.Contains(out, "14,101 checked (38s)") {
		t.Error("missing stats")
	}
}

func TestProgressWriter_NonInteractive_FinishJob_Failed(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, false, false)

	pw.StartJob(context.Background(), "photos")
	pw.FinishJob(ItemResult{Status: StatusFailed})

	out := buf.String()
	if !strings.Contains(out, "\u2717") {
		t.Error("missing failure symbol")
	}
	if !strings.Contains(out, "failed") {
		t.Error("missing 'failed' text")
	}
}

func TestProgressWriter_NonInteractive_FinishJob_NotFound(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, false, false)

	pw.StartJob(context.Background(), "photos")
	pw.FinishJob(ItemResult{Status: StatusNotFound})

	out := buf.String()
	if !strings.Contains(out, "not found") {
		t.Error("missing 'not found' text")
	}
}

func TestProgressWriter_NonInteractive_FinishJob_WithTransfers(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, false, false)

	pw.StartJob(context.Background(), "photos")
	pw.FinishJob(ItemResult{
		Status: StatusOK,
		Stats: TransferStats{
			FilesChecked: 14101, FilesTransferred: 12,
			BytesSent: "234.5 MiB", Speed: "5.6 MiB/s",
			Elapsed: 38 * time.Second,
		},
	})

	out := buf.String()
	if !strings.Contains(out, "14,101 checked") {
		t.Error("missing checked count")
	}
	if !strings.Contains(out, "12 transferred, 234.5 MiB sent at 5.6 MiB/s") {
		t.Error("missing transfer detail line")
	}
}

func TestProgressWriter_SkipJob(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, false, false)

	pw.SkipJob("projects")

	out := buf.String()
	if !strings.Contains(out, "\u2013") {
		t.Error("missing skip symbol")
	}
	if !strings.Contains(out, "projects") {
		t.Error("missing job name")
	}
	if !strings.Contains(out, "skipped") {
		t.Error("missing 'skipped' text")
	}
}

func TestProgressWriter_NonInteractive_StartJob_NoOutput(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, false, false)

	pw.StartJob(context.Background(), "photos")

	if buf.Len() != 0 {
		t.Errorf("non-interactive StartJob should produce no output, got: %q", buf.String())
	}
}

func TestProgressWriter_NonInteractive_UpdateProgress_NoOutput(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, false, false)

	pw.StartJob(context.Background(), "photos")
	pw.UpdateProgress("45%, 2.30 MB/s")

	if buf.Len() != 0 {
		t.Errorf("non-interactive UpdateProgress should produce no output, got: %q", buf.String())
	}
}

func TestProgressWriter_Interactive_Returns(t *testing.T) {
	pw := NewProgressWriter(&bytes.Buffer{}, true, false)
	if !pw.Interactive() {
		t.Error("Interactive() should be true")
	}
	pw2 := NewProgressWriter(&bytes.Buffer{}, false, false)
	if pw2.Interactive() {
		t.Error("Interactive() should be false")
	}
}

func TestProgressWriter_Interactive_FinishJob_ClearsLine(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, true, false)

	pw.StartJob(context.Background(), "photos")
	time.Sleep(100 * time.Millisecond)
	pw.FinishJob(ItemResult{
		Status: StatusOK,
		Stats:  TransferStats{FilesChecked: 919, Elapsed: 5 * time.Second},
	})

	out := buf.String()
	if !strings.Contains(out, "\033[2K") {
		t.Error("missing ANSI clear-line sequence")
	}
	if !strings.Contains(out, "919 checked") {
		t.Error("missing stats in final line")
	}
}

func TestProgressWriter_Interactive_MultipleJobs(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProgressWriter(&buf, true, false)

	pw.StartJob(context.Background(), "job1")
	pw.FinishJob(ItemResult{Status: StatusOK, Stats: TransferStats{FilesChecked: 100}})

	pw.StartJob(context.Background(), "job2")
	pw.FinishJob(ItemResult{Status: StatusFailed})

	out := buf.String()
	if !strings.Contains(out, "job1") {
		t.Error("missing first job")
	}
	if !strings.Contains(out, "job2") {
		t.Error("missing second job")
	}
	if !strings.Contains(out, "failed") {
		t.Error("missing failure status")
	}
}

func TestSanitizeProgress(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain stats unchanged", "604 KiB / 1 MiB, 59%, 206 KiB/s, ETA 2s", "604 KiB / 1 MiB, 59%, 206 KiB/s, ETA 2s"},
		{"bare ESC stripped", "evil\x1bxstats", "evilxstats"},
		{"CSI clear-screen stripped", "a\x1b[2Jb", "a[2Jb"},
		{"OSC title stripped", "x\x1b]0;pwned\x07y", "x]0;pwnedy"},
		{"DEL stripped", "a\x7fb", "ab"},
		{"newline and tab stripped", "a\nb\tc", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeProgress(tt.in); got != tt.want {
				t.Errorf("sanitizeProgress(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUpdateProgress_StoresSanitizedText(t *testing.T) {
	pw := NewProgressWriter(&bytes.Buffer{}, true, false) // interactive so UpdateProgress stores
	pw.UpdateProgress("danger\x1b]0;x\x07 100%")
	pw.mu.Lock()
	got := pw.currentProgress
	pw.mu.Unlock()
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("currentProgress retains control bytes: %q", got)
	}
}
