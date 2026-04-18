package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jkleinne/shuttle/internal/config"
)

func TestSelectMode(t *testing.T) {
	logger := newTestLogger(t)

	tests := []struct {
		name             string
		mode             string
		destination      string
		remoteName       string
		backupPath       string
		runTimestamp     string
		isDir            bool
		wantSubcommand   string
		wantBackupDirArg string
	}{
		{
			name:             "copy mode, directory source",
			mode:             "copy",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            true,
			wantSubcommand:   "copy",
			wantBackupDirArg: "",
		},
		{
			name:             "copy mode, file source",
			mode:             "copy",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            false,
			wantSubcommand:   "copy",
			wantBackupDirArg: "",
		},
		{
			name:             "sync mode, file source falls back to copy",
			mode:             "sync",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            false,
			wantSubcommand:   "copy",
			wantBackupDirArg: "",
		},
		{
			name:             "sync mode, directory source",
			mode:             "sync",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            true,
			wantSubcommand:   "sync",
			wantBackupDirArg: "",
		},
		{
			name:             "sync mode, directory source, backup path",
			mode:             "sync",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "backups",
			runTimestamp:     "2025-01-15_120000",
			isDir:            true,
			wantSubcommand:   "sync",
			wantBackupDirArg: "myremote:backups/2025-01-15_120000/photos/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subcommand, backupDirArg := selectMode(
				tt.mode, tt.destination, tt.remoteName,
				tt.backupPath, tt.runTimestamp, tt.isDir, logger,
			)
			if subcommand != tt.wantSubcommand {
				t.Errorf("subcommand = %q, want %q", subcommand, tt.wantSubcommand)
			}
			if backupDirArg != tt.wantBackupDirArg {
				t.Errorf("backupDirArg = %q, want %q", backupDirArg, tt.wantBackupDirArg)
			}
		})
	}
}

func TestScanRcloneProgress_ActiveTransfer_ShowsBytesLine(t *testing.T) {
	input := "Transferred:   1.082 GiB / 2.164 GiB, 50%, 32.709 KiB/s, ETA 30s\n"
	r := strings.NewReader(input)

	var called []string
	if err := scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	}); err != nil {
		t.Fatalf("scanRcloneProgress: %v", err)
	}

	if len(called) == 0 {
		t.Fatal("onProgress was never called")
	}
	if !strings.Contains(called[0], "GiB") {
		t.Errorf("progress = %q, want bytes-transferred line", called[0])
	}
}

func TestScanRcloneProgress_ZeroBytesTransferred_ReturnsEmpty(t *testing.T) {
	input := "Transferred:   0 B / 0 B, -, 0 B/s, ETA -\nChecks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	var called []string
	if err := scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	}); err != nil {
		t.Fatalf("scanRcloneProgress: %v", err)
	}

	if len(called) != 0 {
		t.Errorf("onProgress should not be called during check-only phase, got %d calls: %v", len(called), called)
	}
}

func TestScanRcloneProgress_ChecksOnly_ReturnsEmpty(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	var called []string
	if err := scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	}); err != nil {
		t.Fatalf("scanRcloneProgress: %v", err)
	}

	if len(called) != 0 {
		t.Errorf("onProgress should not be called for checks-only output, got %d calls", len(called))
	}
}

func TestScanRcloneProgress_CountOnlyTransferred_Ignored(t *testing.T) {
	// The count-only Transferred line (no /s) should not trigger progress
	input := "Transferred:   12 / 50, 24%\n"
	r := strings.NewReader(input)

	var called []string
	if err := scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	}); err != nil {
		t.Fatalf("scanRcloneProgress: %v", err)
	}

	if len(called) != 0 {
		t.Errorf("onProgress should not be called for count-only transferred line, got %d calls", len(called))
	}
}

func TestScanRcloneProgress_NilCallback(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	// Should not panic
	if err := scanRcloneProgress(r, nil); err != nil {
		t.Fatalf("scanRcloneProgress(nil): %v", err)
	}
}

func TestScanRcloneProgress_ConcatenatedPerFileProgress(t *testing.T) {
	// When piped, rclone's per-file progress line has no trailing delimiter.
	// The next "Transferred:" bytes line gets concatenated onto it, forming a
	// segment like: "* file.bin: 40% /1Mi, 100Ki/s, 5sTransferred: 512 KiB ..."
	input := " *                                      test.bin: 40% /1Mi, 219.991Ki/s, 2sTransferred:   \t      604 KiB / 1 MiB, 59%, 206 KiB/s, ETA 2s\n"
	r := strings.NewReader(input)

	var called []string
	if err := scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	}); err != nil {
		t.Fatalf("scanRcloneProgress: %v", err)
	}

	if len(called) == 0 {
		t.Fatal("onProgress was never called for concatenated per-file + Transferred line")
	}
	if !strings.Contains(called[0], "604 KiB / 1 MiB") {
		t.Errorf("progress = %q, want '604 KiB / 1 MiB' bytes line", called[0])
	}
}

func TestScanRcloneProgress_CheckThenTransfer(t *testing.T) {
	// Simulates a real rclone run: check-only phase (0 B / 0 B) produces no
	// progress, then an active transfer starts and progress appears.
	input := "Transferred:   0 B / 0 B, -, 0 B/s, ETA -\n" +
		"Checks:        500 / 1000, 50%\n" +
		"Transferred:   0 / 0\n" +
		"Elapsed time:  1.0s\n" +
		"Transferred:   512 KiB / 2 MiB, 25%, 512 KiB/s, ETA 3s\n" +
		"Checks:       1000 / 1000, 100%\n" +
		"Transferred:   1 / 5, 20%\n" +
		"Elapsed time:  2.0s\n"
	r := strings.NewReader(input)

	var called []string
	if err := scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	}); err != nil {
		t.Fatalf("scanRcloneProgress: %v", err)
	}

	if len(called) == 0 {
		t.Fatal("onProgress was never called after transfer started")
	}
	if !strings.Contains(called[0], "512 KiB / 2 MiB") {
		t.Errorf("first progress = %q, want bytes line from transfer phase", called[0])
	}
}

func TestScanRcloneProgress_CRDelimitedLines(t *testing.T) {
	// Rclone uses \r for in-place updates during transfers
	input := "Transferred:   1 GiB / 3 GiB, 33%, 5 MiB/s, ETA 10m\rTransferred:   2 GiB / 3 GiB, 66%, 5 MiB/s, ETA 5m\r"
	r := strings.NewReader(input)

	var called []string
	if err := scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	}); err != nil {
		t.Fatalf("scanRcloneProgress: %v", err)
	}

	if len(called) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(called))
	}
	if !strings.Contains(called[len(called)-1], "2 GiB / 3 GiB") {
		t.Errorf("last progress = %q, want latest transfer line", called[len(called)-1])
	}
}

// ---------------------------------------------------------------------------
// Rclone executor integration tests
//
// Exec tests use --config /dev/null via ExtraFlags to prevent rclone from
// reading the user's config. CleanupArchives tests use the RCLONE_CONFIG env
// var instead because CleanupArchives constructs its own rclone commands
// internally and does not accept extra flags.
// ---------------------------------------------------------------------------

func skipIfNoRclone(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not found on PATH")
	}
}

func newRcloneTestExecutor(t *testing.T) (*RcloneExecutor, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "rclone-test.log")
	logger := newTestLogger(t)
	return NewRcloneExecutor(logger, logPath), logPath
}

func TestRcloneExec_CopyFile_Succeeds(t *testing.T) {
	skipIfNoRclone(t)
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	executor, logPath := newRcloneTestExecutor(t)
	job := config.Job{ExtraFlags: []string{"--config", "/dev/null"}}
	args := BuildRcloneArgs("copy", nil, job, src+"/", ":local:"+dst, false, logPath, "")
	result := executor.Exec(context.Background(), args, nil)
	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	content, err := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if err != nil {
		t.Fatalf("reading copied file: %v", err)
	}
	if string(content) != "world" {
		t.Errorf("file content = %q, want 'world'", string(content))
	}
}

func TestRcloneExec_CopyDir_Succeeds(t *testing.T) {
	skipIfNoRclone(t)
	src := t.TempDir()
	subDir := filepath.Join(src, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("shallow"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	dst := t.TempDir()
	executor, logPath := newRcloneTestExecutor(t)
	job := config.Job{ExtraFlags: []string{"--config", "/dev/null"}}
	args := BuildRcloneArgs("copy", nil, job, src+"/", ":local:"+dst, false, logPath, "")
	result := executor.Exec(context.Background(), args, nil)
	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if _, err := os.Stat(filepath.Join(dst, "top.txt")); err != nil {
		t.Error("top.txt should exist at dest")
	}
	if _, err := os.Stat(filepath.Join(dst, "sub", "nested.txt")); err != nil {
		t.Error("sub/nested.txt should exist at dest")
	}
}

func TestRcloneExec_SyncDir_DeletesExtra(t *testing.T) {
	skipIfNoRclone(t)
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("remove"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	executor, logPath := newRcloneTestExecutor(t)
	job := config.Job{ExtraFlags: []string{"--config", "/dev/null"}}
	args := BuildRcloneArgs("sync", nil, job, src+"/", ":local:"+dst, false, logPath, "")
	result := executor.Exec(context.Background(), args, nil)
	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Error("stale.txt should have been deleted by sync")
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.txt")); err != nil {
		t.Error("keep.txt should still exist")
	}
}

func TestRcloneExec_SyncDir_BackupDir_PreservesDeleted(t *testing.T) {
	skipIfNoRclone(t)
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "victim.txt"), []byte("save me"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	backupDir := t.TempDir()
	executor, logPath := newRcloneTestExecutor(t)
	job := config.Job{ExtraFlags: []string{"--config", "/dev/null"}}
	backupDirArg := ":local:" + backupDir
	args := BuildRcloneArgs("sync", nil, job, src+"/", ":local:"+dst, false, logPath, backupDirArg)
	result := executor.Exec(context.Background(), args, nil)
	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if _, err := os.Stat(filepath.Join(dst, "victim.txt")); !os.IsNotExist(err) {
		t.Error("victim.txt should be removed from dest")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "victim.txt")); err != nil {
		t.Error("victim.txt should be preserved in backup dir")
	}
}

func TestRcloneExec_MissingSource_Fails(t *testing.T) {
	skipIfNoRclone(t)
	dst := t.TempDir()
	executor, logPath := newRcloneTestExecutor(t)
	job := config.Job{ExtraFlags: []string{"--config", "/dev/null"}}
	args := BuildRcloneArgs("copy", nil, job, "/nonexistent/path/does/not/exist", ":local:"+dst, false, logPath, "")
	result := executor.Exec(context.Background(), args, nil)
	if result.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", result.Status)
	}
}

// ---------------------------------------------------------------------------
// CleanupArchives guard-clause tests (no rclone needed)
// ---------------------------------------------------------------------------

func TestCleanupArchives_DryRun_Skips(t *testing.T) {
	executor, _ := newRcloneTestExecutor(t)
	err := executor.CleanupArchives(context.Background(), "remote", "/some/path", 30, true)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestCleanupArchives_EmptyBackupPath_Skips(t *testing.T) {
	executor, _ := newRcloneTestExecutor(t)
	err := executor.CleanupArchives(context.Background(), "remote", "", 30, false)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestCleanupArchives_ZeroRetention_Skips(t *testing.T) {
	executor, _ := newRcloneTestExecutor(t)
	err := executor.CleanupArchives(context.Background(), "remote", "/some/path", 0, false)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CleanupArchives integration tests (require rclone + temp config)
// ---------------------------------------------------------------------------

// writeRcloneConfig creates a temp rclone config with a "testlocal" remote
// of type "local" and sets RCLONE_CONFIG. Returns a cleanup function.
// Not safe for t.Parallel() because it manipulates process-global env.
func writeRcloneConfig(t *testing.T) (cleanup func()) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(configPath, []byte("[testlocal]\ntype = local\n"), 0o644); err != nil {
		t.Fatalf("writing rclone config: %v", err)
	}
	prev, hadPrev := os.LookupEnv("RCLONE_CONFIG")
	os.Setenv("RCLONE_CONFIG", configPath) //nolint:errcheck
	return func() {
		if hadPrev {
			os.Setenv("RCLONE_CONFIG", prev) //nolint:errcheck
		} else {
			os.Unsetenv("RCLONE_CONFIG") //nolint:errcheck
		}
	}
}

func TestCleanupArchives_PurgesExpired(t *testing.T) {
	skipIfNoRclone(t)
	cleanup := writeRcloneConfig(t)
	defer cleanup()
	archiveRoot := t.TempDir()
	oldDate := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	oldDir := filepath.Join(archiveRoot, oldDate+"_backup")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("creating old archive dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "data.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("writing archive file: %v", err)
	}
	executor, _ := newRcloneTestExecutor(t)
	err := executor.CleanupArchives(context.Background(), "testlocal", archiveRoot, 7, false)
	if err != nil {
		t.Fatalf("CleanupArchives returned error: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("expired archive dir should have been purged")
	}
}

func TestCleanupArchives_KeepsRecent(t *testing.T) {
	skipIfNoRclone(t)
	cleanup := writeRcloneConfig(t)
	defer cleanup()
	archiveRoot := t.TempDir()
	todayDate := time.Now().Format("2006-01-02")
	recentDir := filepath.Join(archiveRoot, todayDate+"_backup")
	if err := os.MkdirAll(recentDir, 0o755); err != nil {
		t.Fatalf("creating recent archive dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recentDir, "data.txt"), []byte("recent"), 0o644); err != nil {
		t.Fatalf("writing archive file: %v", err)
	}
	executor, _ := newRcloneTestExecutor(t)
	err := executor.CleanupArchives(context.Background(), "testlocal", archiveRoot, 7, false)
	if err != nil {
		t.Fatalf("CleanupArchives returned error: %v", err)
	}
	if _, err := os.Stat(recentDir); err != nil {
		t.Error("recent archive dir should still exist")
	}
}

func TestRcloneExec_ExpiredContext_ReturnsTimedOut(t *testing.T) {
	skipIfNoRclone(t)

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	// A context whose deadline is already in the past will cause exec.CommandContext
	// to kill the process immediately, producing a DeadlineExceeded context error.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	executor, logPath := newRcloneTestExecutor(t)
	job := config.Job{ExtraFlags: []string{"--config", "/dev/null"}}
	args := BuildRcloneArgs("copy", nil, job, src+"/", ":local:"+dst, false, logPath, "")
	result := executor.Exec(ctx, args, nil)

	if result.Status != StatusTimedOut {
		t.Errorf("Status = %q, want %q", result.Status, StatusTimedOut)
	}
}
