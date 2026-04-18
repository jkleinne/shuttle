package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	pw         *ProgressWriter
	rsync      *RsyncExecutor
	rclone     *RcloneExecutor
	dryRun     bool
	logFile    string
	lockFile   *os.File // held open to maintain flock; released on process exit
}

// NewRunner creates a Runner for the given config. configPath is the
// absolute path to the config file (used for per-config locking).
// pw controls live terminal progress display. If nil, a non-interactive
// writer is created that prints plain status lines to io.Discard.
func NewRunner(cfg *config.Config, configPath string, logger *log.Logger, pw *ProgressWriter, dryRun bool, logFile string) *Runner {
	if pw == nil {
		pw = NewProgressWriter(io.Discard, false, false)
	}
	return &Runner{
		cfg:        cfg,
		configPath: configPath,
		logger:     logger,
		pw:         pw,
		rsync:      NewRsyncExecutor(logger),
		rclone:     NewRcloneExecutor(logger, logFile),
		dryRun:     dryRun,
		logFile:    logFile,
	}
}

// logHeader writes a section header. In interactive mode, output goes to the
// log file only so it doesn't interleave with the live spinner.
func (r *Runner) logHeader(msg string) {
	if r.pw.Interactive() {
		r.logger.FileHeader(msg)
	} else {
		r.logger.Header(msg)
	}
}

// logInfo writes an informational message, routed like logHeader.
func (r *Runner) logInfo(msg string) {
	if r.pw.Interactive() {
		r.logger.FileInfo(msg)
	} else {
		r.logger.Info(msg)
	}
}

// logError writes an error message, routed like logHeader.
func (r *Runner) logError(msg string) {
	if r.pw.Interactive() {
		r.logger.FileError(msg)
	} else {
		r.logger.Error(msg)
	}
}

// logWarn writes a warning, routed like logHeader so it does not interleave
// with the live spinner in interactive mode. The warning still lands in the
// log file; stderr output is skipped while the spinner owns the TTY.
func (r *Runner) logWarn(msg string) {
	if r.pw.Interactive() {
		r.logger.FileWarn(msg)
	} else {
		r.logger.Warn(msg)
	}
}

// formatExec joins argv into a single line for the "exec:" debug output.
// Arguments containing whitespace are quoted via strconv.Quote so the line
// is unambiguous; arguments without whitespace are left unquoted so the
// common case remains readable.
func formatExec(tool string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, tool)
	for _, a := range args {
		if strings.ContainsAny(a, " \t\n") {
			parts = append(parts, strconv.Quote(a))
		} else {
			parts = append(parts, a)
		}
	}
	return "exec: " + strings.Join(parts, " ")
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
		jobs = append(jobs, r.dispatchJob(ctx, job, opts, timestamp)...)
	}

	return Summary{
		Jobs:     jobs,
		Duration: time.Since(start),
		DryRun:   opts.DryRun,
		Errors:   collectErrors(jobs),
	}, nil
}

// dispatchJob routes a single configured job through the skip filter and its
// engine-specific execution path. Rsync jobs produce exactly one JobResult;
// rclone jobs produce one JobResult per target remote, or a single skipped
// result when the user's --remote filter excludes every configured remote.
func (r *Runner) dispatchJob(ctx context.Context, job config.Job, opts RunOptions, timestamp string) []JobResult {
	if !shouldRunJob(job.Name, opts.SkipJobs, opts.OnlyJobs) {
		r.pw.SkipJob(job.Name)
		return []JobResult{skippedJobResult(job.Name)}
	}

	switch job.Engine {
	case config.EngineRsync:
		r.logHeader(fmt.Sprintf("Syncing: %s", job.Name))
		return []JobResult{r.runRsyncJob(ctx, job)}

	case config.EngineRclone:
		return r.dispatchRclone(ctx, job, opts, timestamp)
	}
	return nil
}

// dispatchRclone expands an rclone job over its remotes, applying the
// user's --remote filter and running archive cleanup and the sync for each
// target. WarnFlagConflicts runs once per job (not per remote) since the
// flag set is identical across remotes.
func (r *Runner) dispatchRclone(ctx context.Context, job config.Job, opts RunOptions, timestamp string) []JobResult {
	var rcloneDefaults *config.RcloneDefaults
	if r.cfg.Defaults != nil {
		rcloneDefaults = r.cfg.Defaults.Rclone
	}
	WarnFlagConflicts(r.logger, "rclone", collectRcloneUserFlags(rcloneDefaults, job))

	remotes := r.targetRemotes(job.Remotes, opts.SelectedRemotes)
	if len(remotes) == 0 && len(opts.SelectedRemotes) > 0 {
		r.pw.SkipJob(job.Name)
		return []JobResult{skippedJobResult(job.Name)}
	}

	results := make([]JobResult, 0, len(remotes))
	for _, remote := range remotes {
		r.logHeader(fmt.Sprintf("Cloud upload: %s → %s [mode: %s]", job.Name, remote, job.Mode))
		if err := r.rclone.CleanupArchives(ctx, remote, job.BackupPath, job.BackupRetentionDays, r.dryRun); err != nil {
			r.logWarn(fmt.Sprintf("archive cleanup for %s: %v", remote, err))
		}
		results = append(results, r.runRcloneJob(ctx, job, remote, timestamp))
	}
	return results
}

// skippedJobResult builds the placeholder JobResult used when a job is filtered
// out by --skip, --only, or an empty --remote intersection.
func skippedJobResult(name string) JobResult {
	return JobResult{
		Name:  name,
		Items: []ItemResult{{Name: name, Status: StatusSkipped}},
	}
}

// collectErrors walks all item results and formats "<job>[→remote]/<item>"
// labels for each failed item. Delegates to Status.IsFailure so the
// failure predicate stays in a single place; see that method for the
// list of statuses that count as failures.
func collectErrors(jobs []JobResult) []string {
	var errs []string
	for _, j := range jobs {
		for _, item := range j.Items {
			if item.Status.IsFailure() {
				errs = append(errs, fmt.Sprintf("%s/%s", jobLabel(j.Name, j.Remote), item.Name))
			}
		}
	}
	return errs
}

// classifyExitStatus maps the combination of a context and a command run error
// to the appropriate Status. Call after the command has terminated or failed
// to start (both cmd.Start and cmd.Wait error paths).
//
// When ctx.Err() is context.DeadlineExceeded the job's per-invocation deadline
// elapsed, so StatusTimedOut is returned regardless of runErr. When ctx.Err()
// is context.Canceled the parent was cancelled (e.g. by a signal), which is
// treated as an ordinary failure. context.Err() returns whichever terminal
// state the context reached first and stays there, so the "deadline first then
// parent cancel" case naturally resolves to StatusTimedOut without any extra
// ordering logic.
func classifyExitStatus(ctx context.Context, runErr error) Status {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return StatusTimedOut
	}
	if runErr != nil {
		return StatusFailed
	}
	return StatusOK
}

// jobContext returns a context and cancel function for a single rsync or rclone
// invocation. When maxRuntime is zero the parent context is returned unchanged
// with a no-op cancel so callers can always defer cancel() safely. When
// maxRuntime is positive a child context with the corresponding deadline is
// returned; the caller is responsible for calling cancel to release the timer
// resource.
func jobContext(parent context.Context, maxRuntime time.Duration) (context.Context, context.CancelFunc) {
	if maxRuntime <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, maxRuntime)
}

// runRsyncJob iterates each source in the job and calls rsync.
func (r *Runner) runRsyncJob(ctx context.Context, job config.Job) JobResult {
	var items []ItemResult
	var defaults *config.RsyncDefaults
	if r.cfg.Defaults != nil {
		defaults = r.cfg.Defaults.Rsync
	}

	WarnFlagConflicts(r.logger, "rsync", collectRsyncUserFlags(defaults, job))

	multiSource := len(job.Sources) > 1

	for _, source := range job.Sources {
		resolved, isDir, err := statPath(source)
		if err != nil {
			label := job.Name
			if multiSource {
				label = fmt.Sprintf("%s · %s", job.Name, filepath.Base(source))
			}
			item := ItemResult{Name: filepath.Base(source)}
			if job.Optional {
				r.logWarn("Source not present (optional, skipping): " + source)
				item.Status = StatusOptionalMissing
			} else {
				r.logError(fmt.Sprintf("Source not found: %s: %v", source, err))
				item.Status = StatusNotFound
			}
			r.pw.StartJob(ctx, label)
			r.pw.FinishJob(item)
			items = append(items, item)
			continue
		}

		label := job.Name
		if multiSource {
			label = fmt.Sprintf("%s · %s", job.Name, filepath.Base(resolved))
		}

		r.logInfo(fmt.Sprintf("Source: %s", resolved))
		r.logInfo(fmt.Sprintf("Destination: %s", job.Destination))

		args := BuildRsyncArgs(defaults, job, resolved, job.Destination, job.Delete && isDir, r.dryRun, r.logFile)
		r.logger.Debug(formatExec("rsync", args))

		maxRuntime, _ := job.MaxRuntimeDuration()
		jobCtx, cancel := jobContext(ctx, maxRuntime)

		r.pw.StartJob(jobCtx, label)
		result := r.rsync.Exec(jobCtx, args, r.pw.ProgressCallback())
		cancel()
		r.pw.FinishJob(result)
		items = append(items, result)
	}
	return JobResult{Name: job.Name, Items: items}
}

// runRcloneJob runs rclone for a single source against a single remote.
// WarnFlagConflicts is called by the caller (Run) once per job, not here.
func (r *Runner) runRcloneJob(ctx context.Context, job config.Job, remoteName, timestamp string) JobResult {
	var rcloneDefaults *config.RcloneDefaults
	if r.cfg.Defaults != nil {
		rcloneDefaults = r.cfg.Defaults.Rclone
	}

	label := fmt.Sprintf("%s → %s", job.Name, remoteName)
	source := job.Source
	isRemote := isRcloneRemote(source)

	var isDir bool
	if isRemote {
		isDir = true
	} else {
		_, isDirStat, err := statPath(source)
		if err != nil {
			item := ItemResult{Name: filepath.Base(source)}
			if job.Optional {
				r.logWarn("Source not present (optional, skipping): " + source)
				item.Status = StatusOptionalMissing
			} else {
				r.logError(fmt.Sprintf("Skipping %s: %v", source, err))
				item.Status = StatusNotFound
			}
			r.pw.StartJob(ctx, label)
			r.pw.FinishJob(item)
			return JobResult{
				Name:   job.Name,
				Remote: remoteName,
				Items:  []ItemResult{item},
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

	r.logInfo(fmt.Sprintf("Source: %s", source))
	r.logInfo(fmt.Sprintf("Destination: %s", destination))

	subcommand, backupDirArg := selectMode(job.Mode, destination, remoteName, job.BackupPath, timestamp, isDir, r.logger)
	args := BuildRcloneArgs(subcommand, rcloneDefaults, job, source, destination, r.dryRun, r.logFile, backupDirArg)
	r.logger.Debug(formatExec("rclone", args))

	maxRuntime, _ := job.MaxRuntimeDuration()
	jobCtx, cancel := jobContext(ctx, maxRuntime)
	defer cancel()

	r.pw.StartJob(jobCtx, label)
	result := r.rclone.Exec(jobCtx, args, r.pw.ProgressCallback())
	result.Name = destName
	if destName == "" {
		result.Name = "(prefix root)"
	}
	r.pw.FinishJob(result)

	return JobResult{Name: job.Name, Remote: remoteName, Items: []ItemResult{result}}
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
		_ = f.Close()
		return fmt.Errorf("another instance is already running (lock: %s)", lockPath)
	}
	r.lockFile = f
	return nil
}

// lockFilePath returns <tempdir>/shuttle-<hash>.lock where hash is the first
// 8 hex characters of SHA-256 of the absolute config file path.
func (r *Runner) lockFilePath() string {
	h := sha256.Sum256([]byte(r.configPath))
	return filepath.Join(os.TempDir(), fmt.Sprintf("shuttle-%x.lock", h[:4]))
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
