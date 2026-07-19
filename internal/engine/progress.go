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
	w         io.Writer
	mode      ProgressMode
	colorMode TerminalColorMode

	stateMutex      sync.Mutex
	outputMutex     sync.Mutex
	currentLabel    string
	currentProgress string
	spinnerIdx      int
	startTime       time.Time

	done chan struct{}
	wg   sync.WaitGroup
}

// NewProgressWriter creates the terminal progress boundary with explicit,
// zero-safe presentation options for cursor manipulation and Shuttle styling.
func NewProgressWriter(w io.Writer, options ProgressOptions) *ProgressWriter {
	return &ProgressWriter{
		w:         w,
		mode:      options.Mode,
		colorMode: options.Color,
	}
}

// ProgressCallback returns the UpdateProgress method as a callback suitable
// for passing to executors. Returns nil in non-interactive mode so executors
// skip progress parsing.
func (pw *ProgressWriter) ProgressCallback() func(string) {
	if pw.mode != ProgressInteractive {
		return nil
	}
	return pw.UpdateProgress
}

// Interactive returns whether the writer uses cursor manipulation.
func (pw *ProgressWriter) Interactive() bool {
	return pw.mode == ProgressInteractive
}

// StartJob begins displaying a spinner for the named job.
// In non-interactive mode, the label is stored but no output is produced.
// Precondition: each StartJob must be paired with a FinishJob call before
// calling StartJob again.
func (pw *ProgressWriter) StartJob(ctx context.Context, label string) {
	pw.stateMutex.Lock()
	pw.currentLabel = SanitizeTerminalText(label)
	pw.currentProgress = ""
	pw.spinnerIdx = 0
	pw.startTime = time.Now()
	pw.stateMutex.Unlock()

	if pw.mode != ProgressInteractive {
		return
	}

	pw.done = make(chan struct{})
	pw.renderSpinner()
	pw.wg.Add(1)
	go pw.spin(ctx)
}

// UpdateProgress sets the progress text displayed beside the spinner.
// Replaces the elapsed-time default when non-empty.
// Control characters (C0, C1, DEL) are stripped before storage so that
// attacker-influenceable filenames in tool output cannot inject terminal escapes.
// In non-interactive mode, this is a no-op.
func (pw *ProgressWriter) UpdateProgress(text string) {
	if pw.mode != ProgressInteractive {
		return
	}
	pw.stateMutex.Lock()
	pw.currentProgress = SanitizeTerminalText(text)
	pw.stateMutex.Unlock()
	pw.renderSpinner()
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
	if pw.mode == ProgressInteractive {
		close(pw.done)
		pw.wg.Wait()
	}

	pw.stateMutex.Lock()
	label := pw.currentLabel
	pw.stateMutex.Unlock()

	symbol := statusSymbol(result.Status, pw.colorMode)
	stats := itemStatsText(result, pw.colorMode)

	pw.outputMutex.Lock()
	defer pw.outputMutex.Unlock()
	if pw.mode == ProgressInteractive {
		// Terminal write errors are unrecoverable at this call site; the write
		// is best-effort output and no meaningful recovery action is possible.
		_, _ = fmt.Fprint(pw.w, ansiClearLine)
	}
	_, _ = fmt.Fprintf(pw.w, "%s %s  %s\n", symbol, label, stats)

	if result.Status == StatusOK && result.Stats.FilesTransferred > 0 {
		_, _ = fmt.Fprintf(pw.w, "    %s\n",
			colorize(pw.colorMode, ansiGreen, formatTransfer(result.Stats)))
	}
}

// SkipJob writes a skip status line without starting a spinner.
// Safe to call without a preceding StartJob.
func (pw *ProgressWriter) SkipJob(name string) {
	symbol := statusSymbol(StatusSkipped, pw.colorMode)
	cleanName := SanitizeTerminalText(name)

	pw.outputMutex.Lock()
	defer pw.outputMutex.Unlock()
	_, _ = fmt.Fprintf(pw.w, "%s %s  %s\n", symbol, cleanName,
		colorize(pw.colorMode, ansiYellow, "skipped"))
}

// spin is the spinner goroutine. It writes the current spinner frame at
// regular intervals until done is closed or ctx is canceled.
func (pw *ProgressWriter) spin(ctx context.Context) {
	defer pw.wg.Done()
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pw.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			pw.stateMutex.Lock()
			pw.spinnerIdx = (pw.spinnerIdx + 1) % len(spinnerFrames)
			pw.stateMutex.Unlock()
			pw.renderSpinner()
		}
	}
}

// renderSpinner writes the current spinner frame, label, and progress text,
// overwriting the previous line content via ANSI clear-line and carriage return.
func (pw *ProgressWriter) renderSpinner() {
	pw.stateMutex.Lock()
	frame := spinnerFrames[pw.spinnerIdx]
	label := pw.currentLabel
	progress := pw.currentProgress
	elapsed := time.Since(pw.startTime)
	pw.stateMutex.Unlock()

	if progress == "" {
		progress = FormatDuration(elapsed)
	}

	coloredFrame := colorize(pw.colorMode, ansiBlue, frame)
	coloredProgress := colorize(pw.colorMode, ansiDim, progress)

	pw.outputMutex.Lock()
	defer pw.outputMutex.Unlock()
	_, _ = fmt.Fprintf(pw.w, "%s%s %s  %s", ansiClearLine, coloredFrame, label, coloredProgress)
}
