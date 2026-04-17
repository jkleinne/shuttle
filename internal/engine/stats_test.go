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

func TestFormatTransfer(t *testing.T) {
	tests := []struct {
		name  string
		stats TransferStats
		want  string
	}{
		{
			"typical rsync",
			TransferStats{FilesTransferred: 42, BytesSent: "3.10K", Speed: "6.27K/s"},
			"42 transferred, 3.10K sent at 6.27K/s",
		},
		{
			"rclone with units",
			TransferStats{FilesTransferred: 5, BytesSent: "12.3 MiB", Speed: "2.1 MiB/s"},
			"5 transferred, 12.3 MiB sent at 2.1 MiB/s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTransfer(tt.stats)
			if got != tt.want {
				t.Errorf("formatTransfer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatChecked(t *testing.T) {
	tests := []struct {
		name  string
		stats TransferStats
		want  string
	}{
		{
			"zero elapsed",
			TransferStats{FilesChecked: 919, Elapsed: 0},
			"919 checked",
		},
		{
			"sub-second elapsed",
			TransferStats{FilesChecked: 4, Elapsed: 500 * time.Millisecond},
			"4 checked",
		},
		{
			"elapsed at threshold",
			TransferStats{FilesChecked: 3258, Elapsed: 1 * time.Second},
			"3,258 checked (1s)",
		},
		{
			"elapsed above threshold",
			TransferStats{FilesChecked: 55259, Elapsed: 36 * time.Second},
			"55,259 checked (36s)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatChecked(tt.stats)
			if got != tt.want {
				t.Errorf("formatChecked() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{5, "5"},
		{999, "999"},
		{1000, "1,000"},
		{14101, "14,101"},
		{55259, "55,259"},
		{1000000, "1,000,000"},
	}
	for _, tt := range tests {
		got := formatNumber(tt.n)
		if got != tt.want {
			t.Errorf("formatNumber(%d) = %q, want %q", tt.n, got, tt.want)
		}
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

func TestSummary_HasErrors_ReturnsTrue_WhenAnyNotFound(t *testing.T) {
	// StatusNotFound (source path missing) is a failure condition equivalent to
	// StatusFailed. HasErrors must include it so the CLI exits with code 1.
	s := Summary{
		Jobs: []JobResult{
			{Name: "job1", Items: []ItemResult{{Status: StatusOK}}},
			{Name: "job2", Items: []ItemResult{{Status: StatusNotFound}}},
		},
	}
	if !s.HasErrors() {
		t.Error("HasErrors() = false, want true for StatusNotFound")
	}
}

func TestSummary_HasErrors_ReturnsFalse_WhenOnlyOptionalMissing(t *testing.T) {
	// StatusOptionalMissing is an explicit user-opted-in outcome for
	// detachable sources. It must not contribute to HasErrors or the
	// run will exit non-zero on normal "device not plugged in" cases.
	s := Summary{
		Jobs: []JobResult{
			{Name: "job1", Items: []ItemResult{{Status: StatusOK}}},
			{Name: "koreader", Items: []ItemResult{{Status: StatusOptionalMissing}}},
		},
	}
	if s.HasErrors() {
		t.Error("HasErrors() = true, want false when only optional-missing items are non-OK")
	}
}

func TestSummary_HasErrors_ReturnsTrue_WhenOptionalMissingMixedWithFailed(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "koreader", Items: []ItemResult{{Status: StatusOptionalMissing}}},
			{Name: "docs", Items: []ItemResult{{Status: StatusFailed}}},
		},
	}
	if !s.HasErrors() {
		t.Error("HasErrors() = false, want true when any item is StatusFailed")
	}
}

func TestJobLabel(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{"photos", "", "photos"},
		{"docs-to-cloud", "my_gdrive", "docs-to-cloud:my_gdrive"},
		{"backup", "koofr", "backup:koofr"},
	}
	for _, tt := range tests {
		got := jobLabel(tt.name, tt.remote)
		if got != tt.want {
			t.Errorf("jobLabel(%q, %q) = %q, want %q", tt.name, tt.remote, got, tt.want)
		}
	}
}

func TestRenderSummary_RsyncSingleSource_NoTransfers(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{FilesChecked: 14101}},
			}},
		},
		Duration: 30 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "✓ photos") {
		t.Error("missing success symbol + job name")
	}
	if !strings.Contains(out, "14,101 checked") {
		t.Error("missing thousand-separated checked count")
	}
	if strings.Contains(out, "transferred") {
		t.Error("should not show transfer details when nothing transferred")
	}
	if !strings.Contains(out, "1 passed") {
		t.Error("missing footer tally")
	}
}

func TestRenderSummary_RsyncSingleSource_WithTransfers(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{
					FilesChecked: 14101, FilesTransferred: 42,
					BytesSent: "3.10K", Speed: "6.27K/s",
				}},
			}},
		},
		Duration: 5 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "14,101 checked") {
		t.Error("missing checked count")
	}
	if !strings.Contains(out, "42 transferred, 3.10K sent at 6.27K/s") {
		t.Error("missing transfer detail line")
	}
}

func TestRenderSummary_RsyncMultipleSources(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{
					FilesChecked: 14101, FilesTransferred: 42,
					BytesSent: "3.10K", Speed: "6.27K/s",
				}},
				{Name: "readme", Status: StatusOK, Stats: TransferStats{FilesChecked: 3}},
			}},
		},
		Duration: 5 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "gallery:") {
		t.Error("missing source name for multi-source job")
	}
	if !strings.Contains(out, "readme:") {
		t.Error("missing second source name")
	}
	if !strings.Contains(out, "42 transferred") {
		t.Error("missing transfer detail under first source")
	}
}

func TestRenderSummary_RcloneCollapsed(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "media-to-cloud", Remote: "my_gdrive", Items: []ItemResult{
				{Name: "media", Status: StatusOK, Stats: TransferStats{FilesChecked: 919}},
			}},
			{Name: "media-to-cloud", Remote: "my_s3", Items: []ItemResult{
				{Name: "media", Status: StatusOK, Stats: TransferStats{FilesChecked: 919}},
			}},
		},
		Duration: 10 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "2 remotes") {
		t.Error("collapsed group should show remote count")
	}
	if !strings.Contains(out, "919 checked") {
		t.Error("collapsed group should show shared checked count")
	}
	if strings.Contains(out, "my_gdrive") {
		t.Error("collapsed group should not show individual remote names")
	}
	if !strings.Contains(out, "1 passed") {
		t.Error("rclone group should count as one job in tally")
	}
}

func TestRenderSummary_RcloneCollapsed_ShowsMaxElapsed(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "docs", Remote: "gdrive", Items: []ItemResult{
				{Name: "d", Status: StatusOK, Stats: TransferStats{FilesChecked: 100, Elapsed: 0}},
			}},
			{Name: "docs", Remote: "koofr", Items: []ItemResult{
				{Name: "d", Status: StatusOK, Stats: TransferStats{FilesChecked: 100, Elapsed: 12 * time.Second}},
			}},
		},
		Duration: 15 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "100 checked (12s)") {
		t.Errorf("collapsed group should show max elapsed, got:\n%s", out)
	}
}

func TestRenderSummary_RcloneExpanded_DifferentChecked(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "docs-to-cloud", Remote: "my_gdrive", Items: []ItemResult{
				{Name: "Documents", Status: StatusOK, Stats: TransferStats{FilesChecked: 1350}},
			}},
			{Name: "docs-to-cloud", Remote: "my_s3", Items: []ItemResult{
				{Name: "Documents", Status: StatusOK, Stats: TransferStats{
					FilesChecked: 3258, Elapsed: 12 * time.Second,
				}},
			}},
		},
		Duration: 15 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "2 remotes") {
		t.Error("expanded group should show remote count")
	}
	if !strings.Contains(out, "├ my_gdrive") {
		t.Error("missing tree branch for first remote")
	}
	if !strings.Contains(out, "└ my_s3") {
		t.Error("missing tree branch for last remote")
	}
	if !strings.Contains(out, "1,350 checked") {
		t.Error("missing first remote stats")
	}
	if !strings.Contains(out, "3,258 checked (12s)") {
		t.Error("missing second remote stats with elapsed")
	}
}

func TestRenderSummary_RcloneExpanded_WithTransfers(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "docs-to-cloud", Remote: "my_gdrive", Items: []ItemResult{
				{Name: "Documents", Status: StatusOK, Stats: TransferStats{
					FilesChecked: 1350, FilesTransferred: 5,
					BytesSent: "12.3 MiB", Speed: "2.1 MiB/s",
				}},
			}},
			{Name: "docs-to-cloud", Remote: "my_s3", Items: []ItemResult{
				{Name: "Documents", Status: StatusOK, Stats: TransferStats{
					FilesChecked: 3258, Elapsed: 12 * time.Second,
				}},
			}},
		},
		Duration: 20 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "5 transferred, 12.3 MiB sent at 2.1 MiB/s") {
		t.Error("missing transfer detail under first remote")
	}
	if !strings.Contains(out, "│") {
		t.Error("transfer detail should have pipe continuation from tree")
	}
}

func TestRenderSummary_RcloneSingleRemote(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "backup", Remote: "gdrive", Items: []ItemResult{
				{Name: "data", Status: StatusOK, Stats: TransferStats{FilesChecked: 50}},
			}},
		},
		Duration: 2 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "gdrive") {
		t.Error("single-remote group should show remote name")
	}
	if !strings.Contains(out, "50 checked") {
		t.Error("missing checked count")
	}
}

func TestRenderSummary_RcloneGroupWithNotFound(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "docs-to-cloud", Remote: "my_gdrive", Items: []ItemResult{
				{Name: "Documents", Status: StatusNotFound},
			}},
			{Name: "docs-to-cloud", Remote: "my_s3", Items: []ItemResult{
				{Name: "Documents", Status: StatusOK, Stats: TransferStats{FilesChecked: 3258}},
			}},
		},
		Errors:   []string{"docs-to-cloud:my_gdrive/Documents"},
		Duration: 10 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "✗ docs-to-cloud") {
		t.Error("group with a failed remote should show ✗ symbol")
	}
	if !strings.Contains(out, "not found") {
		t.Error("not-found remote should show 'not found'")
	}
	if !strings.Contains(out, "1 failed") {
		t.Error("group should count as failed in tally")
	}
}

func TestRenderSummary_FailedAndNotFound(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{FilesChecked: 100}},
			}},
			{Name: "backups", Items: []ItemResult{
				{Name: "backups", Status: StatusNotFound},
			}},
		},
		Errors:   []string{"backups/backups"},
		Duration: 5 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "✗ backups") {
		t.Error("failed job should show ✗ symbol")
	}
	if !strings.Contains(out, "not found") {
		t.Error("not-found item should show 'not found'")
	}
	if !strings.Contains(out, "1 passed") {
		t.Error("incorrect passed count")
	}
	if !strings.Contains(out, "1 failed") {
		t.Error("incorrect failed count")
	}
	if !strings.Contains(out, "Errors:") {
		t.Error("missing errors section")
	}
}

func TestRenderSummary_SkippedJob(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{FilesChecked: 100}},
			}},
			{Name: "projects", Items: []ItemResult{
				{Name: "projects", Status: StatusSkipped},
			}},
		},
		Duration: 5 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "– projects") {
		t.Error("skipped job should show – symbol")
	}
	if !strings.Contains(out, "skipped") {
		t.Error("skipped job should show 'skipped'")
	}
	if !strings.Contains(out, "1 passed") {
		t.Error("skipped jobs should not count in tally")
	}
	if strings.Contains(out, "failed") {
		t.Error("tally should omit failed count when zero")
	}
}

func TestRenderSummary_DryRun(t *testing.T) {
	s := Summary{DryRun: true, Duration: 1 * time.Second}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "[DRY RUN]") {
		t.Error("missing dry run notice")
	}
	if !strings.Contains(out, "Sync Summary") {
		t.Error("missing header")
	}
}

func TestRenderSummary_FooterOmitsFailedWhenZero(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{FilesChecked: 10}},
			}},
		},
		Duration: 5 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if strings.Contains(out, "failed") {
		t.Error("footer should omit 'failed' when zero")
	}
	if !strings.Contains(out, "1 passed") {
		t.Error("missing passed count")
	}
	if !strings.Contains(out, "Duration: 5s") {
		t.Error("missing duration")
	}
}

func TestRenderSummary_WithColor(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{FilesChecked: 10}},
			}},
		},
		Duration: 1 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, true)
	out := buf.String()

	if !strings.Contains(out, "\033[") {
		t.Error("color output should contain ANSI escape codes")
	}
	if !strings.Contains(out, ansiGreen) {
		t.Error("success symbol should use green")
	}
	if !strings.Contains(out, ansiBold+ansiBlue) {
		t.Error("header should use bold blue")
	}
}

func TestStatusSymbol_OptionalMissing_PlainText(t *testing.T) {
	got := statusSymbol(StatusOptionalMissing, false)
	if got != "○" {
		t.Errorf("statusSymbol(StatusOptionalMissing, false) = %q, want \"○\"", got)
	}
}

func TestItemStatsText_OptionalMissing_PlainText(t *testing.T) {
	item := ItemResult{Status: StatusOptionalMissing}
	got := itemStatsText(item, false)
	if got != "source missing (optional)" {
		t.Errorf("itemStatsText = %q, want \"source missing (optional)\"", got)
	}
}

func TestJobStatus_AllOptionalMissing(t *testing.T) {
	job := JobResult{
		Name: "koreader",
		Items: []ItemResult{
			{Status: StatusOptionalMissing},
		},
	}
	if got := jobStatus(job); got != StatusOptionalMissing {
		t.Errorf("jobStatus = %q, want %q", got, StatusOptionalMissing)
	}
}

func TestJobStatus_MixedOKAndOptionalMissing_IsOK(t *testing.T) {
	// Multi-source rsync: one source present and synced, one absent.
	// The job is not a failure; the tally counts it as passed.
	job := JobResult{
		Name: "photos",
		Items: []ItemResult{
			{Status: StatusOK},
			{Status: StatusOptionalMissing},
		},
	}
	if got := jobStatus(job); got != StatusOK {
		t.Errorf("jobStatus = %q, want %q", got, StatusOK)
	}
}

func TestJobStatus_FailedWinsOverOptionalMissing(t *testing.T) {
	job := JobResult{
		Name: "photos",
		Items: []ItemResult{
			{Status: StatusFailed},
			{Status: StatusOptionalMissing},
		},
	}
	if got := jobStatus(job); got != StatusFailed {
		t.Errorf("jobStatus = %q, want %q", got, StatusFailed)
	}
}

func TestCanCollapseGroup_AllOptionalMissing(t *testing.T) {
	group := []JobResult{
		{Name: "koreader", Remote: "crypt_gdrive", Items: []ItemResult{{Status: StatusOptionalMissing}}},
		{Name: "koreader", Remote: "crypt_koofr", Items: []ItemResult{{Status: StatusOptionalMissing}}},
	}
	if !canCollapseGroup(group) {
		t.Error("canCollapseGroup = false, want true for all-optional-missing group")
	}
}

func TestFormatTally_IncludesOptionalSegment(t *testing.T) {
	got := formatTally(5, 1, 2, 30*time.Second, false)
	if !strings.Contains(got, "5 passed") {
		t.Errorf("missing '5 passed' in %q", got)
	}
	if !strings.Contains(got, "1 optional") {
		t.Errorf("missing '1 optional' in %q", got)
	}
	if !strings.Contains(got, "2 failed") {
		t.Errorf("missing '2 failed' in %q", got)
	}
}

func TestFormatTally_OmitsOptionalWhenZero(t *testing.T) {
	got := formatTally(5, 0, 0, 10*time.Second, false)
	if strings.Contains(got, "optional") {
		t.Errorf("unexpected 'optional' segment in %q", got)
	}
}

func TestRenderSummary_OptionalMissingRclone_TallyAndSymbol(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{Name: "photos", Items: []ItemResult{
				{Name: "gallery", Status: StatusOK, Stats: TransferStats{FilesChecked: 100}},
			}},
			{Name: "koreader", Remote: "crypt_gdrive", Items: []ItemResult{
				{Name: "books", Status: StatusOptionalMissing},
			}},
			{Name: "koreader", Remote: "crypt_koofr", Items: []ItemResult{
				{Name: "books", Status: StatusOptionalMissing},
			}},
		},
		Duration: 5 * time.Second,
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	out := buf.String()

	if !strings.Contains(out, "○") {
		t.Error("missing optional-missing symbol ○")
	}
	if !strings.Contains(out, "source missing (optional)") {
		t.Error("missing optional-missing text")
	}
	if !strings.Contains(out, "1 passed") {
		t.Errorf("tally should read '1 passed' (photos), got: %s", out)
	}
	if !strings.Contains(out, "1 optional") {
		t.Errorf("tally should read '1 optional' (koreader group), got: %s", out)
	}
	if strings.Contains(out, "failed") {
		t.Errorf("tally should not include 'failed' segment when no failures, got: %s", out)
	}
}
