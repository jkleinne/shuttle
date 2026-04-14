package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jkleinne/shuttle/internal/log"
)

// RcloneExecutor wraps rclone execution via os/exec. It receives pre-assembled
// argument lists from the runner (built by BuildRcloneArgs) and handles command
// execution and stats parsing from the shared log file.
type RcloneExecutor struct {
	logger  *log.Logger
	logFile string
}

// NewRcloneExecutor returns a configured RcloneExecutor.
// logFile is the path to the shared log file used for stats parsing.
func NewRcloneExecutor(logger *log.Logger, logFile string) *RcloneExecutor {
	return &RcloneExecutor{logger: logger, logFile: logFile}
}

// rcloneProgressTracker accumulates rclone -P stdout output and surfaces the
// most informative progress line. When files are actively transferring, the
// bytes line (with speed and ETA) takes precedence. When rclone is only
// checking files (Transferred shows "0 B / 0 B"), the Checks line is shown.
type rcloneProgressTracker struct {
	lastBytesLine  string
	lastChecksLine string
}

// feedLine processes one line of rclone -P output and returns the current best
// progress text. Returns empty string when no informative line has been seen.
func (t *rcloneProgressTracker) feedLine(line string) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "Transferred:") && strings.Contains(trimmed, "/s"):
		colonIdx := strings.IndexByte(trimmed, ':')
		value := strings.TrimSpace(trimmed[colonIdx+1:])
		// Only treat as active transfer when bytes are non-zero.
		// Rclone always emits "0 B / 0 B, -, 0 B/s, ETA -" even during
		// check-only runs; showing that is noise.
		if !strings.HasPrefix(value, "0 B / 0 B") {
			t.lastBytesLine = value
		}
	case strings.HasPrefix(trimmed, "Checks:"):
		colonIdx := strings.IndexByte(trimmed, ':')
		t.lastChecksLine = strings.TrimSpace(trimmed[colonIdx+1:])
	}

	if t.lastBytesLine != "" {
		return t.lastBytesLine
	}
	return t.lastChecksLine
}

// scanRcloneProgress reads rclone -P progress output line by line, extracts
// progress updates, and forwards each to onProgress. If onProgress is nil,
// the reader is drained without parsing.
func scanRcloneProgress(r io.Reader, onProgress func(string)) {
	if onProgress == nil {
		io.Copy(io.Discard, r) //nolint:errcheck // discard intentionally; caller doesn't need the error
		return
	}

	var tracker rcloneProgressTracker
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if progress := tracker.feedLine(scanner.Text()); progress != "" {
			onProgress(progress)
		}
	}
}

// Exec runs rclone with the given pre-assembled argument list.
// Stdout is piped to a goroutine that parses -P progress output (rclone writes
// progress to stdout, not stderr). Stats are parsed from the log file section
// written during this call.
func (e *RcloneExecutor) Exec(ctx context.Context, args []string, onProgress func(string)) ItemResult {
	// Display name from second-to-last arg (source).
	source := ""
	if len(args) >= 2 {
		source = args[len(args)-2]
	}
	displayName := filepath.Base(strings.TrimRight(source, "/"))

	logStartLine := 0
	if e.logFile != "" {
		logStartLine = countLines(e.logFile)
	}

	start := time.Now()

	cmd := exec.CommandContext(ctx, "rclone", args...)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.logger.FileError(fmt.Sprintf("rclone pipe setup failed for %s: %v", displayName, err))
		return ItemResult{Name: displayName, Status: StatusFailed}
	}

	if err := cmd.Start(); err != nil {
		e.logger.FileError(fmt.Sprintf("rclone start failed for %s: %v", displayName, err))
		return ItemResult{Name: displayName, Status: StatusFailed}
	}

	var pipeWg sync.WaitGroup
	pipeWg.Add(1)
	go func() {
		defer pipeWg.Done()
		scanRcloneProgress(stdout, onProgress)
	}()

	pipeWg.Wait()
	runErr := cmd.Wait()
	elapsed := time.Since(start)

	if stderrBuf.Len() > 0 {
		for _, line := range strings.Split(strings.TrimSpace(stderrBuf.String()), "\n") {
			if line != "" {
				e.logger.FileError(line)
			}
		}
	}

	var stats TransferStats
	if e.logFile != "" {
		logSection := readLinesAfter(e.logFile, logStartLine)
		stats = ParseRcloneStats(logSection)
	}
	stats.Elapsed = elapsed

	status := StatusOK
	if runErr != nil {
		status = StatusFailed
		subcommand := "rclone"
		if len(args) > 0 {
			subcommand = "rclone " + args[0]
		}
		e.logger.FileError(fmt.Sprintf("%s failed for %s: %v", subcommand, displayName, runErr))
	}

	return ItemResult{Name: displayName, Status: status, Stats: stats}
}

// selectMode returns the rclone subcommand and any --backup-dir argument value.
// Copy mode is used when mode is "copy" or the source is a file (rclone sync
// requires a directory target). When sync mode is active and a backup path is
// configured, the backup-dir is constructed as:
//
//	remote:<backup_path>/<run_timestamp>/<dest_subpath>/
func selectMode(mode, destination, remoteName, backupPath, runTimestamp string, isDir bool, logger *log.Logger) (subcommand, backupDirArg string) {
	if mode == "copy" || !isDir {
		if mode == "sync" && !isDir {
			logger.Info("mode is 'sync' but source is a file; using 'rclone copy'")
		}
		return "copy", ""
	}

	if backupPath != "" {
		destSubpath := strings.TrimPrefix(destination, remoteName+":")
		destSubpath = strings.TrimRight(destSubpath, "/")
		backupDir := fmt.Sprintf("%s:%s/%s/%s/",
			remoteName,
			strings.TrimRight(backupPath, "/"),
			runTimestamp,
			destSubpath,
		)
		return "sync", backupDir
	}

	return "sync", ""
}

// CleanupArchives purges archive subdirectories older than retentionDays
// from the backup root on the given remote. It is non-fatal: individual purge
// failures are logged as warnings and do not stop processing of remaining
// directories. Skipped during dry-run, when backupPath is empty, or when
// retentionDays is non-positive.
func (e *RcloneExecutor) CleanupArchives(ctx context.Context, remoteName, backupPath string, retentionDays int, dryRun bool) error {
	if backupPath == "" || retentionDays <= 0 || dryRun {
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
