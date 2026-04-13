package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

func TestBuildRsyncArgs_DefaultsAndExtraFlags(t *testing.T) {
	defaults := &config.RsyncDefaults{
		Flags: []string{"-a", "-v", "-h", "-P"},
	}
	job := config.Job{
		Delete:     true,
		ExtraFlags: []string{"--exclude=.*"},
	}
	args := BuildRsyncArgs(defaults, job, "/src/", "/dst/", true, false, "")
	joined := strings.Join(args, " ")

	// Instrumentation must be present.
	if !strings.Contains(joined, "--stats") {
		t.Error("missing instrumentation flag --stats")
	}
	if !strings.Contains(joined, "--info=progress2") {
		t.Error("missing instrumentation flag --info=progress2")
	}
	// Default flags must be present.
	if !strings.Contains(joined, "-a") {
		t.Error("missing default flag -a")
	}
	// Extra flags must be present.
	if !strings.Contains(joined, "--exclude=.*") {
		t.Error("missing extra flag --exclude=.*")
	}
	// Delete flag for directory source.
	if !strings.Contains(joined, "--delete-after") {
		t.Error("missing --delete-after for directory source with delete=true")
	}
	// Source and dest must be last.
	if args[len(args)-2] != "/src/" || args[len(args)-1] != "/dst/" {
		t.Errorf("source/dest should be last, got %v", args[len(args)-2:])
	}
}

func TestBuildRsyncArgs_NoDefaults(t *testing.T) {
	args := BuildRsyncArgs(nil, config.Job{}, "/src", "/dst", false, false, "")
	// Instrumentation flags must appear even when no defaults are provided.
	found := false
	for _, a := range args {
		if a == "--stats" {
			found = true
		}
	}
	if !found {
		t.Error("instrumentation --stats should be present even with nil defaults")
	}
}

func TestBuildRsyncArgs_DryRun(t *testing.T) {
	args := BuildRsyncArgs(nil, config.Job{}, "/src", "/dst", false, true, "")
	found := false
	for _, a := range args {
		if a == "--dry-run" {
			found = true
		}
	}
	if !found {
		t.Error("--dry-run should be present when dryRun is true")
	}
}

func TestBuildRsyncArgs_DeleteNotAppliedToFile(t *testing.T) {
	// isDeleteDir=false: --delete-after must not appear even when job.Delete is true.
	job := config.Job{Delete: true}
	args := BuildRsyncArgs(nil, job, "/src/file.txt", "/dst/", false, false, "")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--delete-after") {
		t.Error("--delete-after must not appear when isDeleteDir is false")
	}
}

func TestBuildRsyncArgs_LogFile(t *testing.T) {
	args := BuildRsyncArgs(nil, config.Job{}, "/src", "/dst", false, false, "/tmp/shuttle.log")
	found := false
	for _, a := range args {
		if strings.HasPrefix(a, "--log-file=") {
			found = true
		}
	}
	if !found {
		t.Error("--log-file= should be present when logFile is non-empty")
	}
}

func TestBuildRcloneArgs_DefaultsAndOverrides(t *testing.T) {
	defaults := &config.RcloneDefaults{
		Flags:      []string{"--copy-links", "--fast-list"},
		FilterFile: "/tmp/filters.txt",
		Transfers:  6,
		Bwlimit:    "5.5M",
	}
	job := config.Job{
		Bwlimit:    "2M", // per-job override
		ExtraFlags: []string{"--track-renames"},
	}
	args := BuildRcloneArgs("copy", defaults, job, "/src/", "remote:dst/", true, false, "/tmp/log", "")
	joined := strings.Join(args, " ")

	// Subcommand is first.
	if args[0] != "copy" {
		t.Errorf("first arg should be subcommand 'copy', got %q", args[0])
	}
	// Default flags must be present.
	if !strings.Contains(joined, "--copy-links") {
		t.Error("missing default flag --copy-links")
	}
	// Default tuning must be translated.
	if !strings.Contains(joined, "--transfers 6") {
		t.Error("missing default tuning --transfers 6")
	}
	// Per-job override: last --bwlimit should be "2M", not "5.5M".
	lastBw := ""
	for i, a := range args {
		if a == "--bwlimit" && i+1 < len(args) {
			lastBw = args[i+1]
		}
	}
	if lastBw != "2M" {
		t.Errorf("last --bwlimit should be 2M (per-job override), got %q", lastBw)
	}
	// Filter file from defaults.
	if !strings.Contains(joined, "--filter-from /tmp/filters.txt") {
		t.Error("missing --filter-from from defaults")
	}
	// Extra flags.
	if !strings.Contains(joined, "--track-renames") {
		t.Error("missing extra flag --track-renames")
	}
}

func TestBuildRcloneArgs_JobFilterFileOverridesDefault(t *testing.T) {
	defaults := &config.RcloneDefaults{
		FilterFile: "/default/filters.txt",
	}
	job := config.Job{
		FilterFile: "/job/filters.txt",
	}
	args := BuildRcloneArgs("copy", defaults, job, "/src", "remote:dst", true, false, "/tmp/log", "")
	// The job-level filter file should be used; the default must not appear.
	found := false
	for i, a := range args {
		if a == "--filter-from" && i+1 < len(args) && args[i+1] == "/job/filters.txt" {
			found = true
		}
	}
	if !found {
		t.Error("job-level filter_file should override defaults")
	}
	// Default filter file must not appear.
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "/default/filters.txt") {
		t.Error("default filter_file should not appear when job overrides it")
	}
}

func TestBuildRcloneArgs_Instrumentation(t *testing.T) {
	args := BuildRcloneArgs("copy", nil, config.Job{}, "/src", "remote:dst", true, false, "/tmp/log", "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--stats 1s") {
		t.Error("missing instrumentation --stats 1s")
	}
	if !strings.Contains(joined, "-P") {
		t.Error("missing instrumentation -P")
	}
	if !strings.Contains(joined, "--log-file /tmp/log") {
		t.Error("missing instrumentation --log-file")
	}
	if !strings.Contains(joined, "--log-level INFO") {
		t.Error("missing instrumentation --log-level")
	}
}

func TestBuildRcloneArgs_BackupDir(t *testing.T) {
	args := BuildRcloneArgs("sync", nil, config.Job{}, "/src", "remote:dst", true, false, "", "remote:_archive/2026-01-01/dst/")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--backup-dir remote:_archive/2026-01-01/dst/") {
		t.Errorf("missing --backup-dir in args: %q", joined)
	}
}

func TestBuildRcloneArgs_DryRun(t *testing.T) {
	args := BuildRcloneArgs("copy", nil, config.Job{}, "/src", "remote:dst", true, true, "", "")
	found := false
	for _, a := range args {
		if a == "--dry-run" {
			found = true
		}
	}
	if !found {
		t.Error("--dry-run should be present when dryRun is true")
	}
}

func TestBuildTuningFlags_ZeroValuesOmitted(t *testing.T) {
	defaults := &config.RcloneDefaults{
		Transfers: 0,
		Bwlimit:   "",
		UseMmap:   false,
	}
	flags := buildTuningFlags(defaults)
	if len(flags) != 0 {
		t.Errorf("expected no flags for zero values, got %v", flags)
	}
}

func TestBuildTuningFlags_AllFields(t *testing.T) {
	defaults := &config.RcloneDefaults{
		Transfers:       4,
		Checkers:        8,
		Bwlimit:         "10M",
		DriveChunkSize:  "32M",
		BufferSize:      "16M",
		UseMmap:         true,
		Timeout:         "5m",
		Contimeout:      "30s",
		LowLevelRetries: 10,
		OrderBy:         "size,desc",
	}
	flags := buildTuningFlags(defaults)
	joined := strings.Join(flags, " ")
	expects := []string{
		"--transfers 4", "--checkers 8", "--bwlimit 10M",
		"--drive-chunk-size 32M", "--buffer-size 16M", "--use-mmap",
		"--timeout 5m", "--contimeout 30s", "--low-level-retries 10",
		"--order-by size,desc",
	}
	for _, e := range expects {
		if !strings.Contains(joined, e) {
			t.Errorf("missing expected flag sequence %q in %q", e, joined)
		}
	}
}

func TestWarnFlagConflicts_DetectsRsyncStats(t *testing.T) {
	var buf strings.Builder
	logPath := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(&buf, logPath, false)
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	defer logger.Close()

	WarnFlagConflicts(logger, "rsync", []string{"-a", "--stats", "-v"})
	if !strings.Contains(buf.String(), "conflicts") {
		t.Errorf("expected conflict warning for --stats, got %q", buf.String())
	}
}

func TestWarnFlagConflicts_DetectsRsyncInfoProgress(t *testing.T) {
	var buf strings.Builder
	logPath := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(&buf, logPath, false)
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	defer logger.Close()

	WarnFlagConflicts(logger, "rsync", []string{"--info=progress2"})
	if !strings.Contains(buf.String(), "conflicts") {
		t.Errorf("expected conflict warning for --info=progress2, got %q", buf.String())
	}
}

func TestWarnFlagConflicts_DetectsRcloneLogFile(t *testing.T) {
	var buf strings.Builder
	logPath := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(&buf, logPath, false)
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	defer logger.Close()

	WarnFlagConflicts(logger, "rclone", []string{"--log-file=/custom/path.log"})
	if !strings.Contains(buf.String(), "conflicts") {
		t.Errorf("expected conflict warning for --log-file=..., got %q", buf.String())
	}
}

func TestWarnFlagConflicts_NoConflict(t *testing.T) {
	var buf strings.Builder
	logPath := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(&buf, logPath, false)
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	defer logger.Close()

	WarnFlagConflicts(logger, "rsync", []string{"-a", "-v", "-h"})
	if strings.Contains(buf.String(), "conflicts") {
		t.Errorf("unexpected conflict warning for safe flags: %q", buf.String())
	}
}

func TestBuildJobTuningFlags_OnlyOverrides(t *testing.T) {
	job := config.Job{
		Bwlimit:   "2M",
		Transfers: 3,
	}
	flags := buildJobTuningFlags(job)
	joined := strings.Join(flags, " ")
	if !strings.Contains(joined, "--bwlimit 2M") {
		t.Error("missing --bwlimit from job override")
	}
	if !strings.Contains(joined, "--transfers 3") {
		t.Error("missing --transfers from job override")
	}
	// Fields not set on job must not appear.
	if strings.Contains(joined, "--checkers") {
		t.Error("--checkers should not appear when not overridden")
	}
}

func TestBuildJobTuningFlags_ZeroValuesOmitted(t *testing.T) {
	job := config.Job{} // all zero values
	flags := buildJobTuningFlags(job)
	if len(flags) != 0 {
		t.Errorf("expected no flags for zero-value job, got %v", flags)
	}
}
