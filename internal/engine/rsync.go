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

// scanRsyncProgress reads rsync stdout from r, writes the byte stream to
// capture (preserving output for ParseRsyncStats), and extracts progress
// updates from \r-delimited segments. Each progress update is passed to
// onProgress. If onProgress is nil, bytes are still written to capture but no
// parsing occurs.
func scanRsyncProgress(r io.Reader, capture io.Writer, onProgress func(string)) {
	buf := make([]byte, 4096)
	var segment []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = capture.Write(buf[:n])

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

// rsyncCaptureTailBytes bounds in-memory capture of rsync stdout. Only the
// trailing --stats block (~600 bytes) is parsed after the run; without a
// bound, user flags like -v make the captured listing grow with file count.
const rsyncCaptureTailBytes = 64 * 1024

// tailBuffer is an io.Writer retaining only the last capacity bytes written.
// Hand-rolled because the stdlib has no bounded byte-tail writer:
// bytes.Buffer is unbounded and container/ring is element-oriented.
type tailBuffer struct {
	capacity int
	buf      []byte
}

func newTailBuffer(capacity int) *tailBuffer {
	return &tailBuffer{capacity: capacity}
}

// Write keeps the suffix of the stream within capacity. It never fails; the
// error return exists to satisfy io.Writer.
func (t *tailBuffer) Write(p []byte) (int, error) {
	if len(p) >= t.capacity {
		t.buf = append(t.buf[:0], p[len(p)-t.capacity:]...)
		return len(p), nil
	}
	if overflow := len(t.buf) + len(p) - t.capacity; overflow > 0 {
		t.buf = append(t.buf[:0], t.buf[overflow:]...)
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

// Bytes returns the retained tail. The slice aliases internal storage and is
// valid until the next Write.
func (t *tailBuffer) Bytes() []byte {
	return t.buf
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
	capture := newTailBuffer(rsyncCaptureTailBytes)

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
		scanRsyncProgress(stdout, capture, onProgress)
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
