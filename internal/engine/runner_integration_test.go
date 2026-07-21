package engine

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func newTestFileRunLocker(t *testing.T) *FileRunLocker {
	t.Helper()
	locker := NewFileRunLocker()
	t.Cleanup(func() {
		_ = locker.Release()
	})
	return locker
}

type blockingRsyncExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingRsyncExecutor) Exec(
	context.Context,
	[]string,
	func(string),
) ItemResult {
	close(e.started)
	<-e.release
	return ItemResult{Status: StatusOK}
}

// runPipeline builds a Runner over cfg with a discard logger and a
// non-interactive ProgressWriter to io.Discard, runs the full pipeline, and
// returns the Summary. Pass a unique configPath under t.TempDir() so the
// per-config lock never collides with sibling tests. Fails the test if Run
// returns an error; scenarios that expect a Run error (lock contention) call
// NewRunner/Run directly instead.
func runPipeline(t *testing.T, cfg *config.Config, configPath string, opts RunOptions) Summary {
	t.Helper()
	if err := os.WriteFile(configPath, []byte("# test config\n"), 0o600); err != nil {
		t.Fatalf("writing config identity fixture: %v", err)
	}
	plan, err := BuildRunPlan(cfg, opts)
	if err != nil {
		t.Fatalf("BuildRunPlan() error = %v", err)
	}
	logFile := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(io.Discard, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	t.Cleanup(logger.Close)
	pw := NewProgressWriter(io.Discard, ProgressOptions{})
	runner, err := NewRunner(RunnerConfig{
		Plan:          plan,
		ConfigPath:    configPath,
		Logger:        logger,
		Progress:      pw,
		Prerequisites: NewSystemPrerequisiteChecker(),
		Locker:        newTestFileRunLocker(t),
		Rsync:         NewRsyncExecutor(logger),
		Rclone:        NewRcloneExecutor(logger, ""),
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	summary, err := runner.Run(context.Background())
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

func TestPipeline_PartialFailure_ContinuesAndRecordsError(t *testing.T) {
	skipIfNoRsync(t)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	healthySrc := seedDir(t, t.TempDir(), "data", "keep.txt", "keep")
	healthyDst := t.TempDir()

	cfg := &config.Config{
		Defaults: rsyncDefaults(),
		Jobs: []config.Job{
			{Name: "broken", Engine: config.EngineRsync, Sources: []string{missing}, Destination: t.TempDir()},
			{Name: "healthy", Engine: config.EngineRsync, Sources: []string{healthySrc}, Destination: healthyDst},
		},
	}

	summary := runPipeline(t, cfg, filepath.Join(t.TempDir(), "config.toml"), RunOptions{})

	if len(summary.Jobs) != 2 {
		t.Fatalf("len(Jobs) = %d, want 2", len(summary.Jobs))
	}
	// Jobs are dispatched in config order: broken first, healthy second.
	if got := summary.Jobs[0].Items[0].Status; got != StatusNotFound {
		t.Errorf("broken status = %q, want %q", got, StatusNotFound)
	}
	if got := summary.Jobs[1].Items[0].Status; got != StatusOK {
		t.Errorf("healthy status = %q, want %q (pipeline must continue past the failure)", got, StatusOK)
	}
	if !summary.HasErrors() {
		t.Error("HasErrors() = false, want true")
	}
	if len(summary.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1 (Errors=%v)", len(summary.Errors), summary.Errors)
	}
	if !strings.Contains(summary.Errors[0], "broken") {
		t.Errorf("Errors[0] = %q, want it to name the 'broken' job", summary.Errors[0])
	}
	if _, err := os.Stat(filepath.Join(healthyDst, "data", "keep.txt")); err != nil {
		t.Errorf("healthy file should have landed: %v", err)
	}
}

func TestPipeline_DryRun_WritesNothing(t *testing.T) {
	skipIfNoRsync(t)

	src := seedDir(t, t.TempDir(), "data", "file.txt", "content")
	dst := t.TempDir()

	cfg := &config.Config{
		Defaults: rsyncDefaults(),
		Jobs: []config.Job{
			{Name: "dry", Engine: config.EngineRsync, Sources: []string{src}, Destination: dst},
		},
	}

	summary := runPipeline(t, cfg, filepath.Join(t.TempDir(), "config.toml"), RunOptions{DryRun: true})

	if !summary.DryRun {
		t.Error("Summary.DryRun = false, want true")
	}
	if got := summary.Jobs[0].Items[0].Status; got != StatusOK {
		t.Errorf("status = %q, want %q", got, StatusOK)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("reading dst: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dst not empty after dry-run: %v", entries)
	}
}

func TestPipeline_OnlyFilter_SkipsUnselectedJob(t *testing.T) {
	skipIfNoRsync(t)

	srcA := seedDir(t, t.TempDir(), "data", "a.txt", "a")
	dstA := t.TempDir()

	srcB := t.TempDir()
	seedFile(t, srcB, "b.txt", "b")
	dstB := t.TempDir()

	cfg := &config.Config{
		Defaults: rsyncDefaults(),
		Jobs: []config.Job{
			{Name: "job-a", Engine: config.EngineRsync, Sources: []string{srcA}, Destination: dstA},
			{Name: "job-b", Engine: config.EngineRsync, Sources: []string{srcB}, Destination: dstB},
		},
	}

	summary := runPipeline(t, cfg, filepath.Join(t.TempDir(), "config.toml"), RunOptions{OnlyJobs: []string{"job-a"}})

	if len(summary.Jobs) != 2 {
		t.Fatalf("len(Jobs) = %d, want 2", len(summary.Jobs))
	}
	jobB := summary.Jobs[1]
	if jobB.Name != "job-b" {
		t.Fatalf("Jobs[1].Name = %q, want job-b", jobB.Name)
	}
	if len(jobB.Items) != 1 || jobB.Items[0].Status != StatusSkipped {
		t.Errorf("job-b items = %+v, want exactly one StatusSkipped", jobB.Items)
	}
	if summary.HasErrors() {
		t.Errorf("HasErrors() = true, want false (skip is not a failure; Errors=%v)", summary.Errors)
	}
	entries, err := os.ReadDir(dstB)
	if err != nil {
		t.Fatalf("reading dstB: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("job-b dest not empty (job-b should have been skipped): %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dstA, "data", "a.txt")); err != nil {
		t.Errorf("job-a file should have landed: %v", err)
	}
}

func TestPipeline_LockContention_SecondRunRejected(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{{
		Name:        "local",
		Engine:      config.EngineRsync,
		Sources:     []string{t.TempDir()},
		Destination: t.TempDir(),
	}}}
	plan, err := BuildRunPlan(cfg, RunOptions{})
	if err != nil {
		t.Fatalf("BuildRunPlan() error = %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("# test config\n"), 0o600); err != nil {
		t.Fatalf("writing config identity fixture: %v", err)
	}
	blockingExecutor := &blockingRsyncExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	newRunner := func(rsync RsyncCommandExecutor) *Runner {
		runner, err := NewRunner(RunnerConfig{
			Plan:          plan,
			ConfigPath:    configPath,
			Logger:        &stubRunnerLogger{},
			Progress:      &stubRunnerProgress{},
			Prerequisites: &stubPrerequisiteChecker{},
			Locker:        newTestFileRunLocker(t),
			Rsync:         rsync,
			Rclone:        &stubRcloneCommandExecutor{},
		})
		if err != nil {
			t.Fatalf("NewRunner() error = %v", err)
		}
		return runner
	}

	run1 := newRunner(blockingExecutor)
	run1Done := make(chan error, 1)
	go func() {
		_, runErr := run1.Run(context.Background())
		run1Done <- runErr
	}()
	<-blockingExecutor.started

	run2 := newRunner(&stubRsyncCommandExecutor{})
	_, err = run2.Run(context.Background())
	if err == nil {
		t.Fatal("run2.Run returned nil, want a lock-contention error")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "already running")
	}
	close(blockingExecutor.release)
	if err := <-run1Done; err != nil {
		t.Fatalf("run1.Run returned error: %v", err)
	}
}

func TestPipeline_RcloneMultiRemote_ExpandsAdjacently(t *testing.T) {
	skipIfNoRclone(t)

	// Two local-type remotes in a temp rclone config, written inline rather than
	// extending writeRcloneConfig (which would force its existing testlocal
	// callers to keep compiling — needless churn for a single test).
	rcloneConf := filepath.Join(t.TempDir(), "rclone.conf")
	body := "[testlocal]\ntype = local\n\n[testlocal2]\ntype = local\n"
	if err := os.WriteFile(rcloneConf, []byte(body), 0o644); err != nil {
		t.Fatalf("writing rclone config: %v", err)
	}
	t.Setenv("RCLONE_CONFIG", rcloneConf)

	src := t.TempDir()
	seedFile(t, src, "cloud.txt", "cloud")
	dst := t.TempDir()

	cfg := &config.Config{
		Jobs: []config.Job{
			{
				Name:        "cloud-sync",
				Engine:      config.EngineRclone,
				Source:      src,
				Destination: dst,
				Remotes:     []string{"testlocal", "testlocal2"},
				Mode:        config.ModeCopy,
			},
		},
	}

	summary := runPipeline(t, cfg, filepath.Join(t.TempDir(), "config.toml"), RunOptions{})

	if len(summary.Jobs) != 2 {
		t.Fatalf("len(Jobs) = %d, want 2 (one JobResult per remote)", len(summary.Jobs))
	}
	// Both results belong to the same job and are adjacent in remote order.
	if summary.Jobs[0].Name != "cloud-sync" || summary.Jobs[1].Name != "cloud-sync" {
		t.Errorf("job names = [%q, %q], want both \"cloud-sync\"", summary.Jobs[0].Name, summary.Jobs[1].Name)
	}
	if summary.Jobs[0].Remote != "testlocal" || summary.Jobs[1].Remote != "testlocal2" {
		t.Errorf("remotes = [%q, %q], want [testlocal, testlocal2]", summary.Jobs[0].Remote, summary.Jobs[1].Remote)
	}
	for _, job := range summary.Jobs {
		if got := job.Items[0].Status; got != StatusOK {
			t.Errorf("remote %q status = %q, want %q", job.Remote, got, StatusOK)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "cloud.txt")); err != nil {
		t.Errorf("rclone file should have landed at <dst>/cloud.txt: %v", err)
	}
}
