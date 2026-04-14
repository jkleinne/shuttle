package engine

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

const spinnerInterval = 80 * time.Millisecond

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ProgressWriter manages the live terminal display during job execution.
// In interactive mode, it shows a spinner on the active job line and replaces
// it with a compact status line when the job finishes. In non-interactive mode
// (pipes, log files, cron), StartJob and UpdateProgress are no-ops; FinishJob
// and SkipJob print plain status lines with no cursor manipulation.
type ProgressWriter struct {
	w           io.Writer
	interactive bool
	useColor    bool

	mu              sync.Mutex
	currentLabel    string
	currentProgress string
	spinnerIdx      int
	startTime       time.Time

	done chan struct{}
	wg   sync.WaitGroup
}

// NewProgressWriter creates a ProgressWriter that writes to w.
// When interactive is true, the writer uses ANSI cursor manipulation for
// in-place spinner updates. Set useColor to match the terminal's color support.
func NewProgressWriter(w io.Writer, interactive bool, useColor bool) *ProgressWriter {
	return &ProgressWriter{
		w:           w,
		interactive: interactive,
		useColor:    useColor,
	}
}

// Interactive returns whether the writer uses cursor manipulation.
func (pw *ProgressWriter) Interactive() bool {
	return pw.interactive
}

// StartJob begins displaying a spinner for the named job.
// In non-interactive mode, the label is stored but no output is produced.
// Precondition: each StartJob must be paired with a FinishJob call before
// calling StartJob again.
func (pw *ProgressWriter) StartJob(ctx context.Context, label string) {
	pw.mu.Lock()
	pw.currentLabel = label
	pw.currentProgress = ""
	pw.spinnerIdx = 0
	pw.startTime = time.Now()
	pw.done = make(chan struct{})
	pw.mu.Unlock()

	if !pw.interactive {
		return
	}

	pw.wg.Add(1)
	go pw.spin(ctx)
}

// UpdateProgress sets the progress text displayed beside the spinner.
// Replaces the elapsed-time default when non-empty.
// In non-interactive mode, this is a no-op.
func (pw *ProgressWriter) UpdateProgress(text string) {
	if !pw.interactive {
		return
	}
	pw.mu.Lock()
	pw.currentProgress = text
	pw.mu.Unlock()
}

// FinishJob stops the spinner and writes the final status line for the job
// started by the most recent StartJob call.
//
// In interactive mode, the spinner goroutine is stopped and the spinner line
// is cleared before writing the status line. In non-interactive mode, the
// status line is written directly.
//
// A transfer detail line is appended when result.Status is StatusOK and
// result.Stats.FilesTransferred > 0.
func (pw *ProgressWriter) FinishJob(result ItemResult) {
	if pw.interactive {
		close(pw.done)
		pw.wg.Wait()
		fmt.Fprint(pw.w, "\033[2K\r")
	}

	pw.mu.Lock()
	label := pw.currentLabel
	pw.mu.Unlock()

	symbol := statusSymbol(result.Status, pw.useColor)
	stats := itemStatsText(result, pw.useColor)
	fmt.Fprintf(pw.w, "%s %s  %s\n", symbol, label, stats)

	if result.Status == StatusOK && result.Stats.FilesTransferred > 0 {
		fmt.Fprintf(pw.w, "    %s\n",
			colorize(pw.useColor, ansiGreen, formatTransfer(result.Stats)))
	}
}

// SkipJob writes a skip status line without starting a spinner.
// Safe to call without a preceding StartJob.
func (pw *ProgressWriter) SkipJob(name string) {
	symbol := statusSymbol(StatusSkipped, pw.useColor)
	fmt.Fprintf(pw.w, "%s %s  %s\n", symbol, name,
		colorize(pw.useColor, ansiYellow, "skipped"))
}

// spin is the spinner goroutine. It writes the current spinner frame at
// regular intervals until done is closed or ctx is canceled.
func (pw *ProgressWriter) spin(ctx context.Context) {
	defer pw.wg.Done()
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	pw.renderSpinner()

	for {
		select {
		case <-pw.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			pw.mu.Lock()
			pw.spinnerIdx = (pw.spinnerIdx + 1) % len(spinnerFrames)
			pw.mu.Unlock()
			pw.renderSpinner()
		}
	}
}

// renderSpinner writes the current spinner frame, label, and progress text,
// overwriting the previous line content via ANSI clear-line and carriage return.
func (pw *ProgressWriter) renderSpinner() {
	pw.mu.Lock()
	frame := spinnerFrames[pw.spinnerIdx]
	label := pw.currentLabel
	progress := pw.currentProgress
	elapsed := time.Since(pw.startTime)
	pw.mu.Unlock()

	if progress == "" {
		progress = FormatDuration(elapsed)
	}

	coloredFrame := colorize(pw.useColor, ansiBlue, frame)
	coloredProgress := colorize(pw.useColor, ansiDim, progress)

	fmt.Fprintf(pw.w, "\033[2K\r%s %s  %s", coloredFrame, label, coloredProgress)
}
