package engine

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jkleinne/shuttle/internal/config"
)

type planRsyncRecorder struct {
	args [][]string
}

func (r *planRsyncRecorder) Exec(_ context.Context, args []string, _ func(string)) ItemResult {
	r.args = append(r.args, slices.Clone(args))
	return ItemResult{Status: StatusOK}
}

type planRcloneRecorder struct {
	args     [][]string
	cleanups []ArchiveCleanupRequest
}

func (r *planRcloneRecorder) Exec(_ context.Context, args []string, _ func(string)) ItemResult {
	r.args = append(r.args, slices.Clone(args))
	return ItemResult{Status: StatusOK}
}

func (r *planRcloneRecorder) CleanupArchives(_ context.Context, request ArchiveCleanupRequest) error {
	r.cleanups = append(r.cleanups, request)
	return nil
}

func runPlannedTest(
	t *testing.T,
	plan RunPlan,
	rsync RsyncCommandExecutor,
	rclone RcloneCommandExecutor,
) Summary {
	t.Helper()
	runner, err := NewRunner(RunnerConfig{
		Plan:          plan,
		ConfigPath:    filepath.Join(t.TempDir(), "config.toml"),
		Logger:        &stubRunnerLogger{},
		Progress:      &stubRunnerProgress{},
		Prerequisites: &stubPrerequisiteChecker{},
		Locker:        &stubRunLocker{},
		Rsync:         rsync,
		Rclone:        rclone,
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	summary, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return summary
}

func TestBuildRunPlan_SelectionPreservesExecutionOrder(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	cfg := &config.Config{
		Jobs: []config.Job{
			{
				Name:        "skipped-local",
				Engine:      config.EngineRsync,
				Sources:     []string{source},
				Destination: destination,
			},
			{
				Name:    "cloud",
				Engine:  config.EngineRclone,
				Source:  source,
				Remotes: []string{"north", "south", "east"},
				Mode:    config.ModeCopy,
			},
			{
				Name:        "selected-local",
				Engine:      config.EngineRsync,
				Sources:     []string{source},
				Destination: destination,
			},
		},
	}

	plan, err := BuildRunPlan(cfg, RunOptions{
		DryRun:          true,
		SkipJobs:        []string{"skipped-local"},
		SelectedRemotes: []string{"east", "north"},
	})
	if err != nil {
		t.Fatalf("BuildRunPlan() error = %v", err)
	}
	if !plan.RequiresRclone() {
		t.Fatal("RequiresRclone() = false, want true")
	}

	summary := runPlannedTest(t, plan, &planRsyncRecorder{}, &planRcloneRecorder{})

	if !summary.DryRun {
		t.Error("Summary.DryRun = false, want true")
	}
	got := make([]string, 0, len(summary.Jobs))
	for _, job := range summary.Jobs {
		got = append(got, jobLabel(job.Name, job.Remote))
	}
	want := []string{"skipped-local", "cloud:north", "cloud:east", "selected-local"}
	if !slices.Equal(got, want) {
		t.Errorf("execution order = %v, want %v", got, want)
	}
	if status := summary.Jobs[0].Items[0].Status; status != StatusSkipped {
		t.Errorf("skipped placeholder status = %q, want %q", status, StatusSkipped)
	}
}

func TestRunner_DoesNotExposePrimaryLogPathToExecutors(t *testing.T) {
	rsyncSource := t.TempDir()
	rcloneSource := t.TempDir()
	primaryLogPath := filepath.Join(t.TempDir(), "primary.log")
	cfg := &config.Config{Jobs: []config.Job{
		{
			Name:        "local",
			Engine:      config.EngineRsync,
			Sources:     []string{rsyncSource},
			Destination: t.TempDir(),
		},
		{
			Name:    "cloud",
			Engine:  config.EngineRclone,
			Source:  rcloneSource,
			Remotes: []string{"remote"},
			Mode:    config.ModeCopy,
		},
	}}
	plan, err := BuildRunPlan(cfg, RunOptions{})
	if err != nil {
		t.Fatalf("BuildRunPlan() error = %v", err)
	}
	rsync := &planRsyncRecorder{}
	rclone := &planRcloneRecorder{}
	runner, err := NewRunner(RunnerConfig{
		Plan:          plan,
		ConfigPath:    filepath.Join(t.TempDir(), "config.toml"),
		Logger:        &stubRunnerLogger{},
		Progress:      &stubRunnerProgress{},
		Prerequisites: &stubPrerequisiteChecker{},
		Locker:        &stubRunLocker{},
		Rsync:         rsync,
		Rclone:        rclone,
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, invocation := range append(rsync.args, rclone.args...) {
		if strings.Contains(strings.Join(invocation, "\x00"), primaryLogPath) {
			t.Errorf("executor arguments expose primary log path %q: %v", primaryLogPath, invocation)
		}
	}
}

func TestBuildRunPlan_NoRunnableOperations(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	tests := []struct {
		name string
		cfg  *config.Config
		opts RunOptions
	}{
		{
			name: "empty config",
			cfg:  &config.Config{},
		},
		{
			name: "all jobs skipped",
			cfg: &config.Config{Jobs: []config.Job{{
				Name:        "local",
				Engine:      config.EngineRsync,
				Sources:     []string{source},
				Destination: destination,
			}}},
			opts: RunOptions{SkipJobs: []string{"local"}},
		},
		{
			name: "selected remote matches no selected job",
			cfg: &config.Config{Jobs: []config.Job{
				{
					Name:    "north-cloud",
					Engine:  config.EngineRclone,
					Source:  source,
					Remotes: []string{"north"},
					Mode:    config.ModeCopy,
				},
				{
					Name:    "south-cloud",
					Engine:  config.EngineRclone,
					Source:  source,
					Remotes: []string{"south"},
					Mode:    config.ModeCopy,
				},
			}},
			opts: RunOptions{
				OnlyJobs:        []string{"north-cloud"},
				SelectedRemotes: []string{"south"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildRunPlan(test.cfg, test.opts)
			if !errors.Is(err, ErrNoJobsSelected) {
				t.Fatalf("BuildRunPlan() error = %v, want ErrNoJobsSelected", err)
			}
			if !strings.Contains(err.Error(), "no jobs selected") {
				t.Errorf("error = %q, want actionable no-jobs context", err)
			}
		})
	}
}

func TestBuildRunPlan_NilConfigWrapsInvalidPlan(t *testing.T) {
	_, err := BuildRunPlan(nil, RunOptions{})
	if !errors.Is(err, ErrInvalidRunPlan) {
		t.Fatalf("BuildRunPlan(nil) error = %v, want ErrInvalidRunPlan", err)
	}
}

func TestBuildRunPlan_OptionalMissingSourceStillCountsAsOperation(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{{
		Name:        "optional-device",
		Engine:      config.EngineRsync,
		Sources:     []string{filepath.Join(t.TempDir(), "missing")},
		Destination: t.TempDir(),
		Optional:    true,
	}}}

	if _, err := BuildRunPlan(cfg, RunOptions{}); err != nil {
		t.Fatalf("BuildRunPlan() error = %v, want selected optional operation", err)
	}
}

func TestBuildRunPlan_MutationsDoNotChangeExecutionSnapshot(t *testing.T) {
	localSource := t.TempDir()
	cloudSource := t.TempDir()
	destination := t.TempDir()
	defaultRsyncFlags := []string{"--archive"}
	defaultRcloneFlags := []string{"--fast-list"}
	localExtraFlags := []string{"--checksum"}
	cloudExtraFlags := []string{"--metadata"}
	localSources := []string{localSource}
	cloudRemotes := []string{"primary", "secondary"}
	onlyJobs := []string{"local", "cloud"}
	selectedRemotes := []string{"primary"}
	cfg := &config.Config{
		Defaults: &config.Defaults{
			Rsync: &config.RsyncDefaults{Flags: defaultRsyncFlags},
			Rclone: &config.RcloneDefaults{
				Flags: defaultRcloneFlags,
				RcloneTuning: config.RcloneTuning{
					Transfers: 2,
					Bwlimit:   "4M",
				},
			},
		},
		Jobs: []config.Job{
			{
				Name:        "local",
				Engine:      config.EngineRsync,
				ExtraFlags:  localExtraFlags,
				Sources:     localSources,
				Destination: destination,
			},
			{
				Name:       "cloud",
				Engine:     config.EngineRclone,
				ExtraFlags: cloudExtraFlags,
				Source:     cloudSource,
				Remotes:    cloudRemotes,
				Mode:       config.ModeCopy,
			},
		},
	}
	opts := RunOptions{
		DryRun:          true,
		OnlyJobs:        onlyJobs,
		SelectedRemotes: selectedRemotes,
	}
	plan, err := BuildRunPlan(cfg, opts)
	if err != nil {
		t.Fatalf("BuildRunPlan() error = %v", err)
	}

	defaultRsyncFlags[0] = "--mutated-rsync-default"
	defaultRcloneFlags[0] = "--mutated-rclone-default"
	localExtraFlags[0] = "--mutated-local-extra"
	cloudExtraFlags[0] = "--mutated-cloud-extra"
	localSources[0] = filepath.Join(t.TempDir(), "mutated-source")
	cloudRemotes[0] = "mutated-remote"
	onlyJobs[0] = "mutated-job"
	selectedRemotes[0] = "mutated-remote"
	cfg.Defaults.Rclone.Transfers = 99
	cfg.Defaults.Rclone.Bwlimit = "99G"
	cfg.Jobs[1].Source = filepath.Join(t.TempDir(), "mutated-cloud-source")

	rsync := &planRsyncRecorder{}
	rclone := &planRcloneRecorder{}
	summary := runPlannedTest(t, plan, rsync, rclone)

	if len(summary.Jobs) != 2 {
		t.Fatalf("summary jobs = %d, want 2", len(summary.Jobs))
	}
	if len(rsync.args) != 1 {
		t.Fatalf("rsync calls = %d, want 1", len(rsync.args))
	}
	rsyncCommand := strings.Join(rsync.args[0], " ")
	for _, original := range []string{"--archive", "--checksum", localSource} {
		if !strings.Contains(rsyncCommand, original) {
			t.Errorf("rsync args = %q, want original %q", rsyncCommand, original)
		}
	}
	for _, mutation := range []string{"--mutated-rsync-default", "--mutated-local-extra", "mutated-source"} {
		if strings.Contains(rsyncCommand, mutation) {
			t.Errorf("rsync args = %q, retained mutation %q", rsyncCommand, mutation)
		}
	}
	if len(rclone.args) != 1 {
		t.Fatalf("rclone calls = %d, want 1", len(rclone.args))
	}
	rcloneCommand := strings.Join(rclone.args[0], " ")
	for _, original := range []string{"--fast-list", "--metadata", "--transfers 2", "--bwlimit 4M", cloudSource, "primary:"} {
		if !strings.Contains(rcloneCommand, original) {
			t.Errorf("rclone args = %q, want original %q", rcloneCommand, original)
		}
	}
	for _, mutation := range []string{"--mutated-rclone-default", "--mutated-cloud-extra", "--transfers 99", "--bwlimit 99G", "mutated-cloud-source", "mutated-remote"} {
		if strings.Contains(rcloneCommand, mutation) {
			t.Errorf("rclone args = %q, retained mutation %q", rcloneCommand, mutation)
		}
	}
}

func TestBuildRunPlan_SkipOptionMutationDoesNotSelectSkippedJob(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	skipJobs := []string{"skipped"}
	cfg := &config.Config{Jobs: []config.Job{
		{
			Name:        "skipped",
			Engine:      config.EngineRsync,
			Sources:     []string{source},
			Destination: destination,
		},
		{
			Name:        "selected",
			Engine:      config.EngineRsync,
			Sources:     []string{source},
			Destination: destination,
		},
	}}
	plan, err := BuildRunPlan(cfg, RunOptions{SkipJobs: skipJobs})
	if err != nil {
		t.Fatalf("BuildRunPlan() error = %v", err)
	}
	skipJobs[0] = "selected"

	rsync := &planRsyncRecorder{}
	summary := runPlannedTest(t, plan, rsync, &planRcloneRecorder{})

	if len(rsync.args) != 1 {
		t.Fatalf("rsync calls = %d, want 1", len(rsync.args))
	}
	if summary.Jobs[0].Items[0].Status != StatusSkipped {
		t.Errorf("first job status = %q, want %q", summary.Jobs[0].Items[0].Status, StatusSkipped)
	}
}
