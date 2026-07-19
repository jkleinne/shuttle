package engine

import (
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

const rsyncHumanReadableUnits = "KMGTP"

// NewRsyncExecutor returns a configured RsyncExecutor.
func NewRsyncExecutor(logger *log.Logger) *RsyncExecutor {
	return &RsyncExecutor{logger: logger}
}

func isASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isRsyncGroupedInteger(value string) bool {
	groups := strings.Split(value, ",")
	if len(groups) == 1 {
		return isASCIIDigits(value)
	}
	if len(groups[0]) == 0 || len(groups[0]) > 3 || !isASCIIDigits(groups[0]) {
		return false
	}
	for _, group := range groups[1:] {
		if len(group) != 3 || !isASCIIDigits(group) {
			return false
		}
	}
	return true
}

func isRsyncHumanReadableNumber(value string) bool {
	if len(value) < 2 || !strings.ContainsRune(rsyncHumanReadableUnits, rune(value[len(value)-1])) {
		return false
	}
	number := value[:len(value)-1]
	separator := strings.IndexAny(number, ".,")
	if separator < 0 {
		return isASCIIDigits(number)
	}
	fraction := number[separator+1:]
	if strings.ContainsAny(fraction, ".,") || len(fraction) == 0 || len(fraction) > 2 {
		return false
	}
	return isASCIIDigits(number[:separator]) && isASCIIDigits(fraction)
}

func isRsyncByteCounter(value string) bool {
	return isRsyncGroupedInteger(value) || isRsyncHumanReadableNumber(value)
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
	if !isRsyncByteCounter(fields[0]) {
		return ""
	}

	var pct, speed, eta string
	isProgressRecord := false
	for _, f := range fields {
		switch {
		case strings.HasPrefix(f, "(xfr#"):
			isProgressRecord = true
		case strings.HasSuffix(f, "%"):
			pct = f
		case strings.Contains(f, "/s"):
			speed = f
		case len(f) > 2 && f[0] != '(' && strings.Contains(f, ":"):
			eta = f
		}
	}

	if pct == "" || !isProgressRecord {
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

// scanRsyncProgress drains bounded records for focused progress tests. The
// executor uses the same capture primitive and adds trusted tool framing.
func scanRsyncProgress(r io.Reader, capture io.Writer, onProgress func(string)) {
	_ = captureDelimitedRecords(r, func(record string) {
		_, _ = io.WriteString(capture, record+"\n")
		if onProgress != nil {
			if progress := parseRsyncProgress(record); progress != "" {
				onProgress(progress)
			}
		}
	})
}

func (e *RsyncExecutor) captureStdoutRecord(
	record string,
	statsTail *tailBuffer,
	onProgress func(string),
) {
	_, _ = io.WriteString(statsTail, record+"\n")
	if progress := parseRsyncProgress(record); progress != "" {
		if onProgress != nil {
			onProgress(progress)
		}
		return
	}
	e.logger.FileTool("rsync", record)
}

func (e *RsyncExecutor) drainOutput(
	stdout io.Reader,
	stderr io.Reader,
	capture *tailBuffer,
	onProgress func(string),
) (stdoutReadErr, stderrReadErr error) {
	var pipeWg sync.WaitGroup
	pipeWg.Add(2)
	go func() {
		defer pipeWg.Done()
		stdoutReadErr = captureDelimitedRecords(stdout, func(record string) {
			e.captureStdoutRecord(record, capture, onProgress)
		})
	}()
	go func() {
		defer pipeWg.Done()
		stderrReadErr = captureDelimitedRecords(stderr, func(record string) {
			e.logger.FileTool("rsync", record)
		})
	}()
	pipeWg.Wait()
	return stdoutReadErr, stderrReadErr
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
	capture := newTailBuffer(statisticsTailBytes)

	cmd := exec.CommandContext(ctx, "rsync", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.logger.FileError(fmt.Sprintf("rsync pipe setup failed for %s: %v", source, err))
		return ItemResult{Name: name, Status: StatusFailed}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		e.logger.FileError(fmt.Sprintf("rsync stderr pipe setup failed for %s: %v", source, err))
		return ItemResult{Name: name, Status: StatusFailed}
	}

	if err := cmd.Start(); err != nil {
		status := classifyExitStatus(ctx, err)
		if status == StatusTimedOut {
			e.logger.FileError(fmt.Sprintf("rsync timed out for %s after per-job max_runtime: %v", source, err))
		} else {
			e.logger.FileError(fmt.Sprintf("rsync start failed for %s: %v", source, err))
		}
		return ItemResult{Name: name, Status: status}
	}

	stdoutReadErr, stderrReadErr := e.drainOutput(stdout, stderr, capture, onProgress)
	runErr := cmd.Wait()
	elapsed := time.Since(start)

	if stdoutReadErr != nil {
		e.logger.FileError(fmt.Sprintf("reading rsync stdout for %s: %v", source, stdoutReadErr))
	}
	if stderrReadErr != nil {
		e.logger.FileError(fmt.Sprintf("reading rsync stderr for %s: %v", source, stderrReadErr))
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
