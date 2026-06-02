package engine

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

// skipIfNoRsync skips the test when rsync is not on PATH, mirroring
// skipIfNoRclone (rclone_test.go) so the end-to-end suite degrades gracefully
// on machines without the external tools.
func skipIfNoRsync(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
}

// seedFile writes dir/name with the given content, failing the test on error.
func seedFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
}

// seedDir creates parent/name as a directory holding one file and returns the
// directory path. rsync receives the source without a trailing slash, so it
// copies the named directory into the dest, landing files at
// <dst>/<name>/<file>. A deterministic name keeps that path assertable.
func seedDir(t *testing.T, parent, name, file, content string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	seedFile(t, dir, file, content)
	return dir
}

// rsyncDefaults returns archive-mode defaults. Real configs always set
// [defaults.rsync] flags; without -a, rsync skips directory sources silently
// ("skipping directory"), so the copy would never happen and filesystem
// assertions would be meaningless.
func rsyncDefaults() *config.Defaults {
	return &config.Defaults{Rsync: &config.RsyncDefaults{Flags: []string{"-a"}}}
}

// runPipeline builds a Runner over cfg with a discard logger and a
// non-interactive ProgressWriter to io.Discard, runs the full pipeline, and
// returns the Summary. Pass a unique configPath under t.TempDir() so the
// per-config lock never collides with sibling tests. Fails the test if Run
// returns an error; scenarios that expect a Run error (lock contention) call
// NewRunner/Run directly instead.
func runPipeline(t *testing.T, cfg *config.Config, configPath string, opts RunOptions) Summary {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(io.Discard, logFile, false, log.VerbosityNormal)
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	pw := NewProgressWriter(io.Discard, false, false)
	runner := NewRunner(cfg, configPath, logger, pw, opts.DryRun, logFile)
	summary, err := runner.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return summary
}

func TestPipeline_MultiEngine_AllSucceed(t *testing.T) {
	skipIfNoRsync(t)
	skipIfNoRclone(t)
	cleanup := writeRcloneConfig(t) // sets RCLONE_CONFIG with a [testlocal] local remote
	defer cleanup()

	rsyncSrc := seedDir(t, t.TempDir(), "data", "local.txt", "local")
	rsyncDst := t.TempDir()

	rcloneSrc := t.TempDir()
	seedFile(t, rcloneSrc, "cloud.txt", "cloud")
	rcloneDst := t.TempDir()

	cfg := &config.Config{
		Defaults: rsyncDefaults(),
		Jobs: []config.Job{
			{
				Name:        "local-sync",
				Engine:      config.EngineRsync,
				Sources:     []string{rsyncSrc},
				Destination: rsyncDst,
			},
			{
				Name:        "cloud-sync",
				Engine:      config.EngineRclone,
				Source:      rcloneSrc,
				Destination: rcloneDst,
				Remotes:     []string{"testlocal"},
				Mode:        config.ModeCopy,
			},
		},
	}

	summary := runPipeline(t, cfg, filepath.Join(t.TempDir(), "config.toml"), RunOptions{})

	if len(summary.Jobs) != 2 {
		t.Fatalf("len(Jobs) = %d, want 2", len(summary.Jobs))
	}
	for _, job := range summary.Jobs {
		for _, item := range job.Items {
			if item.Status != StatusOK {
				t.Errorf("job %q item %q status = %q, want %q", job.Name, item.Name, item.Status, StatusOK)
			}
		}
	}
	if summary.HasErrors() {
		t.Errorf("HasErrors() = true, want false (Errors=%v)", summary.Errors)
	}
	if len(summary.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", summary.Errors)
	}
	// rsync source has no trailing slash, so "data" is copied into the dest.
	if _, err := os.Stat(filepath.Join(rsyncDst, "data", "local.txt")); err != nil {
		t.Errorf("rsync file not at expected path <dst>/data/local.txt: %v", err)
	}
	// The runner appends a trailing slash to the rclone local dir source, so its
	// contents are copied into the dest.
	if _, err := os.Stat(filepath.Join(rcloneDst, "cloud.txt")); err != nil {
		t.Errorf("rclone file not at expected path <dst>/cloud.txt: %v", err)
	}
}
