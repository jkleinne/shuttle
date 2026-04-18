package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jkleinne/shuttle/internal/log"
)

// RsyncExecutor wraps rsync execution via os/exec. It receives pre-assembled
// argument lists from the runner (built by BuildRsyncArgs) and handles command
// execution, output capture, and stats parsing.
type RsyncExecutor struct {
	logger *log.Logger
}

// NewRsyncExecutor returns a configured RsyncExecutor.
func NewRsyncExecutor(logger *log.Logger) *RsyncExecutor {
	return &RsyncExecutor{logger: logger}
}

// parseRsyncProgress extracts a progress string from an rsync --info=progress2
// output segment. Returns empty string if the segment is not a progress line.
//
// Input:  "  1,234,567  45%   2.30MB/s    0:01:23 (xfr#12, to-chk=88/100)"
// Output: "45%, 2.30MB/s, 0:01:23 remaining"
func parseRsyncProgress(segment string) string {
	fields := strings.Fields(segment)
	if len(fields) < 3 {
		return ""
	}

	var pct, speed, eta string
	for _, f := range fields {
		switch {
		case strings.HasSuffix(f, "%"):
			pct = f
		case strings.Contains(f, "/s"):
			speed = f
		case len(f) > 2 && f[0] != '(' && strings.Contains(f, ":"):
			eta = f
		}
	}

	if pct == "" {
		return ""
	}

	var parts []string
	parts = append(parts, pct)
	if speed != "" {
		parts = append(parts, speed)
	}
	if eta != "" && eta != "0:00:00" {
		parts = append(parts, eta+" remaining")
	}
	return strings.Join(parts, ", ")
}

// scanRsyncProgress reads rsync stdout from r, writes all bytes to capture
// (preserving raw output for ParseRsyncStats), and extracts progress updates
// from \r-delimited segments. Each progress update is passed to onProgress.
// If onProgress is nil, bytes are still written to capture but no parsing occurs.
func scanRsyncProgress(r io.Reader, capture *bytes.Buffer, onProgress func(string)) {
	buf := make([]byte, 4096)
	var segment []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			capture.Write(buf[:n])

			if onProgress != nil {
				for _, b := range buf[:n] {
					switch b {
					case '\r':
						if progress := parseRsyncProgress(string(segment)); progress != "" {
							onProgress(progress)
						}
						segment = segment[:0]
					case '\n':
						segment = segment[:0]
					default:
						segment = append(segment, b)
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// Exec runs rsync with the given pre-assembled argument list.
// Stdout is captured for stats parsing. If onProgress is non-nil, progress
// updates from --info=progress2 are parsed in real-time and forwarded.
func (e *RsyncExecutor) Exec(ctx context.Context, args []string, onProgress func(string)) ItemResult {
	source := ""
	if len(args) >= 2 {
		source = args[len(args)-2]
	}
	name := filepath.Base(strings.TrimRight(source, "/"))

	start := time.Now()
	var capture bytes.Buffer

	cmd := exec.CommandContext(ctx, "rsync", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.logger.FileError(fmt.Sprintf("rsync pipe setup failed for %s: %v", source, err))
		return ItemResult{Name: name, Status: StatusFailed}
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		status := classifyExitStatus(ctx, err)
		if status == StatusTimedOut {
			e.logger.FileError(fmt.Sprintf("rsync timed out for %s after per-job max_runtime: %v", source, err))
		} else {
			e.logger.FileError(fmt.Sprintf("rsync start failed for %s: %v", source, err))
		}
		return ItemResult{Name: name, Status: status}
	}

	var pipeWg sync.WaitGroup
	pipeWg.Add(1)
	go func() {
		defer pipeWg.Done()
		scanRsyncProgress(stdout, &capture, onProgress)
	}()

	pipeWg.Wait()
	runErr := cmd.Wait()
	elapsed := time.Since(start)

	// Log any stderr output to the log file for diagnostics.
	if stderrBuf.Len() > 0 {
		for _, line := range strings.Split(strings.TrimSpace(stderrBuf.String()), "\n") {
			if line != "" {
				e.logger.FileError(line)
			}
		}
	}

	stats := ParseRsyncStats(capture.Bytes())
	stats.Elapsed = elapsed

	status := classifyExitStatus(ctx, runErr)
	if runErr != nil {
		if status == StatusTimedOut {
			e.logger.FileError(fmt.Sprintf("rsync timed out for %s after per-job max_runtime: %v", source, runErr))
		} else {
			e.logger.FileError(fmt.Sprintf("rsync failed for %s: %v", source, runErr))
		}
	}

	return ItemResult{Name: name, Status: status, Stats: stats}
}
