package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

const lockFilePath = "/tmp/shuttle.lock"

// RunOptions holds the CLI flags that control a sync run.
type RunOptions struct {
	DryRun          bool
	SkipJobs        []string
	OnlyJobs        []string
	SelectedRemotes []string
	RcloneOverrides []string
}

// Runner orchestrates sync jobs and cloud uploads. It checks prerequisites,
// acquires a file lock to prevent concurrent runs, dispatches jobs in config
// order, and collects results into a Summary.
type Runner struct {
	cfg      *config.Config
	logger   *log.Logger
	rsync    *RsyncExecutor
	rclone   *RcloneExecutor
	lockFile *os.File // held open to maintain flock; released on process exit
}

// NewRunner creates a Runner wired to the given config and logger. The dryRun
// flag propagates to both the rsync and rclone executors. logFile is the path
// to the shared log file used by both executors for stats capture.
func NewRunner(cfg *config.Config, logger *log.Logger, dryRun bool, logFile string) *Runner {
	return &Runner{
		cfg:    cfg,
		logger: logger,
		rsync:  NewRsyncExecutor(logger, dryRun, logFile),
		rclone: NewRcloneExecutor(logger, dryRun, logFile),
	}
}

// Run executes the full sync pipeline: prerequisites, lock, sync jobs, cloud
// uploads. Partial failures are recorded in the summary but do not stop
// subsequent jobs.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (Summary, error) {
	runCloud := shouldRunJob("cloud", opts.SkipJobs, opts.OnlyJobs)

	if err := r.checkPrerequisites(opts, runCloud); err != nil {
		return Summary{}, fmt.Errorf("prerequisites: %w", err)
	}

	if err := r.acquireLock(); err != nil {
		return Summary{}, err
	}

	start := time.Now()
	timestamp := start.Format("2006-01-02_150405")

	var jobs []JobResult

	// Sync jobs in config order.
	for _, job := range r.cfg.SyncJobs {
		if !shouldRunJob(job.Name, opts.SkipJobs, opts.OnlyJobs) {
			jobs = append(jobs, JobResult{
				Name:  job.Name,
				Items: []ItemResult{{Name: job.Name, Status: StatusSkipped}},
			})
			continue
		}
		r.logger.Header(fmt.Sprintf("Syncing: %s", job.Name))
		jobResult := r.runSyncJob(ctx, job)
		jobs = append(jobs, jobResult)
	}

	// Cloud uploads: remote x item matrix.
	if runCloud && r.cfg.Cloud != nil {
		remotes := r.targetRemotes(opts.SelectedRemotes)
		filterFile := expandTilde("~/.config/rclone/filters.txt")

		rcloneOpts := RcloneOpts{
			Mode:         r.cfg.Cloud.Mode,
			TuningFlags:  r.buildTuningFlags(opts.RcloneOverrides),
			BackupPath:   r.cfg.Cloud.BackupPath,
			RunTimestamp: timestamp,
			FilterFile:   filterFile,
		}

		for _, remote := range remotes {
			r.logger.Header(fmt.Sprintf("Cloud upload: %s [mode: %s]", remote, r.cfg.Cloud.Mode))
			r.rclone.CleanupArchives(ctx, remote, r.cfg.Cloud.BackupPath, r.cfg.Cloud.BackupRetentionDays)
			jobResult := r.runCloudJob(ctx, remote, rcloneOpts)
			jobs = append(jobs, jobResult)
		}
	} else if !runCloud {
		jobs = append(jobs, JobResult{
			Name:  "cloud",
			Items: []ItemResult{{Name: "cloud", Status: StatusSkipped}},
		})
	}

	summary := Summary{
		Jobs:     jobs,
		Duration: time.Since(start),
		DryRun:   opts.DryRun,
	}

	for _, j := range jobs {
		for _, item := range j.Items {
			if item.Status == StatusFailed {
				summary.Errors = append(summary.Errors, fmt.Sprintf("%s/%s", j.Name, item.Name))
			}
		}
	}

	return summary, nil
}

// runSyncJob iterates each source in the job, resolves the path, and calls
// rsync. Sources that cannot be stat'd are recorded as StatusNotFound.
func (r *Runner) runSyncJob(ctx context.Context, job config.SyncJob) JobResult {
	var items []ItemResult
	for _, source := range job.Sources {
		resolved, isDir, err := expandPath(source)
		if err != nil {
			r.logger.Error(fmt.Sprintf("Source not found: %s: %v", source, err))
			items = append(items, ItemResult{
				Name:   filepath.Base(source),
				Status: StatusNotFound,
			})
			continue
		}

		rsyncOpts := RsyncOpts{
			Delete:    job.Delete && isDir,
			ExtraOpts: job.ExtraOpts,
		}

		r.logger.Info(fmt.Sprintf("Source: %s", resolved))
		r.logger.Info(fmt.Sprintf("Destination: %s", job.Destination))

		result := r.rsync.Exec(ctx, resolved, job.Destination, rsyncOpts)
		items = append(items, result)
	}
	return JobResult{Name: job.Name, Items: items}
}

// runCloudJob iterates each cloud item, resolves its source, builds the
// rclone destination path, and calls rclone for the given remote.
func (r *Runner) runCloudJob(ctx context.Context, remoteName string, opts RcloneOpts) JobResult {
	var items []ItemResult
	prefix := r.cfg.Cloud.RemotePath
	if prefix != "" {
		prefix = strings.TrimRight(prefix, "/") + "/"
	}

	for _, item := range r.cfg.Cloud.Items {
		resolved, isRemote, isDir, err := resolveCloudSource(item.Source, r.cfg.ExternalDrive)
		if err != nil {
			r.logger.Error(fmt.Sprintf("Skipping %s: %v", item.Source, err))
			items = append(items, ItemResult{
				Name:   filepath.Base(item.Source),
				Status: StatusNotFound,
			})
			continue
		}

		destName := cloudDestName(item, resolved, isRemote)

		var destination string
		switch {
		case destName == "":
			destination = fmt.Sprintf("%s:%s", remoteName, strings.TrimRight(prefix, "/"))
		case isDir || isRemote:
			destination = fmt.Sprintf("%s:%s%s/", remoteName, prefix, destName)
		default:
			destination = fmt.Sprintf("%s:%s%s", remoteName, prefix, destName)
		}

		// Append trailing slash to directory sources so rclone syncs contents.
		if !isRemote && isDir && !strings.HasSuffix(resolved, "/") {
			resolved += "/"
		}

		r.logger.Info(fmt.Sprintf("Source: %s", resolved))
		r.logger.Info(fmt.Sprintf("Destination: %s", destination))

		result := r.rclone.Exec(ctx, resolved, destination, remoteName, isDir, opts)
		result.Name = destName
		if destName == "" {
			result.Name = "(prefix root)"
		}
		items = append(items, result)
	}

	return JobResult{Name: "cloud:" + remoteName, Items: items}
}

// cloudDestName determines the destination folder name for a cloud item.
// Uses item.Destination if set, otherwise derives it from the source basename.
// For remote sources (containing ':'), extracts the path after the colon.
func cloudDestName(item config.CloudItem, resolved string, isRemote bool) string {
	if item.Destination != "" {
		return item.Destination
	}
	if isRemote {
		parts := strings.SplitN(item.Source, ":", 2)
		if len(parts) > 1 && parts[1] != "" && parts[1] != "/" {
			return filepath.Base(strings.TrimRight(parts[1], "/"))
		}
		return ""
	}
	return filepath.Base(strings.TrimRight(resolved, "/"))
}

// checkPrerequisites validates that required tools and paths exist. Each check
// is conditional on whether the corresponding job type will actually run.
func (r *Runner) checkPrerequisites(opts RunOptions, runCloud bool) error {
	hasSyncJobs := false
	for _, job := range r.cfg.SyncJobs {
		if shouldRunJob(job.Name, opts.SkipJobs, opts.OnlyJobs) {
			hasSyncJobs = true
			break
		}
	}

	if hasSyncJobs {
		if _, err := exec.LookPath("rsync"); err != nil {
			return fmt.Errorf("rsync not found on PATH")
		}
	}

	if runCloud {
		if _, err := exec.LookPath("rclone"); err != nil {
			return fmt.Errorf("rclone not found on PATH")
		}
		filterFile := expandTilde("~/.config/rclone/filters.txt")
		if _, err := os.Stat(filterFile); err != nil {
			return fmt.Errorf("rclone filter file not found: %s", filterFile)
		}
	}

	// No flock PATH check needed: locking uses syscall.Flock directly.

	if r.needsExternalDrive(opts) {
		if _, err := os.Stat(r.cfg.ExternalDrive); err != nil {
			return fmt.Errorf("external drive not found at %s", r.cfg.ExternalDrive)
		}
	}

	r.logger.Success("All prerequisites met.")
	return nil
}

// needsExternalDrive returns true when any active job references the
// configured external drive path (sync sources/destinations that start with
// ExternalDrive, or cloud items with relative paths that resolve against it).
func (r *Runner) needsExternalDrive(opts RunOptions) bool {
	for _, job := range r.cfg.SyncJobs {
		if !shouldRunJob(job.Name, opts.SkipJobs, opts.OnlyJobs) {
			continue
		}
		for _, src := range job.Sources {
			if strings.HasPrefix(src, r.cfg.ExternalDrive) {
				return true
			}
		}
		if strings.HasPrefix(job.Destination, r.cfg.ExternalDrive) {
			return true
		}
	}

	runCloud := shouldRunJob("cloud", opts.SkipJobs, opts.OnlyJobs)
	if runCloud && r.cfg.Cloud != nil {
		for _, item := range r.cfg.Cloud.Items {
			src := item.Source
			// Relative paths (not absolute, not tilde, not remote) resolve against the external drive.
			if !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "~") && !strings.Contains(src, ":") {
				return true
			}
		}
	}
	return false
}

// acquireLock obtains an exclusive, non-blocking file lock via syscall.Flock.
// The lock file descriptor is stored on the Runner to prevent GC from closing
// it and releasing the lock prematurely.
func (r *Runner) acquireLock() error {
	f, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("another instance is already running")
	}
	r.lockFile = f
	return nil
}

// targetRemotes returns the user-selected remotes if any were specified,
// otherwise falls back to all configured remote names.
func (r *Runner) targetRemotes(selected []string) []string {
	if len(selected) > 0 {
		return selected
	}
	return r.cfg.RemoteNames()
}

// buildTuningFlags translates CloudTuning struct fields to rclone flag strings.
// When overrides are provided (from CLI), those are used verbatim instead.
func (r *Runner) buildTuningFlags(overrides []string) []string {
	if len(overrides) > 0 {
		return overrides
	}
	if r.cfg.Cloud == nil {
		return nil
	}
	t := r.cfg.Cloud.Tuning
	var flags []string
	if t.Transfers > 0 {
		flags = append(flags, "--transfers", fmt.Sprintf("%d", t.Transfers))
	}
	if t.Checkers > 0 {
		flags = append(flags, "--checkers", fmt.Sprintf("%d", t.Checkers))
	}
	if t.Bwlimit != "" {
		flags = append(flags, "--bwlimit", t.Bwlimit)
	}
	if t.DriveChunkSize != "" {
		flags = append(flags, "--drive-chunk-size", t.DriveChunkSize)
	}
	if t.BufferSize != "" {
		flags = append(flags, "--buffer-size", t.BufferSize)
	}
	if t.UseMmap {
		flags = append(flags, "--use-mmap")
	}
	if t.Timeout != "" {
		flags = append(flags, "--timeout", t.Timeout)
	}
	if t.Contimeout != "" {
		flags = append(flags, "--contimeout", t.Contimeout)
	}
	if t.LowLevelRetries > 0 {
		flags = append(flags, "--low-level-retries", fmt.Sprintf("%d", t.LowLevelRetries))
	}
	if t.OrderBy != "" {
		flags = append(flags, "--order-by", t.OrderBy)
	}
	return flags
}

// ValidateJobNames checks that all names in skip and only are recognized job
// names (including "cloud" as a reserved word). Returns an error if skip and
// only are both non-empty, or if any name is unknown.
func ValidateJobNames(skip, only, jobNames []string) error {
	if len(skip) > 0 && len(only) > 0 {
		return fmt.Errorf("--skip and --only are mutually exclusive")
	}

	valid := map[string]bool{"cloud": true}
	for _, name := range jobNames {
		valid[name] = true
	}

	for _, name := range skip {
		if !valid[name] {
			return fmt.Errorf("unknown job %q in --skip; valid names: %v", name, sortedKeys(valid))
		}
	}
	for _, name := range only {
		if !valid[name] {
			return fmt.Errorf("unknown job %q in --only; valid names: %v", name, sortedKeys(valid))
		}
	}
	return nil
}

// shouldRunJob determines whether a named job should execute given the current
// skip and only filters. If only is set, the job must appear in it. If skip is
// set, the job must not appear in it.
func shouldRunJob(name string, skip, only []string) bool {
	if len(only) > 0 {
		for _, o := range only {
			if o == name {
				return true
			}
		}
		return false
	}
	for _, s := range skip {
		if s == name {
			return false
		}
	}
	return true
}

// sortedKeys returns the keys of a bool map in sorted order.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
