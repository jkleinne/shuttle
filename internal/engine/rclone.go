package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jkleinne/shuttle/internal/log"
)

// rclonePermanentOpts are the fixed flags passed to every rclone invocation.
// They request symlink copying, fast listing, rename tracking by modtime+size,
// skip-newer semantics, progress output, and 1-second stat intervals.
var rclonePermanentOpts = []string{
	"--copy-links",
	"--fast-list",
	"--track-renames",
	"--track-renames-strategy", "modtime,size",
	"--update",
	"-P",
	"--stats", "1s",
}

// RcloneOpts carries per-run configuration for rclone calls.
// TuningFlags are translated from [cloud.tuning] by the runner before passing here.
// BackupPath and RunTimestamp are used together to construct the --backup-dir path.
type RcloneOpts struct {
	Mode         string   // "copy" or "sync"
	TuningFlags  []string // translated from [cloud.tuning]
	BackupPath   string   // e.g. "_archive"; empty disables --backup-dir
	RunTimestamp string   // e.g. "2026-04-12_081532"
	FilterFile   string   // path to rclone filter file; empty disables --filter-from
}

// RcloneExecutor wraps rclone execution via os/exec.
// dryRun injects --dry-run into every call.
// logFile, when non-empty, injects --log-file and --log-level INFO so rclone
// mirrors its output to a persistent file for stats parsing.
type RcloneExecutor struct {
	logger  *log.Logger
	dryRun  bool
	logFile string
}

// NewRcloneExecutor returns a configured RcloneExecutor. The logger receives
// error and informational messages. dryRun and logFile are applied to every
// subsequent Exec and CleanupArchives call.
func NewRcloneExecutor(logger *log.Logger, dryRun bool, logFile string) *RcloneExecutor {
	return &RcloneExecutor{logger: logger, dryRun: dryRun, logFile: logFile}
}

// Exec runs rclone against a single source/destination pair.
// Subcommand selection (copy vs sync) and --backup-dir construction are
// delegated to selectMode. Stats are parsed from the log file section written
// during this call. Returns an ItemResult with parsed TransferStats and
// StatusOK or StatusFailed based on the rclone exit code.
func (e *RcloneExecutor) Exec(ctx context.Context, source, destination, remoteName string, isDir bool, opts RcloneOpts) ItemResult {
	displayName := filepath.Base(strings.TrimRight(source, "/"))

	// Record the current log line count so we can read only the section
	// appended during this call when parsing stats afterward.
	logStartLine := 0
	if e.logFile != "" {
		logStartLine = countLines(e.logFile)
	}

	start := time.Now()

	subcommand, backupDirArgs := e.selectMode(opts, destination, remoteName, isDir)

	args := make([]string, 0, 30)
	args = append(args, subcommand)
	args = append(args, opts.TuningFlags...)
	args = append(args, rclonePermanentOpts...)
	if opts.FilterFile != "" {
		args = append(args, "--filter-from", opts.FilterFile)
	}
	if e.dryRun {
		args = append(args, "--dry-run")
	}
	if e.logFile != "" {
		args = append(args, "--log-file", e.logFile, "--log-level", "INFO")
	}
	args = append(args, backupDirArgs...)
	args = append(args, source, destination)

	cmd := exec.CommandContext(ctx, "rclone", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	elapsed := time.Since(start)

	var stats TransferStats
	if e.logFile != "" {
		logSection := readLinesAfter(e.logFile, logStartLine)
		stats = ParseRcloneStats(logSection)
	}
	stats.Elapsed = elapsed

	status := StatusOK
	if err != nil {
		status = StatusFailed
		e.logger.Error(fmt.Sprintf("rclone %s failed for %s: %v", subcommand, displayName, err))
	}

	return ItemResult{Name: displayName, Status: status, Stats: stats}
}

// selectMode returns the rclone subcommand and any --backup-dir arguments.
// Copy mode is used when opts.Mode is "copy" or the source is a file (rclone
// sync requires a directory target). When sync mode is active and a backup
// path is configured, --backup-dir is constructed as:
//
//	remote:<backup_path>/<run_timestamp>/<dest_subpath>/
func (e *RcloneExecutor) selectMode(opts RcloneOpts, destination, remoteName string, isDir bool) (subcommand string, backupDirArgs []string) {
	if opts.Mode == "copy" || !isDir {
		if opts.Mode == "sync" && !isDir {
			e.logger.Info("mode is 'sync' but source is a file; using 'rclone copy'")
		}
		return "copy", nil
	}

	// sync mode with a directory source
	if opts.BackupPath != "" {
		destSubpath := strings.TrimPrefix(destination, remoteName+":")
		destSubpath = strings.TrimRight(destSubpath, "/")
		backupDir := fmt.Sprintf("%s:%s/%s/%s/",
			remoteName,
			strings.TrimRight(opts.BackupPath, "/"),
			opts.RunTimestamp,
			destSubpath,
		)
		return "sync", []string{"--backup-dir", backupDir}
	}

	return "sync", nil
}

// CleanupArchives purges archive subdirectories older than retentionDays
// from the backup root on the given remote. It is non-fatal: individual purge
// failures are logged as warnings and do not stop processing of remaining
// directories. Skipped entirely during dry-run or when backupPath is empty
// or retentionDays is non-positive.
func (e *RcloneExecutor) CleanupArchives(ctx context.Context, remoteName, backupPath string, retentionDays int) error {
	if backupPath == "" || retentionDays <= 0 || e.dryRun {
		return nil
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format("2006-01-02")
	archiveRoot := fmt.Sprintf("%s:%s", remoteName, strings.TrimRight(backupPath, "/"))

	lsdArgs := []string{"lsd", archiveRoot + "/"}
	if e.logFile != "" {
		lsdArgs = append(lsdArgs, "--log-file", e.logFile, "--log-level", "INFO")
	}
	output, err := exec.CommandContext(ctx, "rclone", lsdArgs...).Output()
	if err != nil {
		e.logger.Info(fmt.Sprintf("no archive directory on %s (nothing to clean)", remoteName))
		return nil
	}

	purged := 0
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		dirName := fields[len(fields)-1]
		// Archive directory names are prefixed with a YYYY-MM-DD date stamp.
		// Skip entries that are too short to contain a full date prefix.
		if len(dirName) < 10 {
			continue
		}
		dirDate := dirName[:10]
		// Validate that the prefix looks like YYYY-MM-DD before comparing.
		if dirDate[4] != '-' || dirDate[7] != '-' {
			continue
		}
		if dirDate < cutoff {
			target := archiveRoot + "/" + dirName
			e.logger.Info(fmt.Sprintf("purging expired archive: %s (%s < %s)", target, dirDate, cutoff))
			purgeArgs := []string{"purge", target}
			if e.logFile != "" {
				purgeArgs = append(purgeArgs, "--log-file", e.logFile, "--log-level", "INFO")
			}
			if purgeErr := exec.CommandContext(ctx, "rclone", purgeArgs...).Run(); purgeErr != nil {
				e.logger.Warn(fmt.Sprintf("failed to purge %s: %v", target, purgeErr))
			} else {
				purged++
			}
		}
	}

	if purged > 0 {
		e.logger.Info(fmt.Sprintf("archive cleanup: purged %d expired director(ies) from %s", purged, remoteName))
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
	defer f.Close()
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
	defer f.Close()

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
