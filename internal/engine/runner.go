package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jkleinne/shuttle/internal/config"
)

// RsyncCommandExecutor keeps rsync process I/O outside runner orchestration.
type RsyncCommandExecutor interface {
	// Exec runs one pre-built argument list so Runner does not own process I/O.
	Exec(context.Context, []string, func(string)) ItemResult
}

// RunnerLogger exposes only the logging behavior required during a run.
type RunnerLogger interface {
	WarningLogger
	// Header keeps noninteractive section boundaries visible to the user.
	Header(string)
	// Info reports noninteractive run context without exposing logger storage.
	Info(string)
	// Success reports completed boundary checks before job execution begins.
	Success(string)
	// Error reports noninteractive failures while preserving Runner's routing choice.
	Error(string)
	// Debug records command diagnostics without coupling Runner to verbosity state.
	Debug(string)
	// FileHeader keeps section boundaries in the log while interactive output owns the terminal.
	FileHeader(string)
	// FileInfo records run context without disturbing interactive terminal presentation.
	FileInfo(string)
	// FileWarn records warnings without disturbing interactive terminal presentation.
	FileWarn(string)
	// FileError records failures without disturbing interactive terminal presentation.
	FileError(string)
	// LogPath keeps command logging and logger storage pointed at one authoritative file.
	LogPath() string
}

// RunnerProgress keeps terminal presentation outside runner orchestration.
type RunnerProgress interface {
	// Interactive lets Runner route messages away from a terminal owned by live progress.
	Interactive() bool
	// SkipJob keeps filtered jobs visible without running their execution boundary.
	SkipJob(string)
	// StartJob begins one presentation lifecycle using the job's cancellation context.
	StartJob(context.Context, string)
	// ProgressCallback bridges executor updates into the active presentation lifecycle.
	ProgressCallback() func(string)
	// FinishJob closes the presentation lifecycle with the executor's result.
	FinishJob(ItemResult)
}

// PrerequisiteChecker keeps executable and filter-file probes at a system boundary.
type PrerequisiteChecker interface {
	// Check prevents a run from locking or executing when selected dependencies are unavailable.
	Check(PrerequisiteRequest) error
}

// RunLocker keeps filesystem locking outside runner orchestration.
type RunLocker interface {
	// Acquire prevents concurrent runs from sharing one absolute configuration identity.
	Acquire(string) error
}

// RcloneCommandExecutor keeps rclone process and archive-cleanup I/O outside orchestration.
type RcloneCommandExecutor interface {
	// Exec runs one pre-built argument list so Runner does not own process I/O.
	Exec(context.Context, []string, func(string)) ItemResult
	// CleanupArchives applies retention before a remote run without exposing rclone I/O to Runner.
	CleanupArchives(context.Context, ArchiveCleanupRequest) error
}

// Runner orchestrates validated jobs through injected boundary ports.
type Runner struct {
	plan          RunPlan
	configPath    string
	logger        RunnerLogger
	progress      RunnerProgress
	prerequisites PrerequisiteChecker
	locker        RunLocker
	rsync         RsyncCommandExecutor
	rclone        RcloneCommandExecutor
}

// RunnerConfig names the immutable plan, stable path, and six boundary ports consumed by a run.
type RunnerConfig struct {
	// Plan supplies one valid execution authority shared by every runner stage.
	Plan RunPlan
	// ConfigPath provides a stable absolute identity for per-config locking.
	ConfigPath string
	// Logger routes run diagnostics and supplies the authoritative absolute log path.
	Logger RunnerLogger
	// Progress owns terminal presentation for job lifecycle events.
	Progress RunnerProgress
	// Prerequisites validates external tools and filter files before locking.
	Prerequisites PrerequisiteChecker
	// Locker prevents concurrent runs of the same absolute config path.
	Locker RunLocker
	// Rsync executes pre-built rsync argument lists.
	Rsync RsyncCommandExecutor
	// Rclone executes pre-built rclone arguments and archive cleanup requests.
	Rclone RcloneCommandExecutor
}

// NewRunner rejects invalid boundary values before any run I/O can begin.
func NewRunner(rc RunnerConfig) (*Runner, error) {
	if !rc.Plan.valid {
		return nil, fmt.Errorf("invalid runner config: %w", ErrInvalidRunPlan)
	}
	if rc.ConfigPath == "" {
		return nil, errors.New("invalid runner config: config path is empty")
	}
	if !filepath.IsAbs(rc.ConfigPath) {
		return nil, fmt.Errorf("invalid runner config: config path %q is not absolute", rc.ConfigPath)
	}
	if isNilRunnerPort(rc.Logger) {
		return nil, errors.New("invalid runner config: logger is nil")
	}
	logPath := rc.Logger.LogPath()
	if logPath == "" {
		return nil, errors.New("invalid runner config: logger log path is empty")
	}
	if !filepath.IsAbs(logPath) {
		return nil, fmt.Errorf("invalid runner config: logger log path %q is not absolute", logPath)
	}
	if isNilRunnerPort(rc.Progress) {
		return nil, errors.New("invalid runner config: progress is nil")
	}
	if isNilRunnerPort(rc.Prerequisites) {
		return nil, errors.New("invalid runner config: prerequisites is nil")
	}
	if isNilRunnerPort(rc.Locker) {
		return nil, errors.New("invalid runner config: locker is nil")
	}
	if isNilRunnerPort(rc.Rsync) {
		return nil, errors.New("invalid runner config: rsync executor is nil")
	}
	if isNilRunnerPort(rc.Rclone) {
		return nil, errors.New("invalid runner config: rclone executor is nil")
	}
	return &Runner{
		plan:          rc.Plan,
		configPath:    rc.ConfigPath,
		logger:        rc.Logger,
		progress:      rc.Progress,
		prerequisites: rc.Prerequisites,
		locker:        rc.Locker,
		rsync:         rc.Rsync,
		rclone:        rc.Rclone,
	}, nil
}

func isNilRunnerPort(port any) bool {
	if port == nil {
		return true
	}
	portValue := reflect.ValueOf(port)
	switch portValue.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice,
		reflect.UnsafePointer:
		return portValue.IsNil()
	default:
		return false
	}
}

// logHeader writes a section header. In interactive mode, output goes to the
// log file only so it doesn't interleave with the live spinner.
func (r *Runner) logHeader(msg string) {
	if r.progress.Interactive() {
		r.logger.FileHeader(msg)
	} else {
		r.logger.Header(msg)
	}
}

// logInfo writes an informational message, routed like logHeader.
func (r *Runner) logInfo(msg string) {
	if r.progress.Interactive() {
		r.logger.FileInfo(msg)
	} else {
		r.logger.Info(msg)
	}
}

// logError writes an error message, routed like logHeader.
func (r *Runner) logError(msg string) {
	if r.progress.Interactive() {
		r.logger.FileError(msg)
	} else {
		r.logger.Error(msg)
	}
}

// logWarn writes a warning, routed like logHeader so it does not interleave
// with the live spinner in interactive mode. The warning still lands in the
// log file; stderr output is skipped while the spinner owns the TTY.
func (r *Runner) logWarn(msg string) {
	if r.progress.Interactive() {
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
func (r *Runner) Run(ctx context.Context) (Summary, error) {
	if err := r.checkPrerequisites(); err != nil {
		return Summary{}, fmt.Errorf("prerequisites: %w", err)
	}
	if err := r.locker.Acquire(r.configPath); err != nil {
		return Summary{}, err
	}

	start := time.Now()
	timestamp := start.Format("2006-01-02_150405")

	var jobs []JobResult
	for _, job := range r.plan.jobs {
		jobs = append(jobs, r.dispatchJob(ctx, job, timestamp)...)
	}

	return Summary{
		Jobs:     jobs,
		Duration: time.Since(start),
		DryRun:   r.plan.dryRun,
		Errors:   collectErrors(jobs),
	}, nil
}

// dispatchJob routes a single configured job through the skip filter and its
// engine-specific execution path. Rsync jobs produce exactly one JobResult;
// rclone jobs produce one JobResult per target remote, or a single skipped
// result when the user's --remote filter excludes every configured remote.
func (r *Runner) dispatchJob(ctx context.Context, planned plannedJob, timestamp string) []JobResult {
	if !planned.selected {
		r.progress.SkipJob(planned.job.Name)
		return []JobResult{skippedJobResult(planned.job.Name)}
	}

	switch planned.job.Engine {
	case config.EngineRsync:
		r.logHeader("Syncing: " + planned.job.Name)
		return []JobResult{r.runRsyncJob(ctx, planned.job)}

	case config.EngineRclone:
		return r.dispatchRclone(ctx, planned.job, planned.remotes, timestamp)
	}
	return nil
}

// dispatchRclone expands an rclone job over its remotes, applying the
// user's --remote filter and running archive cleanup and the sync for each
// target. WarnFlagConflicts runs once per job (not per remote) since the
// flag set is identical across remotes.
func (r *Runner) dispatchRclone(
	ctx context.Context,
	job config.Job,
	remotes []string,
	timestamp string,
) []JobResult {
	WarnFlagConflicts(
		r.logger,
		config.EngineRclone,
		collectRcloneUserFlags(r.plan.rcloneDefaults, job),
	)

	results := make([]JobResult, 0, len(remotes))
	for _, remote := range remotes {
		r.logHeader("Cloud upload: " + job.Name + " → " + remote + " [mode: " + job.Mode + "]")
		// CleanupArchives deliberately uses the parent ctx, not the per-invocation
		// jobCtx created inside runRcloneJob: a slow housekeeping pass shouldn't
		// inherit the job's max_runtime and cause the cleanup to time out.
		if err := r.rclone.CleanupArchives(ctx, ArchiveCleanupRequest{
			RemoteName:    remote,
			BackupPath:    job.BackupPath,
			RetentionDays: job.BackupRetentionDays,
			DryRun:        r.plan.dryRun,
		}); err != nil {
			r.logWarn("archive cleanup for " + remote + ": " + err.Error())
		}
		results = append(results, r.runRcloneJob(ctx, rcloneJobRequest{
			job:          job,
			remoteName:   remote,
			runTimestamp: timestamp,
		}))
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
	defaults := r.plan.rsyncDefaults

	WarnFlagConflicts(r.logger, config.EngineRsync, collectRsyncUserFlags(defaults, job))

	multiSource := len(job.Sources) > 1

	for _, source := range job.Sources {
		resolved, isDir, err := statPath(source)
		if err != nil {
			label := job.Name
			if multiSource {
				label = job.Name + " · " + filepath.Base(source)
			}
			item := ItemResult{Name: filepath.Base(source)}
			if job.Optional {
				r.logWarn("Source not present (optional, skipping): " + source)
				item.Status = StatusOptionalMissing
			} else {
				r.logError("Source not found: " + source + ": " + err.Error())
				item.Status = StatusNotFound
			}
			r.progress.StartJob(ctx, label)
			r.progress.FinishJob(item)
			items = append(items, item)
			continue
		}

		label := job.Name
		if multiSource {
			label = job.Name + " · " + filepath.Base(resolved)
		}

		r.logInfo("Source: " + resolved)
		r.logInfo("Destination: " + job.Destination)

		args := BuildRsyncArgs(RsyncArgsRequest{
			Defaults:    defaults,
			Job:         job,
			Source:      resolved,
			Destination: job.Destination,
			IsDeleteDir: job.Delete && isDir,
			DryRun:      r.plan.dryRun,
			LogFile:     r.logger.LogPath(),
		})
		r.logger.Debug(formatExec("rsync", args))

		// MaxRuntimeDuration returns (0, nil) for empty or (duration, nil) for
		// validated input. The LoadBytes path rejects bad values at parse time,
		// so discarding the error is safe here; callers bypassing validation
		// (e.g. direct Job literals in tests) see that branch instead.
		maxRuntime, _ := job.MaxRuntimeDuration()
		jobCtx, cancel := jobContext(ctx, maxRuntime)

		r.progress.StartJob(jobCtx, label)
		result := r.rsync.Exec(jobCtx, args, r.progress.ProgressCallback())
		cancel()
		r.progress.FinishJob(result)
		items = append(items, result)
	}
	return JobResult{Name: job.Name, Items: items}
}

type rcloneSource struct {
	value       string
	isDirectory bool
	isRemote    bool
}

type rcloneInvocation struct {
	source          rcloneSource
	sourceArgument  string
	destination     string
	destinationName string
}

type rcloneJobRequest struct {
	job          config.Job
	remoteName   string
	runTimestamp string
}

type rcloneArgumentsRequest struct {
	defaults     *config.RcloneDefaults
	job          config.Job
	invocation   rcloneInvocation
	remoteName   string
	runTimestamp string
	dryRun       bool
	logPath      string
}

func resolveRcloneSource(source string) (rcloneSource, error) {
	if isRcloneRemote(source) {
		return rcloneSource{value: source, isDirectory: true, isRemote: true}, nil
	}
	resolved, isDirectory, err := statPath(source)
	if err != nil {
		return rcloneSource{}, err
	}
	return rcloneSource{value: resolved, isDirectory: isDirectory}, nil
}

func prepareRcloneInvocation(
	source rcloneSource,
	jobDestination string,
	remoteName string,
) rcloneInvocation {
	destinationName := rcloneDestName(jobDestination, source.value)

	var destination string
	switch {
	case destinationName == "":
		destination = remoteName + ":"
	case source.isDirectory || source.isRemote:
		destination = remoteName + ":" + destinationName + "/"
	default:
		destination = remoteName + ":" + destinationName
	}

	sourceArgument := source.value
	if !source.isRemote && source.isDirectory && !strings.HasSuffix(sourceArgument, "/") {
		sourceArgument += "/"
	}
	return rcloneInvocation{
		source:          source,
		sourceArgument:  sourceArgument,
		destination:     destination,
		destinationName: destinationName,
	}
}

func (r *Runner) buildRcloneJobArguments(request rcloneArgumentsRequest) []string {
	subcommand, backupDirectoryArgument := selectMode(modeRequest{
		Mode:         request.job.Mode,
		Destination:  request.invocation.destination,
		RemoteName:   request.remoteName,
		BackupPath:   request.job.BackupPath,
		RunTimestamp: request.runTimestamp,
		IsDir:        request.invocation.source.isDirectory,
	}, r.logger)
	return BuildRcloneArgs(RcloneArgsRequest{
		Subcommand:   subcommand,
		Defaults:     request.defaults,
		Job:          request.job,
		Source:       request.invocation.sourceArgument,
		Destination:  request.invocation.destination,
		DryRun:       request.dryRun,
		LogFile:      request.logPath,
		BackupDirArg: backupDirectoryArgument,
	})
}

func (r *Runner) missingRcloneSourceResult(
	ctx context.Context,
	request rcloneJobRequest,
	sourceError error,
) JobResult {
	item := ItemResult{Name: filepath.Base(request.job.Source)}
	if request.job.Optional {
		r.logWarn("Source not present (optional, skipping): " + request.job.Source)
		item.Status = StatusOptionalMissing
	} else {
		r.logError("Skipping " + request.job.Source + ": " + sourceError.Error())
		item.Status = StatusNotFound
	}
	r.progress.StartJob(ctx, request.job.Name+" → "+request.remoteName)
	r.progress.FinishJob(item)
	return JobResult{
		Name:   request.job.Name,
		Remote: request.remoteName,
		Items:  []ItemResult{item},
	}
}

// runRcloneJob runs rclone for one request-shaped source and remote invocation.
func (r *Runner) runRcloneJob(ctx context.Context, request rcloneJobRequest) JobResult {
	defaults := r.plan.rcloneDefaults

	label := request.job.Name + " → " + request.remoteName
	source, err := resolveRcloneSource(request.job.Source)
	if err != nil {
		return r.missingRcloneSourceResult(ctx, request, err)
	}
	invocation := prepareRcloneInvocation(source, request.job.Destination, request.remoteName)

	r.logInfo("Source: " + invocation.sourceArgument)
	r.logInfo("Destination: " + invocation.destination)

	args := r.buildRcloneJobArguments(rcloneArgumentsRequest{
		defaults:     defaults,
		job:          request.job,
		invocation:   invocation,
		remoteName:   request.remoteName,
		runTimestamp: request.runTimestamp,
		dryRun:       r.plan.dryRun,
		logPath:      r.logger.LogPath(),
	})
	r.logger.Debug(formatExec("rclone", args))

	// See the rsync branch for why the error is discarded; same reasoning applies.
	maxRuntime, _ := request.job.MaxRuntimeDuration()
	jobCtx, cancel := jobContext(ctx, maxRuntime)
	defer cancel()

	r.progress.StartJob(jobCtx, label)
	result := r.rclone.Exec(jobCtx, args, r.progress.ProgressCallback())
	result.Name = invocation.destinationName
	if invocation.destinationName == "" {
		result.Name = "(prefix root)"
	}
	r.progress.FinishJob(result)

	return JobResult{
		Name:   request.job.Name,
		Remote: request.remoteName,
		Items:  []ItemResult{result},
	}
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

// checkPrerequisites delegates the plan's cloned prerequisite request to the system boundary.
func (r *Runner) checkPrerequisites() error {
	if err := r.prerequisites.Check(r.plan.prerequisiteRequest()); err != nil {
		return err
	}

	r.logger.Success("All prerequisites met.")
	return nil
}
