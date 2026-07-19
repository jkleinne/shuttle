package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

const (
	rcloneCommandName               = "rclone"
	rcloneConfigPasswordEnvironment = "RCLONE_CONFIG_PASS"
)

// RcloneExecutor wraps rclone execution via os/exec. It receives pre-assembled
// argument lists from the runner (built by BuildRcloneArgs) and handles command
// execution and command-local bounded stats capture.
type RcloneExecutor struct {
	logger *log.Logger
	// configPassword is injected per command, with an empty value preserving native inheritance.
	configPassword string
	now            func() time.Time
}

// ArchiveCleanupRequest carries the values for one remote archive cleanup operation.
type ArchiveCleanupRequest struct {
	// RemoteName keeps retention mutations scoped to the same configured remote as the job.
	RemoteName string
	// BackupPath keeps cleanup below the job's configured archive root.
	BackupPath string
	// RetentionDays makes purge eligibility explicit at the process boundary.
	RetentionDays int
	// DryRun prevents retention checks from mutating remote state during previews.
	DryRun bool
}

// InformationLogger lets mode selection report fallback behavior without owning logger storage.
type InformationLogger interface {
	// Info surfaces mode fallbacks without coupling selection logic to a concrete logger.
	Info(string)
}

// NewRcloneExecutor returns a configured RcloneExecutor. configPassword, when
// non-empty, is injected as RCLONE_CONFIG_PASS into each rclone command's
// environment and nowhere else.
func NewRcloneExecutor(logger *log.Logger, configPassword string) *RcloneExecutor {
	return &RcloneExecutor{
		logger:         logger,
		configPassword: configPassword,
		now:            time.Now,
	}
}

// rcloneProgressTracker extracts transfer progress from rclone -P stdout.
// Only surfaces the Transferred bytes line (with speed and ETA) when files
// are actively moving. During check-only phases (no transfer, or "0 B / 0 B"),
// returns empty so the spinner falls back to showing elapsed time.
type rcloneProgressTracker struct {
	lastBytesLine string
}

// feedLine processes one line of rclone -P output. Returns non-empty only
// when rclone is actively transferring bytes (not just checking files).
//
// When piped, rclone's per-file progress lines are not newline-terminated,
// so the "Transferred:" bytes line can appear mid-segment after a per-file
// line (e.g. "* file.bin: 40% /1Mi, 100Ki/s, 5sTransferred: 512 KiB / ...").
// LastIndex finds the stats marker regardless of where it sits in the segment.
func (t *rcloneProgressTracker) feedLine(line string) string {
	trimmed := strings.TrimSpace(line)
	idx := strings.LastIndex(trimmed, "Transferred:")
	if idx < 0 {
		return t.lastBytesLine
	}
	rest := trimmed[idx:]
	if !strings.Contains(rest, "/s") {
		return t.lastBytesLine
	}
	colonIdx := strings.IndexByte(rest, ':')
	value := strings.TrimSpace(rest[colonIdx+1:])
	if !strings.HasPrefix(value, "0 B / 0 B") {
		t.lastBytesLine = value
	}
	return t.lastBytesLine
}

// scanRcloneProgress drains bounded records for focused progress tests. The
// executor uses the same capture primitive and adds statistics and log policy.
func scanRcloneProgress(r io.Reader, onProgress func(string)) error {
	var tracker rcloneProgressTracker
	return captureDelimitedRecords(r, func(record string) {
		if onProgress == nil {
			return
		}
		if progress := tracker.feedLine(record); progress != "" {
			onProgress(progress)
		}
	})
}

// rcloneCommand builds an rclone *exec.Cmd, injecting the config password into
// this command's environment only when one was supplied. When configPassword is
// empty the command inherits the ambient environment unchanged (covering both
// the no-password case and a user-exported RCLONE_CONFIG_PASS). Callers choose
// their own execution method on the returned command.
func (e *RcloneExecutor) rcloneCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, rcloneCommandName, args...)
	if e.configPassword != "" {
		cmd.Env = append(
			os.Environ(),
			rcloneConfigPasswordEnvironment+"="+e.configPassword,
		)
	}
	return cmd
}

type rcloneExecution struct {
	command     *exec.Cmd
	stdout      io.ReadCloser
	stderr      io.ReadCloser
	displayName string
	subcommand  string
	startedAt   time.Time
}

func (e *RcloneExecutor) prepareExecution(ctx context.Context, args []string) (rcloneExecution, *ItemResult) {
	source := ""
	if len(args) >= 2 {
		source = args[len(args)-2]
	}
	displayName := filepath.Base(strings.TrimRight(source, "/"))
	subcommand := rcloneCommandName
	if len(args) > 0 {
		subcommand += " " + args[0]
	}

	startedAt := e.now()
	command := e.rcloneCommand(ctx, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		e.logger.FileError(fmt.Sprintf("setting up rclone stdout pipe for %s: %v", displayName, err))
		return rcloneExecution{}, &ItemResult{Name: displayName, Status: StatusFailed}
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		e.logger.FileError(fmt.Sprintf("setting up rclone stderr pipe for %s: %v", displayName, err))
		return rcloneExecution{}, &ItemResult{Name: displayName, Status: StatusFailed}
	}

	execution := rcloneExecution{
		command:     command,
		stdout:      stdout,
		stderr:      stderr,
		displayName: displayName,
		subcommand:  subcommand,
		startedAt:   startedAt,
	}
	if err := command.Start(); err != nil {
		status := classifyExitStatus(ctx, err)
		if status == StatusTimedOut {
			e.logger.FileError(fmt.Sprintf(
				"rclone timed out for %s after per-job max_runtime: %v",
				displayName,
				err,
			))
		} else {
			e.logger.FileError(fmt.Sprintf("rclone start failed for %s: %v", displayName, err))
		}
		return rcloneExecution{}, &ItemResult{Name: displayName, Status: status}
	}
	return execution, nil
}

func rcloneStatisticsMarkerAtRecordStart(record string) string {
	markers := []string{"Transferred:", "Checks:", "Deleted:"}
	for _, marker := range markers {
		if strings.HasPrefix(record, marker) {
			return marker
		}
	}
	return ""
}

func (e *RcloneExecutor) captureStdoutRecord(
	record string,
	statsTail *tailBuffer,
	tracker *rcloneProgressTracker,
	onProgress func(string),
) {
	trimmed := strings.TrimLeftFunc(record, unicode.IsSpace)
	if trimmed == "" {
		return
	}
	if trimmed[0] == '{' {
		e.logger.FileTool("rclone", trimmed)
		return
	}
	if marker := rcloneStatisticsMarkerAtRecordStart(trimmed); marker != "" {
		normalized := strings.TrimSpace(trimmed)
		_, _ = io.WriteString(statsTail, normalized+"\n")
		if marker == "Transferred:" && strings.Contains(normalized, "/s") && onProgress != nil {
			if progress := tracker.feedLine(normalized); progress != "" {
				onProgress(progress)
			}
		}
		return
	}

	position := strings.LastIndex(trimmed, "Transferred:")
	if position <= 0 {
		e.logger.FileTool("rclone", trimmed)
		return
	}
	progressRecord := trimmed[position:]
	if !strings.Contains(progressRecord, "/s") {
		e.logger.FileTool("rclone", trimmed)
		return
	}
	if onProgress == nil {
		return
	}
	progress := tracker.feedLine(progressRecord)
	if progress == "" {
		return
	}
	onProgress(progress)
}

func (e *RcloneExecutor) logExecutionFailure(
	ctx context.Context,
	execution rcloneExecution,
	runError error,
	firstStderr string,
) Status {
	status := classifyExitStatus(ctx, runError)
	if runError == nil {
		return status
	}
	detail := ""
	if firstStderr != "" {
		detail = " (" + firstStderr + ")"
	}
	if status == StatusTimedOut {
		e.logger.FileError(fmt.Sprintf(
			"%s timed out for %s after per-job max_runtime: %v%s",
			execution.subcommand,
			execution.displayName,
			runError,
			detail,
		))
	} else {
		e.logger.FileError(fmt.Sprintf(
			"%s failed for %s: %v%s",
			execution.subcommand,
			execution.displayName,
			runError,
			detail,
		))
	}
	return status
}

// Exec keeps rclone process I/O behind the executor by draining both streams,
// framing diagnostics, forwarding progress, and parsing only bounded records
// captured during this command.
func (e *RcloneExecutor) Exec(ctx context.Context, args []string, onProgress func(string)) ItemResult {
	execution, failure := e.prepareExecution(ctx, args)
	if failure != nil {
		return *failure
	}

	statsTail := newTailBuffer(statisticsTailBytes)
	tracker := &rcloneProgressTracker{}
	var firstStderr string
	var stdoutReadErr, stderrReadErr error
	var drains sync.WaitGroup
	drains.Add(2)
	go func() {
		defer drains.Done()
		stdoutReadErr = captureDelimitedRecords(execution.stdout, func(record string) {
			e.captureStdoutRecord(record, statsTail, tracker, onProgress)
		})
	}()
	go func() {
		defer drains.Done()
		stderrReadErr = captureDelimitedRecords(execution.stderr, func(record string) {
			if firstStderr == "" {
				firstStderr = record
			}
			e.logger.FileTool("rclone", record)
		})
	}()
	drains.Wait()
	runError := execution.command.Wait()
	elapsed := e.now().Sub(execution.startedAt)

	if stdoutReadErr != nil {
		e.logger.FileError(fmt.Sprintf(
			"reading rclone stdout for %s: %v",
			execution.displayName,
			stdoutReadErr,
		))
	}
	if stderrReadErr != nil {
		e.logger.FileError(fmt.Sprintf(
			"reading rclone stderr for %s: %v",
			execution.displayName,
			stderrReadErr,
		))
	}

	stats := ParseRcloneStats(statsTail.Bytes())
	stats.Elapsed = elapsed
	status := e.logExecutionFailure(ctx, execution, runError, firstStderr)
	return ItemResult{Name: execution.displayName, Status: status, Stats: stats}
}

// modeRequest carries the inputs selectMode needs to pick the rclone
// subcommand and construct the --backup-dir value.
type modeRequest struct {
	Mode         string
	Destination  string
	RemoteName   string
	BackupPath   string
	RunTimestamp string
	IsDir        bool
}

// selectMode returns the rclone subcommand and any --backup-dir argument value.
// Copy mode is used when mode is "copy" or the source is a file (rclone sync
// requires a directory target). When sync mode is active and a backup path is
// configured, the backup-dir is constructed as:
//
//	remote:<backup_path>/<run_timestamp>/<dest_subpath>/
func selectMode(req modeRequest, logger InformationLogger) (subcommand, backupDirArg string) {
	if req.Mode == config.ModeCopy || !req.IsDir {
		if req.Mode == config.ModeSync && !req.IsDir {
			logger.Info("mode is 'sync' but source is a file; using 'rclone copy'")
		}
		// config.ModeCopy/ModeSync double as the rclone subcommand spellings,
		// so the mode constant is returned directly as the subcommand.
		return config.ModeCopy, ""
	}

	if req.BackupPath != "" {
		destSubpath := strings.TrimPrefix(req.Destination, req.RemoteName+":")
		destSubpath = strings.TrimRight(destSubpath, "/")
		backupDir := fmt.Sprintf("%s:%s/%s/%s/",
			req.RemoteName,
			strings.TrimRight(req.BackupPath, "/"),
			req.RunTimestamp,
			destSubpath,
		)
		return config.ModeSync, backupDir
	}

	return config.ModeSync, ""
}

// archiveDateLayout is the date prefix on archive directory names; the run
// timestamp used for --backup-dir construction starts with this format.
const archiveDateLayout = "2006-01-02"

// rcloneExitDirNotFound is rclone's documented exit code for "directory not
// found", the one lsd failure that means "no archives yet" rather than a
// real problem. https://rclone.org/docs/#exit-code
const rcloneExitDirNotFound = 3

// archiveDirExpired classifies an archive directory name against a cutoff
// date (archiveDateLayout format). recognized is false when the name does
// not begin with a calendar-valid date, in which case expired is
// meaningless. Pure so purge eligibility is testable without rclone; the
// caller filters empty names before calling.
func archiveDirExpired(dirName, cutoff string) (expired, recognized bool) {
	if len(dirName) < len(archiveDateLayout) {
		return false, false
	}
	datePrefix := dirName[:len(archiveDateLayout)]
	if _, err := time.Parse(archiveDateLayout, datePrefix); err != nil {
		return false, false
	}
	return datePrefix < cutoff, true
}

// isDirNotFound reports whether an rclone invocation failed only because the
// target directory does not exist.
func isDirNotFound(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == rcloneExitDirNotFound
}

func (e *RcloneExecutor) captureToolRecords(
	reader io.Reader,
	firstRecord *string,
) error {
	return captureDelimitedRecords(reader, func(record string) {
		if firstRecord != nil && *firstRecord == "" {
			*firstRecord = record
		}
		e.logger.FileTool("rclone", record)
	})
}

// purgeArchiveDir runs rclone purge for one expired archive directory. The
// caller decides how to handle a process failure.
func (e *RcloneExecutor) purgeArchiveDir(ctx context.Context, target string) error {
	purgeArgs := []string{"purge", target}
	command := e.rcloneCommand(ctx, purgeArgs...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("setting up purge stdout pipe for %s: %w", target, err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return fmt.Errorf("setting up purge stderr pipe for %s: %w", target, err)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("starting purge for %s: %w", target, err)
	}

	var stdoutReadErr, stderrReadErr error
	var firstStderr string
	var drains sync.WaitGroup
	drains.Add(2)
	go func() {
		defer drains.Done()
		stdoutReadErr = e.captureToolRecords(stdout, nil)
	}()
	go func() {
		defer drains.Done()
		stderrReadErr = e.captureToolRecords(stderr, &firstStderr)
	}()
	drains.Wait()
	processErr := command.Wait()

	if stdoutReadErr != nil {
		e.logger.FileError(fmt.Sprintf("reading purge stdout for %s: %v", target, stdoutReadErr))
	}
	if stderrReadErr != nil {
		e.logger.FileError(fmt.Sprintf("reading purge stderr for %s: %v", target, stderrReadErr))
	}
	if processErr == nil {
		return nil
	}
	if firstStderr != "" {
		return fmt.Errorf("purging archive directory %s: %w (%s)", target, processErr, firstStderr)
	}
	return fmt.Errorf("purging archive directory %s: %w", target, processErr)
}

// CleanupArchives purges archive subdirectories older than the requested
// retention from the selected remote. Individual purge failures are
// logged as warnings and do not stop processing of remaining directories,
// but a failure to list the backup root at all is returned as an error so
// the caller can surface it; a missing backup root (rclone exit 3) is the
// expected first-run state and stays a non-error. Skipped during dry-run,
// when the backup path is empty, or when retention is non-positive.
func (e *RcloneExecutor) CleanupArchives(ctx context.Context, request ArchiveCleanupRequest) error {
	plan, shouldRun := prepareArchiveCleanup(request)
	if !shouldRun {
		return nil
	}
	listing, err := e.readArchiveDirectoryListing(ctx, plan)
	if err != nil {
		return err
	}
	return e.purgeExpiredArchiveDirectories(ctx, archivePurgeRequest{
		plan:    plan,
		listing: bytes.NewReader(listing),
		purge:   e.purgeArchiveDir,
	})
}

type archiveCleanupPlan struct {
	remoteName  string
	archiveRoot string
	cutoff      string
}

func prepareArchiveCleanup(request ArchiveCleanupRequest) (archiveCleanupPlan, bool) {
	if request.BackupPath == "" || request.RetentionDays <= 0 || request.DryRun {
		return archiveCleanupPlan{}, false
	}
	return archiveCleanupPlan{
		remoteName:  request.RemoteName,
		archiveRoot: fmt.Sprintf("%s:%s", request.RemoteName, strings.TrimRight(request.BackupPath, "/")),
		cutoff:      time.Now().AddDate(0, 0, -request.RetentionDays).Format(archiveDateLayout),
	}, true
}

func (e *RcloneExecutor) drainArchiveListing(
	stdout io.Reader,
	stderr io.Reader,
) (listing []byte, firstStderr string, stdoutReadErr, stderrReadErr error) {
	var output bytes.Buffer
	var drains sync.WaitGroup
	drains.Add(2)
	go func() {
		defer drains.Done()
		_, stdoutReadErr = io.Copy(&output, stdout)
	}()
	go func() {
		defer drains.Done()
		stderrReadErr = e.captureToolRecords(stderr, &firstStderr)
	}()
	drains.Wait()
	return output.Bytes(), firstStderr, stdoutReadErr, stderrReadErr
}

func (e *RcloneExecutor) readArchiveDirectoryListing(
	ctx context.Context,
	plan archiveCleanupPlan,
) ([]byte, error) {
	lsdArgs := []string{"lsd", plan.archiveRoot + "/"}
	command := e.rcloneCommand(ctx, lsdArgs...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("setting up archive listing stdout pipe for %s: %w", plan.archiveRoot, err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("setting up archive listing stderr pipe for %s: %w", plan.archiveRoot, err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("starting archive listing for %s: %w", plan.archiveRoot, err)
	}

	listing, firstStderr, stdoutReadErr, stderrReadErr := e.drainArchiveListing(stdout, stderr)
	processErr := command.Wait()

	if stdoutReadErr != nil {
		e.logger.FileError(fmt.Sprintf(
			"reading archive listing stdout for %s: %v",
			plan.archiveRoot,
			stdoutReadErr,
		))
	}
	if stderrReadErr != nil {
		e.logger.FileError(fmt.Sprintf(
			"reading archive listing stderr for %s: %v",
			plan.archiveRoot,
			stderrReadErr,
		))
	}
	if processErr != nil {
		if isDirNotFound(processErr) {
			e.logger.Info(fmt.Sprintf("no archive directory on %s (nothing to clean)", plan.remoteName))
			return nil, nil
		}
		if firstStderr != "" {
			return nil, fmt.Errorf(
				"listing archive root %s: %w (%s)",
				plan.archiveRoot,
				processErr,
				firstStderr,
			)
		}
		return nil, fmt.Errorf("listing archive root %s: %w", plan.archiveRoot, processErr)
	}
	return listing, nil
}

type archivePurgeRequest struct {
	plan    archiveCleanupPlan
	listing io.Reader
	purge   func(context.Context, string) error
}

func (e *RcloneExecutor) purgeExpiredArchiveDirectories(
	ctx context.Context,
	request archivePurgeRequest,
) error {
	purged := 0
	scanner := bufio.NewScanner(request.listing)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		directory := fields[len(fields)-1]
		expired, recognized := archiveDirExpired(directory, request.plan.cutoff)
		if !recognized {
			e.logger.Warn(fmt.Sprintf(
				"archive cleanup: skipping unrecognized directory %s on %s",
				directory,
				request.plan.remoteName,
			))
			continue
		}
		if !expired {
			continue
		}
		target := request.plan.archiveRoot + "/" + directory
		e.logger.Info(fmt.Sprintf(
			"purging expired archive: %s (%s < %s)",
			target,
			directory[:len(archiveDateLayout)],
			request.plan.cutoff,
		))
		if purgeErr := request.purge(ctx, target); purgeErr != nil {
			e.logger.Warn(fmt.Sprintf("failed to purge %s: %v", target, purgeErr))
		} else {
			purged++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning archive listing for %s: %w", request.plan.archiveRoot, err)
	}
	if purged > 0 {
		e.logger.Info(fmt.Sprintf(
			"archive cleanup: purged %d expired director(ies) from %s",
			purged,
			request.plan.remoteName,
		))
	}
	return nil
}
