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
	"time"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

const (
	rcloneCommandName               = "rclone"
	rcloneConfigPasswordEnvironment = "RCLONE_CONFIG_PASS"
)

// RcloneExecutor wraps rclone execution via os/exec. It receives pre-assembled
// argument lists from the runner (built by BuildRcloneArgs) and handles command
// execution and stats parsing from the shared log file.
type RcloneExecutor struct {
	logger  *log.Logger
	logFile string
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

// NewRcloneExecutor returns a configured RcloneExecutor. logFile is the shared
// log file used for stats parsing; configPassword, when non-empty, is injected as
// RCLONE_CONFIG_PASS into each rclone command's environment (and nowhere else).
func NewRcloneExecutor(logger *log.Logger, logFile, configPassword string) *RcloneExecutor {
	return &RcloneExecutor{
		logger:         logger,
		logFile:        logFile,
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

// scanRcloneProgress reads rclone -P progress output, extracts progress
// updates, and forwards each to onProgress. If onProgress is nil, the reader
// is drained without parsing. Handles both \n and \r as line delimiters
// because rclone uses \r for in-place updates during transfers. Returns the
// first non-EOF read error encountered, or nil on clean EOF.
func scanRcloneProgress(r io.Reader, onProgress func(string)) error {
	if onProgress == nil {
		_, err := io.Copy(io.Discard, r)
		return err
	}

	var tracker rcloneProgressTracker
	scanner := bufio.NewScanner(r)
	scanner.Split(splitOnCROrLF)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if progress := tracker.feedLine(string(line)); progress != "" {
			onProgress(progress)
		}
	}
	return scanner.Err()
}

// splitOnCROrLF is a bufio.SplitFunc that returns tokens delimited by either
// \r or \n. Used for rclone -P output, which mixes \n-terminated log lines
// with \r-terminated in-place progress updates.
//
//nolint:revive // bufio.SplitFunc requires the atEOF boolean parameter.
func splitOnCROrLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\r' || b == '\n' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
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

// Exec keeps rclone process I/O behind the executor by running a pre-assembled
// argument list, forwarding progress, and parsing only this call's log section.
func (e *RcloneExecutor) Exec(ctx context.Context, args []string, onProgress func(string)) ItemResult {
	execution, failure := e.prepareExecution(ctx, args)
	if failure != nil {
		return *failure
	}
	progressDone := e.scanExecutionProgress(execution, onProgress)
	return e.waitForExecution(ctx, execution, progressDone)
}

type rcloneExecution struct {
	command      *exec.Cmd
	stdout       io.ReadCloser
	stderr       *bytes.Buffer
	displayName  string
	subcommand   string
	logStartLine int
	startedAt    time.Time
}

type rcloneExecutionTiming struct {
	logStartLine int
	startedAt    time.Time
}

func (e *RcloneExecutor) beginExecutionTiming() rcloneExecutionTiming {
	logStartLine := 0
	if e.logFile != "" {
		logStartLine = countLines(e.logFile)
	}
	return rcloneExecutionTiming{
		logStartLine: logStartLine,
		startedAt:    e.now(),
	}
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

	timing := e.beginExecutionTiming()
	command := e.rcloneCommand(ctx, args...)
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		e.logger.FileError(fmt.Sprintf("rclone pipe setup failed for %s: %v", displayName, err))
		return rcloneExecution{}, &ItemResult{Name: displayName, Status: StatusFailed}
	}

	execution := rcloneExecution{
		command:      command,
		stdout:       stdout,
		stderr:       stderr,
		displayName:  displayName,
		subcommand:   subcommand,
		logStartLine: timing.logStartLine,
		startedAt:    timing.startedAt,
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

func (e *RcloneExecutor) scanExecutionProgress(
	execution rcloneExecution,
	onProgress func(string),
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := scanRcloneProgress(execution.stdout, onProgress); err != nil {
			e.logger.FileError(fmt.Sprintf(
				"reading rclone progress for %s: %v",
				execution.displayName,
				err,
			))
		}
	}()
	return done
}

func (e *RcloneExecutor) waitForExecution(
	ctx context.Context,
	execution rcloneExecution,
	progressDone <-chan struct{},
) ItemResult {
	<-progressDone
	runError := execution.command.Wait()
	elapsed := e.now().Sub(execution.startedAt)

	if execution.stderr.Len() > 0 {
		for _, line := range strings.Split(strings.TrimSpace(execution.stderr.String()), "\n") {
			if line != "" {
				e.logger.FileError(line)
			}
		}
	}

	var stats TransferStats
	if e.logFile != "" {
		logSection := readLinesAfter(e.logFile, execution.logStartLine)
		stats = ParseRcloneStats(logSection)
	}
	stats.Elapsed = elapsed

	status := classifyExitStatus(ctx, runError)
	if runError != nil {
		if status == StatusTimedOut {
			e.logger.FileError(fmt.Sprintf(
				"%s timed out for %s after per-job max_runtime: %v",
				execution.subcommand,
				execution.displayName,
				runError,
			))
		} else {
			e.logger.FileError(fmt.Sprintf(
				"%s failed for %s: %v",
				execution.subcommand,
				execution.displayName,
				runError,
			))
		}
	}

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

// firstStderrLine returns the first stderr line captured by Output(), or ""
// when stderr was empty (rclone routes error text to --log-file when set,
// leaving stderr blank in normal runs).
func firstStderrLine(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || len(exitErr.Stderr) == 0 {
		return ""
	}
	return oneLine(string(exitErr.Stderr))
}

// purgeArchiveDir runs rclone purge for one expired archive directory,
// mirroring the lsd call's log-file routing. The caller decides how to
// handle a failure (warn and continue).
func (e *RcloneExecutor) purgeArchiveDir(ctx context.Context, target string) error {
	purgeArgs := []string{"purge", target}
	if e.logFile != "" {
		purgeArgs = append(purgeArgs, "--log-file", e.logFile, "--log-level", "INFO")
	}
	return e.rcloneCommand(ctx, purgeArgs...).Run()
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

func (e *RcloneExecutor) readArchiveDirectoryListing(
	ctx context.Context,
	plan archiveCleanupPlan,
) ([]byte, error) {
	lsdArgs := []string{"lsd", plan.archiveRoot + "/"}
	if e.logFile != "" {
		lsdArgs = append(lsdArgs, "--log-file", e.logFile, "--log-level", "INFO")
	}
	output, err := e.rcloneCommand(ctx, lsdArgs...).Output()
	if err != nil {
		if isDirNotFound(err) {
			e.logger.Info(fmt.Sprintf("no archive directory on %s (nothing to clean)", plan.remoteName))
			return nil, nil
		}
		if detail := firstStderrLine(err); detail != "" {
			return nil, fmt.Errorf("listing archive root %s: %w (%s)", plan.archiveRoot, err, detail)
		}
		return nil, fmt.Errorf("listing archive root %s: %w", plan.archiveRoot, err)
	}
	return output, nil
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

// countLines counts the number of newline-terminated lines in the file at path.
// Returns 0 if the file cannot be opened, so callers can safely treat a missing
// log file as having zero pre-existing lines.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	return count
}

// readLinesAfter reads all lines from the file at path that come after
// startLine (1-based). Used to extract the log section written during a single
// rclone call for stats parsing without re-reading lines from prior calls.
// Returns nil if the file cannot be opened.
func readLinesAfter(path string, startLine int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var result []byte
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum > startLine {
			result = append(result, scanner.Bytes()...)
			result = append(result, '\n')
		}
	}
	return result
}
