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

// Exec runs rsync with the given pre-assembled argument list.
// The args slice must contain all flags, source, and destination (source is
// second-to-last, destination is last). Stdout is written to the terminal and
// simultaneously captured for stats parsing. Returns an ItemResult with parsed
// TransferStats and StatusOK or StatusFailed based on the rsync exit code.
func (e *RsyncExecutor) Exec(ctx context.Context, args []string) ItemResult {
	// Name is derived from the second-to-last arg (source).
	source := ""
	if len(args) >= 2 {
		source = args[len(args)-2]
	}
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
