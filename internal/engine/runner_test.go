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

func TestShouldRunJob_SkipLogic(t *testing.T) {
	tests := []struct {
		name    string
		skip    []string
		only    []string
		jobName string
		wantRun bool
	}{
		{"no filters", nil, nil, "photos", true},
		{"skip photos", []string{"photos"}, nil, "photos", false},
		{"skip photos, run projects", []string{"photos"}, nil, "projects", true},
		{"only docs", nil, []string{"docs"}, "photos", false},
		{"only photos", nil, []string{"photos"}, "photos", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRunJob(tt.jobName, tt.skip, tt.only)
			if got != tt.wantRun {
				t.Errorf("shouldRunJob(%q) = %v, want %v", tt.jobName, got, tt.wantRun)
			}
		})
	}
}

func TestLockFilePath_DifferentConfigs(t *testing.T) {
	r1 := &Runner{configPath: "/home/user/.config/shuttle/config.toml"}
	r2 := &Runner{configPath: "/home/user/alt/shuttle/config.toml"}
	r3 := &Runner{configPath: "/home/user/.config/shuttle/config.toml"}

	if r1.lockFilePath() == r2.lockFilePath() {
		t.Error("different config paths should produce different lock paths")
	}
	if r1.lockFilePath() != r3.lockFilePath() {
		t.Error("same config path should produce same lock path")
	}
	wantPrefix := filepath.Join(os.TempDir(), "shuttle-")
	if !strings.HasPrefix(r1.lockFilePath(), wantPrefix) {
		t.Errorf("lock path should start with %q, got %q", wantPrefix, r1.lockFilePath())
	}
}

func TestTargetRemotes_NoSelection(t *testing.T) {
	r := &Runner{}
	got := r.targetRemotes([]string{"gdrive", "koofr"}, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 remotes, got %d", len(got))
	}
}

func TestTargetRemotes_WithSelection(t *testing.T) {
	r := &Runner{}
	got := r.targetRemotes([]string{"gdrive", "koofr"}, []string{"gdrive"})
	if len(got) != 1 || got[0] != "gdrive" {
		t.Errorf("expected [gdrive], got %v", got)
	}
}

func TestTargetRemotes_SelectionNotInJob(t *testing.T) {
	r := &Runner{}
	got := r.targetRemotes([]string{"gdrive"}, []string{"onedrive"})
	if len(got) != 0 {
		t.Errorf("expected empty (no overlap), got %v", got)
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
	logger, err := log.NewWithWriter(termBuf, logFile, false, log.VerbosityNormal)
	if err != nil {
		t.Fatalf("creating logger: %v", err)
	}
	pw := NewProgressWriter(io.Discard, false, false)
	return NewRunner(&config.Config{}, "", logger, pw, false, logFile)
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

	result := r.runRcloneJob(context.Background(), job, "crypt_gdrive", "2026-04-16_120000")

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

	result := r.runRcloneJob(context.Background(), job, "crypt_gdrive", "2026-04-16_120000")

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

	result := r.runRcloneJob(context.Background(), job, "any-remote", "2026-04-18_000000")

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
		name    string
		ctx     context.Context
		runErr  error
		want    Status
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
			name:   "deadline exceeded context, non-nil error",
			ctx:    func() context.Context { c, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second)); t.Cleanup(cancel); return c }(),
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
