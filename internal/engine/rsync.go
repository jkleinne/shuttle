package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jkleinne/sync-station/internal/log"
)

// rsyncBaseOpts are the fixed flags passed to every rsync invocation.
// They request archive mode, verbose human-readable output, partial-file
// resume, macOS extended attributes, skip-newer, stats, and DS_Store exclusion.
var rsyncBaseOpts = []string{
	"-a", "-v", "-h", "-P", "-E", "-u",
	"--stats", "--info=progress2", "--exclude=.DS_Store",
}

// RsyncOpts carries per-job configuration for a single rsync call.
// Delete controls --delete-after; ExtraOpts are appended verbatim after the
// base flags and before the source/destination arguments.
type RsyncOpts struct {
	Delete    bool
	ExtraOpts []string
}

// RsyncExecutor wraps rsync execution via os/exec.
// dryRun injects --dry-run into every call.
// logFile, when non-empty, injects --log-file=<path> so rsync mirrors its
// output to a persistent file.
type RsyncExecutor struct {
	logger  *log.Logger
	dryRun  bool
	logFile string
}

// NewRsyncExecutor returns a configured RsyncExecutor. The logger receives
// error messages on exec failure. dryRun and logFile are applied to every
// subsequent Exec call.
func NewRsyncExecutor(logger *log.Logger, dryRun bool, logFile string) *RsyncExecutor {
	return &RsyncExecutor{logger: logger, dryRun: dryRun, logFile: logFile}
}

// Exec runs rsync with the given source, destination, and per-job options.
// Stdout is written to the terminal and simultaneously captured for stats
// parsing. Returns an ItemResult with parsed TransferStats and StatusOK or
// StatusFailed based on the rsync exit code.
func (e *RsyncExecutor) Exec(ctx context.Context, source, destination string, opts RsyncOpts) ItemResult {
	args := make([]string, 0, len(rsyncBaseOpts)+10)
	args = append(args, rsyncBaseOpts...)

	if e.dryRun {
		args = append(args, "--dry-run")
	}
	if e.logFile != "" {
		args = append(args, "--log-file="+e.logFile)
	}
	if opts.Delete {
		args = append(args, "--delete-after")
	}
	args = append(args, opts.ExtraOpts...)
	args = append(args, source, destination)

	name := filepath.Base(strings.TrimRight(source, "/"))

	start := time.Now()
	var capture bytes.Buffer
	cmd := exec.CommandContext(ctx, "rsync", args...)
	cmd.Stdout = io.MultiWriter(os.Stdout, &capture)
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	elapsed := time.Since(start)

	stats := ParseRsyncStats(capture.Bytes())
	stats.Elapsed = elapsed

	status := StatusOK
	if err != nil {
		status = StatusFailed
		e.logger.Error(fmt.Sprintf("rsync failed for %s: %v", source, err))
	}

	return ItemResult{Name: name, Status: status, Stats: stats}
}
