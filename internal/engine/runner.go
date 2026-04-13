package engine

import (
	"context"
	"crypto/sha256"
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

// RunOptions holds the CLI flags that control a sync run.
type RunOptions struct {
	DryRun          bool
	SkipJobs        []string
	OnlyJobs        []string
	SelectedRemotes []string
}

// Runner orchestrates jobs: checks prerequisites, acquires a per-config
// file lock, dispatches jobs in config order, and collects results.
type Runner struct {
	cfg        *config.Config
	configPath string
	logger     *log.Logger
	rsync      *RsyncExecutor
	rclone     *RcloneExecutor
	dryRun     bool
	logFile    string
	lockFile   *os.File // held open to maintain flock; released on process exit
}

// NewRunner creates a Runner for the given config. configPath is the
// absolute path to the config file (used for per-config locking).
func NewRunner(cfg *config.Config, configPath string, logger *log.Logger, dryRun bool, logFile string) *Runner {
	return &Runner{
		cfg:        cfg,
		configPath: configPath,
		logger:     logger,
		rsync:      NewRsyncExecutor(logger),
		rclone:     NewRcloneExecutor(logger, logFile),
		dryRun:     dryRun,
		logFile:    logFile,
	}
}

// Run executes the full pipeline: prerequisites, lock, jobs, summary.
// Partial failures are recorded in the summary but do not stop subsequent jobs.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (Summary, error) {
	if err := r.checkPrerequisites(opts); err != nil {
		return Summary{}, fmt.Errorf("prerequisites: %w", err)
	}

	if err := r.acquireLock(); err != nil {
		return Summary{}, err
	}

	start := time.Now()
	timestamp := start.Format("2006-01-02_150405")

	var jobs []JobResult

	for _, job := range r.cfg.Jobs {
		if !shouldRunJob(job.Name, opts.SkipJobs, opts.OnlyJobs) {
			jobs = append(jobs, JobResult{
				Name:  job.Name,
				Items: []ItemResult{{Name: job.Name, Status: StatusSkipped}},
			})
			continue
		}

		switch job.Engine {
		case config.EngineRsync:
			r.logger.Header(fmt.Sprintf("Syncing: %s", job.Name))
			jobResult := r.runRsyncJob(ctx, job)
			jobs = append(jobs, jobResult)

		case config.EngineRclone:
			remotes := r.targetRemotes(job.Remotes, opts.SelectedRemotes)
			for _, remote := range remotes {
				r.logger.Header(fmt.Sprintf("Cloud upload: %s → %s [mode: %s]", job.Name, remote, job.Mode))
				r.rclone.CleanupArchives(ctx, remote, job.BackupPath, job.BackupRetentionDays, r.dryRun)
				jobResult := r.runRcloneJob(ctx, job, remote, timestamp)
				jobs = append(jobs, jobResult)
			}
		}
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

// runRsyncJob iterates each source in the job and calls rsync.
func (r *Runner) runRsyncJob(ctx context.Context, job config.Job) JobResult {
	var items []ItemResult
	var defaults *config.RsyncDefaults
	if r.cfg.Defaults != nil {
		defaults = r.cfg.Defaults.Rsync
	}

	WarnFlagConflicts(r.logger, "rsync", collectRsyncUserFlags(defaults, job))

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

		r.logger.Info(fmt.Sprintf("Source: %s", resolved))
		r.logger.Info(fmt.Sprintf("Destination: %s", job.Destination))

		args := BuildRsyncArgs(defaults, job, resolved, job.Destination, job.Delete && isDir, r.dryRun, r.logFile)
		result := r.rsync.Exec(ctx, args)
		items = append(items, result)
	}
	return JobResult{Name: job.Name, Items: items}
}

// runRcloneJob runs rclone for a single source against a single remote.
func (r *Runner) runRcloneJob(ctx context.Context, job config.Job, remoteName, timestamp string) JobResult {
	var rcloneDefaults *config.RcloneDefaults
	if r.cfg.Defaults != nil {
		rcloneDefaults = r.cfg.Defaults.Rclone
	}

	WarnFlagConflicts(r.logger, "rclone", collectRcloneUserFlags(rcloneDefaults, job))

	source := job.Source
	isRemote := isRcloneRemote(source)

	var isDir bool
	if isRemote {
		isDir = true
	} else {
		_, isDirStat, err := expandPath(source)
		if err != nil {
			r.logger.Error(fmt.Sprintf("Skipping %s: %v", source, err))
			return JobResult{
				Name: job.Name + ":" + remoteName,
				Items: []ItemResult{{
					Name:   filepath.Base(source),
					Status: StatusNotFound,
				}},
			}
		}
		isDir = isDirStat
	}

	destName := rcloneDestName(job.Destination, source, isRemote)

	var destination string
	switch {
	case destName == "":
		destination = fmt.Sprintf("%s:", remoteName)
	case isDir || isRemote:
		destination = fmt.Sprintf("%s:%s/", remoteName, destName)
	default:
		destination = fmt.Sprintf("%s:%s", remoteName, destName)
	}

	if !isRemote && isDir && !strings.HasSuffix(source, "/") {
		source += "/"
	}

	r.logger.Info(fmt.Sprintf("Source: %s", source))
	r.logger.Info(fmt.Sprintf("Destination: %s", destination))

	subcommand, backupDirArg := selectMode(job.Mode, destination, remoteName, job.BackupPath, timestamp, isDir, r.logger)
	args := BuildRcloneArgs(subcommand, rcloneDefaults, job, source, destination, isDir, r.dryRun, r.logFile, backupDirArg)

	result := r.rclone.Exec(ctx, args)
	result.Name = destName
	if destName == "" {
		result.Name = "(prefix root)"
	}

	return JobResult{Name: job.Name + ":" + remoteName, Items: []ItemResult{result}}
}

// collectRsyncUserFlags gathers all user-provided flags for rsync conflict detection.
func collectRsyncUserFlags(defaults *config.RsyncDefaults, job config.Job) []string {
	var flags []string
	if defaults != nil {
		flags = append(flags, defaults.Flags...)
	}
	flags = append(flags, job.ExtraFlags...)
	return flags
}

// collectRcloneUserFlags gathers all user-provided flags for rclone conflict detection.
func collectRcloneUserFlags(defaults *config.RcloneDefaults, job config.Job) []string {
	var flags []string
	if defaults != nil {
		flags = append(flags, defaults.Flags...)
	}
	flags = append(flags, job.ExtraFlags...)
	return flags
}

// checkPrerequisites validates that required tools and filter files exist.
// Each check is conditional on whether the corresponding job type will
// actually run.
func (r *Runner) checkPrerequisites(opts RunOptions) error {
	needsRsync := false
	needsRclone := false

	for _, job := range r.cfg.Jobs {
		if !shouldRunJob(job.Name, opts.SkipJobs, opts.OnlyJobs) {
			continue
		}
		switch job.Engine {
		case config.EngineRsync:
			needsRsync = true
		case config.EngineRclone:
			needsRclone = true
		}
	}

	if needsRsync {
		if _, err := exec.LookPath("rsync"); err != nil {
			return fmt.Errorf("rsync not found on PATH")
		}
	}

	if needsRclone {
		if _, err := exec.LookPath("rclone"); err != nil {
			return fmt.Errorf("rclone not found on PATH")
		}
	}

	// Check filter files referenced by active rclone jobs.
	filterFiles := r.collectFilterFiles(opts)
	for _, ff := range filterFiles {
		if _, err := os.Stat(ff); err != nil {
			return fmt.Errorf("rclone filter file not found: %s", ff)
		}
	}

	r.logger.Success("All prerequisites met.")
	return nil
}

// collectFilterFiles returns all unique filter file paths that will be used
// by active rclone jobs (considering both default and per-job overrides).
func (r *Runner) collectFilterFiles(opts RunOptions) []string {
	seen := make(map[string]bool)
	var files []string

	defaultFilter := ""
	if r.cfg.Defaults != nil && r.cfg.Defaults.Rclone != nil {
		defaultFilter = r.cfg.Defaults.Rclone.FilterFile
	}

	for _, job := range r.cfg.Jobs {
		if job.Engine != config.EngineRclone {
			continue
		}
		if !shouldRunJob(job.Name, opts.SkipJobs, opts.OnlyJobs) {
			continue
		}

		ff := job.FilterFile
		if ff == "" {
			ff = defaultFilter
		}
		if ff != "" && !seen[ff] {
			seen[ff] = true
			files = append(files, ff)
		}
	}
	return files
}

// acquireLock obtains an exclusive, non-blocking file lock via syscall.Flock.
// The lock file path is derived from the config file path so that different
// configs can run concurrently.
func (r *Runner) acquireLock() error {
	lockPath := r.lockFilePath()
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("another instance is already running (lock: %s)", lockPath)
	}
	r.lockFile = f
	return nil
}

// lockFilePath returns /tmp/shuttle-<hash>.lock where hash is the first
// 8 hex characters of SHA-256 of the absolute config file path.
func (r *Runner) lockFilePath() string {
	h := sha256.Sum256([]byte(r.configPath))
	return fmt.Sprintf("/tmp/shuttle-%x.lock", h[:4])
}

// targetRemotes returns the intersection of the job's remotes and the
// user-selected remotes. If no selection was made, returns all job remotes.
func (r *Runner) targetRemotes(jobRemotes, selected []string) []string {
	if len(selected) == 0 {
		return jobRemotes
	}
	selectedSet := make(map[string]bool, len(selected))
	for _, s := range selected {
		selectedSet[s] = true
	}
	var filtered []string
	for _, remote := range jobRemotes {
		if selectedSet[remote] {
			filtered = append(filtered, remote)
		}
	}
	return filtered
}

// ValidateJobNames checks that all names in skip and only are recognized job
// names. Returns an error if skip and only are both non-empty, or if any name
// is unknown.
func ValidateJobNames(skip, only, jobNames []string) error {
	if len(skip) > 0 && len(only) > 0 {
		return fmt.Errorf("--skip and --only are mutually exclusive")
	}

	valid := make(map[string]bool, len(jobNames))
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
