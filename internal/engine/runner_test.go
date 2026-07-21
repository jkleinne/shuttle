package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

func TestValidateJobNames_ValidNames(t *testing.T) {
	jobNames := []string{"photos", "projects", "docs-to-cloud"}
	err := ValidateJobNames([]string{"photos"}, nil, jobNames)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateJobNames_UnknownName(t *testing.T) {
	jobNames := []string{"photos", "projects"}
	err := ValidateJobNames([]string{"typo"}, nil, jobNames)
	if err == nil {
		t.Fatal("expected error for unknown name, got nil")
	}
}

func TestValidateJobNames_SkipAndOnlyConflict(t *testing.T) {
	jobNames := []string{"photos"}
	err := ValidateJobNames([]string{"photos"}, []string{"photos"}, jobNames)
	if err == nil {
		t.Fatal("expected error for skip+only conflict, got nil")
	}
}

type stubRunnerLogger struct{}

func (l *stubRunnerLogger) Header(string)     {}
func (l *stubRunnerLogger) Info(string)       {}
func (l *stubRunnerLogger) Success(string)    {}
func (l *stubRunnerLogger) Warn(string)       {}
func (l *stubRunnerLogger) Error(string)      {}
func (l *stubRunnerLogger) Debug(string)      {}
func (l *stubRunnerLogger) FileHeader(string) {}
func (l *stubRunnerLogger) FileInfo(string)   {}
func (l *stubRunnerLogger) FileWarn(string)   {}
func (l *stubRunnerLogger) FileError(string)  {}

type stubRunnerProgress struct{}

func (*stubRunnerProgress) Interactive() bool                { return false }
func (*stubRunnerProgress) SkipJob(string)                   {}
func (*stubRunnerProgress) StartJob(context.Context, string) {}
func (*stubRunnerProgress) ProgressCallback() func(string)   { return nil }
func (*stubRunnerProgress) FinishJob(ItemResult)             {}

type stubPrerequisiteChecker struct {
	requests []PrerequisiteRequest
	err      error
	events   *[]string
}

func (s *stubPrerequisiteChecker) Check(request PrerequisiteRequest) error {
	if s.events != nil {
		*s.events = append(*s.events, "prerequisites")
	}
	request.FilterFiles = append([]string(nil), request.FilterFiles...)
	s.requests = append(s.requests, request)
	return s.err
}

type stubRunLocker struct {
	configPaths  []string
	acquireErr   error
	releaseErr   error
	releaseCalls int
	events       *[]string
}

func (s *stubRunLocker) Acquire(configPath string) error {
	if s.events != nil {
		*s.events = append(*s.events, "acquire")
	}
	s.configPaths = append(s.configPaths, configPath)
	return s.acquireErr
}

func (s *stubRunLocker) Release() error {
	if s.events != nil {
		*s.events = append(*s.events, "release")
	}
	s.releaseCalls++
	return s.releaseErr
}

type stubRsyncCommandExecutor struct {
	calls  int
	args   [][]string
	events *[]string
}

func (s *stubRsyncCommandExecutor) Exec(_ context.Context, args []string, _ func(string)) ItemResult {
	if s.events != nil {
		*s.events = append(*s.events, "rsync")
	}
	s.calls++
	s.args = append(s.args, append([]string(nil), args...))
	return ItemResult{Status: StatusOK}
}

type stubRcloneCommandExecutor struct {
	events *[]string
}

func (s *stubRcloneCommandExecutor) Exec(context.Context, []string, func(string)) ItemResult {
	if s.events != nil {
		*s.events = append(*s.events, "rclone")
	}
	return ItemResult{Status: StatusOK}
}

func (s *stubRcloneCommandExecutor) CleanupArchives(context.Context, ArchiveCleanupRequest) error {
	if s.events != nil {
		*s.events = append(*s.events, "cleanup")
	}
	return nil
}

func buildTestRunPlan(t *testing.T, cfg *config.Config, options RunOptions) RunPlan {
	t.Helper()
	plan, err := BuildRunPlan(cfg, options)
	if err != nil {
		t.Fatalf("BuildRunPlan() error = %v", err)
	}
	return plan
}

func validRunnerConfig(t *testing.T, plan RunPlan) RunnerConfig {
	t.Helper()
	return RunnerConfig{
		Plan:          plan,
		ConfigPath:    filepath.Join(t.TempDir(), "config.toml"),
		Logger:        &stubRunnerLogger{},
		Progress:      &stubRunnerProgress{},
		Prerequisites: &stubPrerequisiteChecker{},
		Locker:        &stubRunLocker{},
		Rsync:         &stubRsyncCommandExecutor{},
		Rclone:        &stubRcloneCommandExecutor{},
	}
}

func baselineRunPlan(t *testing.T) RunPlan {
	t.Helper()
	return buildTestRunPlan(t, &config.Config{Jobs: []config.Job{{
		Name:        "baseline",
		Engine:      config.EngineRsync,
		Sources:     []string{filepath.Join(t.TempDir(), "missing")},
		Destination: t.TempDir(),
		Optional:    true,
	}}}, RunOptions{})
}

func TestNewRunner_InvalidInput_ReturnsContextualError(t *testing.T) {
	tests := []struct {
		name      string
		change    func(*RunnerConfig)
		wantError string
	}{
		{
			name:      "zero plan",
			change:    func(rc *RunnerConfig) { rc.Plan = RunPlan{} },
			wantError: "invalid run plan",
		},
		{
			name:      "empty config path",
			change:    func(rc *RunnerConfig) { rc.ConfigPath = "" },
			wantError: "config path",
		},
		{
			name:      "relative config path",
			change:    func(rc *RunnerConfig) { rc.ConfigPath = "config.toml" },
			wantError: "config path",
		},
		{
			name:      "nil logger",
			change:    func(rc *RunnerConfig) { rc.Logger = nil },
			wantError: "logger",
		},
		{
			name:      "nil progress",
			change:    func(rc *RunnerConfig) { rc.Progress = nil },
			wantError: "progress",
		},
		{
			name:      "nil prerequisites",
			change:    func(rc *RunnerConfig) { rc.Prerequisites = nil },
			wantError: "prerequisites",
		},
		{
			name:      "nil locker",
			change:    func(rc *RunnerConfig) { rc.Locker = nil },
			wantError: "locker",
		},
		{
			name:      "nil rsync",
			change:    func(rc *RunnerConfig) { rc.Rsync = nil },
			wantError: "rsync",
		},
		{
			name:      "nil rclone",
			change:    func(rc *RunnerConfig) { rc.Rclone = nil },
			wantError: "rclone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := validRunnerConfig(t, baselineRunPlan(t))
			tt.change(&rc)

			runner, err := NewRunner(rc)

			if err == nil {
				t.Fatalf("NewRunner() = (%v, nil), want contextual error", runner)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantError)
			}
			if tt.name == "zero plan" && !errors.Is(err, ErrInvalidRunPlan) {
				t.Errorf("error = %v, want it to wrap ErrInvalidRunPlan", err)
			}
		})
	}
}

func TestNewRunner_TypedNilPorts_ReturnPlainNilErrors(t *testing.T) {
	tests := []struct {
		name      string
		change    func(*RunnerConfig)
		wantError string
	}{
		{
			name: "logger",
			change: func(rc *RunnerConfig) {
				var logger *stubRunnerLogger
				rc.Logger = logger
			},
			wantError: "invalid runner config: logger is nil",
		},
		{
			name: "progress",
			change: func(rc *RunnerConfig) {
				var progress *stubRunnerProgress
				rc.Progress = progress
			},
			wantError: "invalid runner config: progress is nil",
		},
		{
			name: "prerequisites",
			change: func(rc *RunnerConfig) {
				var prerequisites *stubPrerequisiteChecker
				rc.Prerequisites = prerequisites
			},
			wantError: "invalid runner config: prerequisites is nil",
		},
		{
			name: "locker",
			change: func(rc *RunnerConfig) {
				var locker *stubRunLocker
				rc.Locker = locker
			},
			wantError: "invalid runner config: locker is nil",
		},
		{
			name: "rsync",
			change: func(rc *RunnerConfig) {
				var rsync *stubRsyncCommandExecutor
				rc.Rsync = rsync
			},
			wantError: "invalid runner config: rsync executor is nil",
		},
		{
			name: "rclone",
			change: func(rc *RunnerConfig) {
				var rclone *stubRcloneCommandExecutor
				rc.Rclone = rclone
			},
			wantError: "invalid runner config: rclone executor is nil",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runnerConfig := validRunnerConfig(t, baselineRunPlan(t))
			test.change(&runnerConfig)

			assertNewRunnerRejectsTypedNil(t, runnerConfig, test.wantError)
		})
	}
}

func assertNewRunnerRejectsTypedNil(t *testing.T, runnerConfig RunnerConfig, wantError string) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("NewRunner() panicked for typed nil port: %v", recovered)
		}
	}()

	runner, err := NewRunner(runnerConfig)
	if err == nil {
		t.Fatalf("NewRunner() = (%v, nil), want %q", runner, wantError)
	}
	if err.Error() != wantError {
		t.Errorf("NewRunner() error = %q, want %q", err, wantError)
	}
}

func TestRunner_Run_UsesInjectedRsyncExecutor(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	cfg := &config.Config{
		Jobs: []config.Job{{
			Name:        "documents",
			Engine:      config.EngineRsync,
			Sources:     []string{source},
			Destination: destination,
		}},
	}
	rc := validRunnerConfig(t, buildTestRunPlan(t, cfg, RunOptions{}))
	prerequisites := rc.Prerequisites.(*stubPrerequisiteChecker)
	locker := rc.Locker.(*stubRunLocker)
	rsync := rc.Rsync.(*stubRsyncCommandExecutor)
	runner, err := NewRunner(rc)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	summary, err := runner.Run(context.Background())

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if rsync.calls != 1 {
		t.Errorf("rsync calls = %d, want 1", rsync.calls)
	}
	if len(prerequisites.requests) != 1 {
		t.Errorf("prerequisite checks = %d, want 1", len(prerequisites.requests))
	}
	if len(locker.configPaths) != 1 {
		t.Errorf("lock acquisitions = %d, want 1", len(locker.configPaths))
	}
	if locker.releaseCalls != 1 {
		t.Errorf("lock releases = %d, want 1", locker.releaseCalls)
	}
	if got := summary.Jobs[0].Items[0].Status; got != StatusOK {
		t.Errorf("status = %q, want %q", got, StatusOK)
	}
}

func TestRunner_Run_BoundaryOrder(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{{
		Name:                "cloud",
		Engine:              config.EngineRclone,
		Source:              t.TempDir(),
		Remotes:             []string{"remote"},
		Mode:                config.ModeCopy,
		BackupPath:          "archive",
		BackupRetentionDays: 30,
	}}}
	rc := validRunnerConfig(t, buildTestRunPlan(t, cfg, RunOptions{}))
	var events []string
	rc.Prerequisites = &stubPrerequisiteChecker{events: &events}
	rc.Locker = &stubRunLocker{events: &events}
	rc.Rclone = &stubRcloneCommandExecutor{events: &events}
	runner, err := NewRunner(rc)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"prerequisites", "acquire", "cleanup", "rclone", "release"}
	if !slices.Equal(events, want) {
		t.Errorf("boundary order = %v, want %v", events, want)
	}
}

func TestRunner_Run_BoundaryFailuresShortCircuit(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{{
		Name:        "local",
		Engine:      config.EngineRsync,
		Sources:     []string{t.TempDir()},
		Destination: t.TempDir(),
	}}}
	plan := buildTestRunPlan(t, cfg, RunOptions{})
	prerequisiteErr := errors.New("prerequisite failure")
	lockErr := errors.New("lock failure")
	tests := []struct {
		name             string
		prerequisiteErr  error
		lockErr          error
		wantErr          error
		wantEvents       []string
		wantReleaseCalls int
	}{
		{
			name:            "prerequisite failure",
			prerequisiteErr: prerequisiteErr,
			wantErr:         prerequisiteErr,
			wantEvents:      []string{"prerequisites"},
		},
		{
			name:       "lock failure",
			lockErr:    lockErr,
			wantErr:    lockErr,
			wantEvents: []string{"prerequisites", "acquire"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rc := validRunnerConfig(t, plan)
			var events []string
			locker := &stubRunLocker{
				acquireErr: test.lockErr,
				events:     &events,
			}
			rc.Prerequisites = &stubPrerequisiteChecker{
				err:    test.prerequisiteErr,
				events: &events,
			}
			rc.Locker = locker
			rc.Rsync = &stubRsyncCommandExecutor{events: &events}
			runner, err := NewRunner(rc)
			if err != nil {
				t.Fatalf("NewRunner() error = %v", err)
			}

			_, err = runner.Run(context.Background())

			if !errors.Is(err, test.wantErr) {
				t.Errorf("Run() error = %v, want %v", err, test.wantErr)
			}
			if !slices.Equal(events, test.wantEvents) {
				t.Errorf("boundary events = %v, want %v", events, test.wantEvents)
			}
			if locker.releaseCalls != test.wantReleaseCalls {
				t.Errorf(
					"release calls = %d, want %d",
					locker.releaseCalls,
					test.wantReleaseCalls,
				)
			}
		})
	}
}

func TestRunner_Run_ReturnsReleaseFailureAfterExecution(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{{
		Name:        "local",
		Engine:      config.EngineRsync,
		Sources:     []string{t.TempDir()},
		Destination: t.TempDir(),
	}}}
	rc := validRunnerConfig(t, buildTestRunPlan(t, cfg, RunOptions{}))
	releaseErr := errors.New("release failure")
	locker := &stubRunLocker{releaseErr: releaseErr}
	rc.Locker = locker
	runner, err := NewRunner(rc)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	summary, err := runner.Run(context.Background())

	if !errors.Is(err, releaseErr) {
		t.Fatalf("Run() error = %v, want release failure", err)
	}
	if len(summary.Jobs) != 1 || summary.Jobs[0].Items[0].Status != StatusOK {
		t.Errorf("Run() summary = %+v, want completed job before release failure", summary)
	}
	if locker.releaseCalls != 1 {
		t.Errorf("release calls = %d, want 1", locker.releaseCalls)
	}
}

// newTestRunner builds a Runner with nil cfg defaults, a logger writing to
// the supplied buffer, and a non-interactive ProgressWriter to io.Discard.
// Delegates to NewRunner so any future field added to Runner is initialized
// by the real constructor rather than silently zero-valued here.
// Suitable for unit-testing the missing-source branches that do not invoke
// rsync or rclone.
func newTestRunner(t *testing.T, termBuf *bytes.Buffer) *Runner {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(termBuf, logFile, log.Options{
		UseColor:  false,
		Verbosity: log.VerbosityNormal,
	})
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	t.Cleanup(logger.Close)
	pw := NewProgressWriter(io.Discard, ProgressOptions{})
	runner, err := NewRunner(RunnerConfig{
		Plan:          baselineRunPlan(t),
		ConfigPath:    filepath.Join(t.TempDir(), "config.toml"),
		Logger:        logger,
		Progress:      pw,
		Prerequisites: &stubPrerequisiteChecker{},
		Locker:        &stubRunLocker{},
		Rsync:         NewRsyncExecutor(logger),
		Rclone:        NewRcloneExecutor(logger, ""),
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	return runner
}

func TestRunRsyncJob_Optional_MissingSource_MarksOptionalMissing(t *testing.T) {
	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	job := config.Job{
		Name:        "koreader",
		Engine:      config.EngineRsync,
		Sources:     []string{missing},
		Destination: t.TempDir(),
		Optional:    true,
	}

	result := r.runRsyncJob(context.Background(), job)

	if len(result.Items) != 1 {
		t.Fatalf("Items count = %d, want 1", len(result.Items))
	}
	if result.Items[0].Status != StatusOptionalMissing {
		t.Errorf("Status = %q, want %q", result.Items[0].Status, StatusOptionalMissing)
	}
	if !strings.Contains(termBuf.String(), "optional") {
		t.Errorf("expected log output to mention 'optional', got: %s", termBuf.String())
	}
}

func TestRunRsyncJob_NotOptional_MissingSource_MarksNotFound(t *testing.T) {
	// Regression: existing non-optional behavior must be preserved.
	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	job := config.Job{
		Name:        "photos",
		Engine:      config.EngineRsync,
		Sources:     []string{missing},
		Destination: t.TempDir(),
		// Optional defaults to false
	}

	result := r.runRsyncJob(context.Background(), job)

	if result.Items[0].Status != StatusNotFound {
		t.Errorf("Status = %q, want %q", result.Items[0].Status, StatusNotFound)
	}
}

func TestRunRsyncJob_Optional_MultiSource_PresentAndMissing(t *testing.T) {
	// Pins the per-source granularity claim: with Optional=true, a present
	// source still syncs normally while a missing source becomes
	// StatusOptionalMissing. Requires rsync on PATH because the present
	// source is actually copied through rsync.
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}

	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	srcPresent := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcPresent, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("seeding source: %v", err)
	}
	srcMissing := filepath.Join(t.TempDir(), "does-not-exist")
	dest := t.TempDir()

	job := config.Job{
		Name:        "multi",
		Engine:      config.EngineRsync,
		Sources:     []string{srcPresent, srcMissing},
		Destination: dest,
		Optional:    true,
	}

	result := r.runRsyncJob(context.Background(), job)

	if len(result.Items) != 2 {
		t.Fatalf("Items count = %d, want 2", len(result.Items))
	}

	var seenOK, seenOptional bool
	for _, item := range result.Items {
		switch item.Status {
		case StatusOK:
			seenOK = true
		case StatusOptionalMissing:
			seenOptional = true
		default:
			t.Errorf("unexpected item status %q", item.Status)
		}
	}
	if !seenOK {
		t.Error("expected one StatusOK item for the present source")
	}
	if !seenOptional {
		t.Error("expected one StatusOptionalMissing item for the absent source")
	}
}

func TestRunRcloneJob_Optional_MissingLocalSource_MarksOptionalMissing(t *testing.T) {
	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	missing := filepath.Join(t.TempDir(), "koreader-absent")
	job := config.Job{
		Name:     "koreader-to-cloud",
		Engine:   config.EngineRclone,
		Source:   missing,
		Remotes:  []string{"crypt_gdrive"},
		Mode:     config.ModeCopy,
		Optional: true,
	}

	result := r.runRcloneJob(context.Background(), rcloneJobRequest{
		job:          job,
		remoteName:   "crypt_gdrive",
		runTimestamp: "2026-04-16_120000",
	})

	if len(result.Items) != 1 {
		t.Fatalf("Items count = %d, want 1", len(result.Items))
	}
	if result.Items[0].Status != StatusOptionalMissing {
		t.Errorf("Status = %q, want %q", result.Items[0].Status, StatusOptionalMissing)
	}
	if result.Remote != "crypt_gdrive" {
		t.Errorf("Remote = %q, want %q", result.Remote, "crypt_gdrive")
	}
	if !strings.Contains(termBuf.String(), "optional") {
		t.Errorf("expected log output to mention 'optional', got: %s", termBuf.String())
	}
}

func TestRunRcloneJob_NotOptional_MissingLocalSource_MarksNotFound(t *testing.T) {
	// Regression: existing non-optional behavior must be preserved.
	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	missing := filepath.Join(t.TempDir(), "absent")
	job := config.Job{
		Name:    "docs-to-cloud",
		Engine:  config.EngineRclone,
		Source:  missing,
		Remotes: []string{"crypt_gdrive"},
		Mode:    config.ModeCopy,
	}

	result := r.runRcloneJob(context.Background(), rcloneJobRequest{
		job:          job,
		remoteName:   "crypt_gdrive",
		runTimestamp: "2026-04-16_120000",
	})

	if result.Items[0].Status != StatusNotFound {
		t.Errorf("Status = %q, want %q", result.Items[0].Status, StatusNotFound)
	}
}

func TestJobContext_NoTimeout_HasNoDeadline(t *testing.T) {
	ctx, cancel := jobContext(context.Background(), 0)
	defer cancel()

	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		t.Error("jobContext(parent, 0) must not set a deadline")
	}
}

func TestJobContext_WithTimeout_SetsDeadline(t *testing.T) {
	const timeout = 1 * time.Hour
	before := time.Now()
	ctx, cancel := jobContext(context.Background(), timeout)
	defer cancel()

	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		t.Fatal("jobContext(parent, 1h) must set a deadline")
	}

	// Deadline should be approximately 1 hour from now (allow 5s of drift).
	wantMin := before.Add(timeout - 5*time.Second)
	wantMax := before.Add(timeout + 5*time.Second)
	if deadline.Before(wantMin) || deadline.After(wantMax) {
		t.Errorf("deadline %v not in expected range [%v, %v]", deadline, wantMin, wantMax)
	}
}

func TestRunRsyncJob_MaxRuntime_FiresAndReportsTimedOut(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}

	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	src := t.TempDir()
	// Seed enough files that rsync cannot finish in 1ms.
	for i := range 20 {
		name := filepath.Join(src, fmt.Sprintf("file%d.dat", i))
		if err := os.WriteFile(name, make([]byte, 1024*1024), 0o644); err != nil {
			t.Fatalf("seeding source: %v", err)
		}
	}

	job := config.Job{
		Name:        "timeout-job",
		Engine:      config.EngineRsync,
		Sources:     []string{src},
		Destination: t.TempDir(),
		MaxRuntime:  "1ms",
	}

	result := r.runRsyncJob(context.Background(), job)

	if len(result.Items) != 1 {
		t.Fatalf("Items count = %d, want 1", len(result.Items))
	}
	if result.Items[0].Status != StatusTimedOut {
		t.Errorf("Status = %q, want %q", result.Items[0].Status, StatusTimedOut)
	}
}

func TestRunRsyncJob_ParentCanceled_ReturnsFailedNotTimedOut(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}

	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("seeding source: %v", err)
	}

	// Pre-cancel the parent context before running the job.
	parentCtx, parentCancel := context.WithCancel(context.Background())
	parentCancel()

	job := config.Job{
		Name:        "canceled-job",
		Engine:      config.EngineRsync,
		Sources:     []string{src},
		Destination: t.TempDir(),
		MaxRuntime:  "1h", // long timeout so the cancellation comes from the parent
	}

	result := r.runRsyncJob(parentCtx, job)

	if len(result.Items) != 1 {
		t.Fatalf("Items count = %d, want 1", len(result.Items))
	}
	if result.Items[0].Status != StatusFailed {
		t.Errorf("Status = %q, want %q (parent cancel must not produce StatusTimedOut)", result.Items[0].Status, StatusFailed)
	}
}

func TestRunRcloneJob_MaxRuntime_FiresAndReportsTimedOut(t *testing.T) {
	// Symmetric to TestRunRsyncJob_MaxRuntime_FiresAndReportsTimedOut: pins
	// that the jobContext wiring at the rclone call site flows the deadline
	// through to RcloneExecutor and the resulting context error is classified
	// as StatusTimedOut. The executor-level rclone timeout test covers the
	// classification in isolation; this one covers the runner-level wiring.
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not found on PATH")
	}

	var termBuf bytes.Buffer
	r := newTestRunner(t, &termBuf)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("seeding source: %v", err)
	}

	job := config.Job{
		Name:    "timeout-rclone-job",
		Engine:  config.EngineRclone,
		Source:  src,
		Remotes: []string{"any-remote"},
		Mode:    config.ModeCopy,
		// --config /dev/null avoids touching the developer's real rclone config
		// during the test. The remote name is irrelevant because the 1ms
		// deadline kills the process before rclone resolves the remote.
		ExtraFlags: []string{"--config", "/dev/null"},
		MaxRuntime: "1ms",
	}

	result := r.runRcloneJob(context.Background(), rcloneJobRequest{
		job:          job,
		remoteName:   "any-remote",
		runTimestamp: "2026-04-18_000000",
	})

	if len(result.Items) != 1 {
		t.Fatalf("Items count = %d, want 1", len(result.Items))
	}
	if result.Items[0].Status != StatusTimedOut {
		t.Errorf("Status = %q, want %q", result.Items[0].Status, StatusTimedOut)
	}
}

func TestClassifyExitStatus(t *testing.T) {
	someErr := errors.New("command failed")

	// "deadline first then parent cancel": construct a context that has already
	// exceeded its deadline, then cancel the parent. context.Err() returns
	// whichever terminal state was reached first — DeadlineExceeded — and stays
	// there regardless of the subsequent cancel.
	parentCtx, parentCancel := context.WithCancel(context.Background())
	pastDeadline := time.Now().Add(-1 * time.Second)
	deadlineFirstCtx, deadlineFirstCancel := context.WithDeadline(parentCtx, pastDeadline)
	// Trigger the parent cancel so both conditions are true, but deadline was first.
	parentCancel()
	defer deadlineFirstCancel()

	tests := []struct {
		name   string
		ctx    context.Context
		runErr error
		want   Status
	}{
		{
			name:   "ok context, nil error",
			ctx:    context.Background(),
			runErr: nil,
			want:   StatusOK,
		},
		{
			name:   "ok context, non-nil error",
			ctx:    context.Background(),
			runErr: someErr,
			want:   StatusFailed,
		},
		{
			name: "deadline exceeded context, non-nil error",
			ctx: func() context.Context {
				c, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
				t.Cleanup(cancel)
				return c
			}(),
			runErr: someErr,
			want:   StatusTimedOut,
		},
		{
			name:   "canceled context, non-nil error",
			ctx:    func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }(),
			runErr: someErr,
			want:   StatusFailed,
		},
		{
			name:   "deadline first then parent cancel",
			ctx:    deadlineFirstCtx,
			runErr: someErr,
			want:   StatusTimedOut,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyExitStatus(tt.ctx, tt.runErr)
			if got != tt.want {
				t.Errorf("classifyExitStatus(...) = %q, want %q", got, tt.want)
			}
		})
	}
}
