package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

func TestParseRsyncStats_UpToDate(t *testing.T) {
	data := readFixture(t, "rsync_stats_uptodate.txt")
	stats := ParseRsyncStats(data)

	if stats.FilesChecked != 14101 {
		t.Errorf("FilesChecked = %d, want 14101", stats.FilesChecked)
	}
	if stats.FilesTransferred != 0 {
		t.Errorf("FilesTransferred = %d, want 0", stats.FilesTransferred)
	}
	if stats.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d, want 0", stats.FilesDeleted)
	}
	if stats.BytesSent != "666.76K" {
		t.Errorf("BytesSent = %q, want 666.76K", stats.BytesSent)
	}
	if stats.Speed != "444.69K/s" {
		t.Errorf("Speed = %q, want 444.69K/s", stats.Speed)
	}
}

func TestParseRsyncStats_WithTransfers(t *testing.T) {
	data := readFixture(t, "rsync_stats_transferred.txt")
	stats := ParseRsyncStats(data)

	if stats.FilesChecked != 250 {
		t.Errorf("FilesChecked = %d, want 250", stats.FilesChecked)
	}
	if stats.FilesTransferred != 8 {
		t.Errorf("FilesTransferred = %d, want 8", stats.FilesTransferred)
	}
	if stats.FilesDeleted != 2 {
		t.Errorf("FilesDeleted = %d, want 2", stats.FilesDeleted)
	}
	if stats.BytesSent != "45.35M" {
		t.Errorf("BytesSent = %q, want 45.35M", stats.BytesSent)
	}
	if stats.Speed != "3.02M/s" {
		t.Errorf("Speed = %q, want 3.02M/s", stats.Speed)
	}
}

func TestParseRcloneStats_UpToDate(t *testing.T) {
	data := readFixture(t, "rclone_log_uptodate.txt")
	stats := ParseRcloneStats(data)

	if stats.FilesChecked != 3258 {
		t.Errorf("FilesChecked = %d, want 3258", stats.FilesChecked)
	}
	if stats.FilesTransferred != 0 {
		t.Errorf("FilesTransferred = %d, want 0", stats.FilesTransferred)
	}
	if stats.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d, want 0", stats.FilesDeleted)
	}
}

func TestParseRcloneStats_WithTransfers(t *testing.T) {
	data := readFixture(t, "rclone_log_transferred.txt")
	stats := ParseRcloneStats(data)

	// FilesChecked = Checks count only (not summed with transferred).
	// This matches rclone's semantics: "Checks" are server-side comparisons,
	// "Transferred" are actual data movements. They're reported separately.
	if stats.FilesChecked != 919 {
		t.Errorf("FilesChecked = %d, want 919", stats.FilesChecked)
	}
	if stats.FilesTransferred != 12 {
		t.Errorf("FilesTransferred = %d, want 12", stats.FilesTransferred)
	}
	if stats.FilesDeleted != 3 {
		t.Errorf("FilesDeleted = %d, want 3", stats.FilesDeleted)
	}
	if stats.BytesSent != "1.082 GiB" {
		t.Errorf("BytesSent = %q, want 1.082 GiB", stats.BytesSent)
	}
	if stats.Speed != "32.709 KiB/s" {
		t.Errorf("Speed = %q, want 32.709 KiB/s", stats.Speed)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{38 * time.Second, "38s"},
		{125 * time.Second, "2m 05s"},
		{3725 * time.Second, "1h 02m 05s"},
		{0, "0s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
	}
	for _, tt := range tests {
		got := FormatDuration(tt.d)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestSummary_HasErrors_ReturnsFalse_WhenAllOK(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "job1", Items: []ItemResult{{Status: StatusOK}}},
		},
	}
	if s.HasErrors() {
		t.Error("HasErrors() = true, want false")
	}
}

func TestSummary_HasErrors_ReturnsTrue_WhenAnyFailed(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "job1", Items: []ItemResult{{Status: StatusOK}}},
			{Name: "job2", Items: []ItemResult{{Status: StatusFailed}}},
		},
	}
	if !s.HasErrors() {
		t.Error("HasErrors() = false, want true")
	}
}

func TestFormatItemStats_UpToDate(t *testing.T) {
	r := ItemResult{Status: StatusOK, Stats: TransferStats{
		FilesChecked: 100, Elapsed: 5 * time.Second,
	}}
	got := FormatItemStats(r)
	// StatusOK with no transfers renders as "N checked (Ts)".
	if !strings.Contains(got, "checked") {
		t.Errorf("expected 'checked', got %q", got)
	}
	if strings.Contains(got, "transferred") {
		t.Errorf("expected no 'transferred' in up-to-date output, got %q", got)
	}
}

func TestFormatItemStats_WithTransfers(t *testing.T) {
	r := ItemResult{Status: StatusOK, Stats: TransferStats{
		FilesChecked: 50, FilesTransferred: 3, BytesSent: "12.3 MiB",
		Speed: "2.1 MiB/s", Elapsed: 15 * time.Second,
	}}
	got := FormatItemStats(r)
	if !strings.Contains(got, "3 transferred") {
		t.Errorf("expected '3 transferred', got %q", got)
	}
	if !strings.Contains(got, "12.3 MiB") {
		t.Errorf("expected bytes, got %q", got)
	}
}

func TestFormatItemStats_Failed(t *testing.T) {
	r := ItemResult{Status: StatusFailed, Stats: TransferStats{
		FilesTransferred: 1, Elapsed: 3 * time.Second,
	}}
	got := FormatItemStats(r)
	// FormatItemStats returns "[failed]" for StatusFailed.
	if !strings.Contains(got, "failed") {
		t.Errorf("expected 'failed', got %q", got)
	}
}

func TestFormatItemStats_Skipped(t *testing.T) {
	r := ItemResult{Status: StatusSkipped}
	got := FormatItemStats(r)
	if !strings.Contains(got, "skipped") {
		t.Errorf("expected 'skipped', got %q", got)
	}
}

func TestRenderSummary_DryRunNotice(t *testing.T) {
	s := Summary{DryRun: true, Duration: 1 * time.Second}
	var buf strings.Builder
	RenderSummary(&buf, s)
	// RenderSummary prepends "[DRY RUN]" when DryRun is true.
	if !strings.Contains(buf.String(), "DRY RUN") {
		t.Errorf("expected dry run notice, got %q", buf.String())
	}
}

func TestRenderSummary_ErrorSection(t *testing.T) {
	s := Summary{
		Errors:   []string{"photos/source1", "docs-to-cloud:gdrive/Documents"},
		Duration: 5 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s)
	out := buf.String()
	if !strings.Contains(out, "Errors") {
		t.Errorf("expected Errors section, got %q", out)
	}
	if !strings.Contains(out, "photos/source1") {
		t.Errorf("expected error detail, got %q", out)
	}
}

func TestRenderSummary_GroupsByJobName(t *testing.T) {
	summary := Summary{
		Jobs: []JobResult{
			{
				Name: "photos",
				Items: []ItemResult{
					{Name: "Photos Sync", Status: StatusOK, Stats: TransferStats{
						FilesChecked: 100, Elapsed: 5 * time.Second,
					}},
				},
			},
			{
				Name: "documents-to-cloud:my_gdrive",
				Items: []ItemResult{
					{Name: "Documents", Status: StatusOK, Stats: TransferStats{
						FilesChecked: 50, FilesTransferred: 3,
						BytesSent: "12.3 MiB", Speed: "2.1 MiB/s",
						Elapsed: 15 * time.Second,
					}},
				},
			},
		},
		Duration: 30 * time.Second,
	}

	var buf strings.Builder
	RenderSummary(&buf, summary)
	out := buf.String()

	if !strings.Contains(out, "photos:") {
		t.Error("missing photos: header")
	}
	if !strings.Contains(out, "documents-to-cloud:my_gdrive:") {
		t.Error("missing documents-to-cloud:my_gdrive: header")
	}
	if !strings.Contains(out, "Duration: 30s") {
		t.Error("missing duration")
	}
}
