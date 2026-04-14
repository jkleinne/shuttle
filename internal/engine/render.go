package engine

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ANSI escape codes for summary rendering. Duplicated from log/logger.go
// because the two uses serve different concerns (log messages vs summary output)
// and the log constants are unexported.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBlue   = "\033[34m"
)

const ansiClearLine = "\033[2K\r"

const summaryDivider = "─────────────────────────────────────────────"

// colorize wraps text in ANSI escape codes when color is enabled.
// When disabled, returns text unchanged for plain-text output.
func colorize(useColor bool, code, text string) string {
	if !useColor {
		return text
	}
	return code + text + ansiReset
}

// statusSymbol returns a colored status indicator character.
func statusSymbol(status Status, useColor bool) string {
	switch status {
	case StatusFailed, StatusNotFound:
		return colorize(useColor, ansiRed, "✗")
	case StatusSkipped:
		return colorize(useColor, ansiYellow, "–")
	default:
		return colorize(useColor, ansiGreen, "✓")
	}
}

// itemStatsText returns the formatted stats string for a single item,
// colored by status.
func itemStatsText(item ItemResult, useColor bool) string {
	switch item.Status {
	case StatusFailed:
		return colorize(useColor, ansiRed, "failed")
	case StatusNotFound:
		return colorize(useColor, ansiRed, "not found")
	case StatusSkipped:
		return colorize(useColor, ansiYellow, "skipped")
	default:
		return colorize(useColor, ansiDim, formatChecked(item.Stats))
	}
}

// jobStatus returns StatusFailed if any item in the job failed or was not found.
func jobStatus(job JobResult) Status {
	for _, item := range job.Items {
		if item.Status == StatusFailed || item.Status == StatusNotFound {
			return StatusFailed
		}
	}
	return StatusOK
}

// groupStatus returns StatusFailed if any job in the group has failures.
func groupStatus(group []JobResult) Status {
	for _, jr := range group {
		if jobStatus(jr) == StatusFailed {
			return StatusFailed
		}
	}
	return StatusOK
}

// collectGroup returns the slice of consecutive JobResults starting at index
// that share the same Name and have non-empty Remote fields.
func collectGroup(jobs []JobResult, start int) []JobResult {
	name := jobs[start].Name
	end := start + 1
	for end < len(jobs) && jobs[end].Name == name && jobs[end].Remote != "" {
		end++
	}
	return jobs[start:end]
}

// canCollapseGroup returns true when all remotes in a group have the same
// FilesChecked count, all succeeded, and none transferred files.
// Relies on the JobResult.Items invariant: Items always has at least one element.
func canCollapseGroup(group []JobResult) bool {
	if len(group) <= 1 {
		return false
	}
	first := group[0].Items[0]
	if first.Status != StatusOK || first.Stats.FilesTransferred > 0 {
		return false
	}
	for _, jr := range group[1:] {
		item := jr.Items[0]
		if item.Status != StatusOK || item.Stats.FilesTransferred > 0 {
			return false
		}
		if item.Stats.FilesChecked != first.Stats.FilesChecked {
			return false
		}
	}
	return true
}

// maxGroupElapsed returns the longest elapsed time across all items in a group.
// Relies on the JobResult.Items invariant: Items always has at least one element.
func maxGroupElapsed(group []JobResult) time.Duration {
	var max time.Duration
	for _, jr := range group {
		if jr.Items[0].Stats.Elapsed > max {
			max = jr.Items[0].Stats.Elapsed
		}
	}
	return max
}

// maxJobNameWidth returns the length of the longest unique job name in the
// summary, used to align stats columns.
func maxJobNameWidth(jobs []JobResult) int {
	seen := make(map[string]bool)
	maxLen := 0
	for _, j := range jobs {
		if seen[j.Name] {
			continue
		}
		seen[j.Name] = true
		if len(j.Name) > maxLen {
			maxLen = len(j.Name)
		}
	}
	return maxLen
}

// formatTransfer returns a detail line for items that transferred files.
// Format: "N transferred, B sent at S"
func formatTransfer(s TransferStats) string {
	return fmt.Sprintf("%d transferred, %s sent at %s", s.FilesTransferred, s.BytesSent, s.Speed)
}

// formatChecked returns "N checked" with optional elapsed time.
// Elapsed is shown only when >= 1 second to suppress noise from fast jobs.
func formatChecked(s TransferStats) string {
	checked := formatNumber(s.FilesChecked)
	if s.Elapsed >= time.Second {
		return fmt.Sprintf("%s checked (%s)", checked, FormatDuration(s.Elapsed))
	}
	return checked + " checked"
}

// formatNumber inserts thousand separators into a non-negative integer.
// Example: 14101 → "14,101".
func formatNumber(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var buf strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		buf.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if buf.Len() > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(s[i : i+3])
	}
	return buf.String()
}

// renderRsyncJob writes one rsync job to the summary.
// Single-source jobs get one line; multi-source jobs get a header plus indented items.
func renderRsyncJob(w io.Writer, job JobResult, nameWidth int, useColor bool) {
	status := jobStatus(job)
	symbol := statusSymbol(status, useColor)

	if len(job.Items) == 1 {
		item := job.Items[0]
		stats := itemStatsText(item, useColor)
		fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, job.Name, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			fmt.Fprintf(w, "    %s\n", colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
		return
	}

	fmt.Fprintf(w, "  %s %s\n", symbol, job.Name)
	for _, item := range job.Items {
		stats := itemStatsText(item, useColor)
		fmt.Fprintf(w, "      %s: %s\n", item.Name, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			fmt.Fprintf(w, "      %s\n", colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
	}
}

// renderRcloneGroup writes a group of rclone results for the same config job.
// Groups with identical stats collapse into one line; others expand into a tree.
// Relies on the JobResult.Items invariant: Items always has at least one element.
func renderRcloneGroup(w io.Writer, group []JobResult, nameWidth int, useColor bool) {
	status := groupStatus(group)
	symbol := statusSymbol(status, useColor)
	name := group[0].Name

	if len(group) == 1 {
		item := group[0].Items[0]
		label := group[0].Remote + " · " + formatChecked(item.Stats)
		stats := colorize(useColor, ansiDim, label)
		if item.Status != StatusOK {
			stats = itemStatsText(item, useColor)
		}
		fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, name, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			fmt.Fprintf(w, "    %s\n", colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
		return
	}

	if canCollapseGroup(group) {
		checked := group[0].Items[0].Stats.FilesChecked
		elapsed := maxGroupElapsed(group)
		collapsedStats := TransferStats{FilesChecked: checked, Elapsed: elapsed}
		label := fmt.Sprintf("%d remotes · %s", len(group), formatChecked(collapsedStats))
		fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, name,
			colorize(useColor, ansiDim, label))
		return
	}

	fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, name,
		colorize(useColor, ansiDim, fmt.Sprintf("%d remotes", len(group))))
	for idx, jr := range group {
		item := jr.Items[0]
		isLast := idx == len(group)-1
		branch := "├"
		pipe := "│"
		if isLast {
			branch = "└"
			pipe = " "
		}
		stats := itemStatsText(item, useColor)
		fmt.Fprintf(w, "    %s %s  %s\n",
			colorize(useColor, ansiDim, branch), jr.Remote, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			fmt.Fprintf(w, "    %s %s\n",
				colorize(useColor, ansiDim, pipe),
				colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
	}
}

// renderSkippedJob writes a single skipped-job line.
func renderSkippedJob(w io.Writer, job JobResult, nameWidth int, useColor bool) {
	symbol := statusSymbol(StatusSkipped, useColor)
	fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, job.Name,
		colorize(useColor, ansiYellow, "skipped"))
}

// formatTally builds the colored footer tally line.
// The failed segment is omitted when zero.
func formatTally(passed, failed int, d time.Duration, useColor bool) string {
	var parts []string
	parts = append(parts, colorize(useColor, ansiGreen, fmt.Sprintf("%d passed", passed)))
	if failed > 0 {
		parts = append(parts, colorize(useColor, ansiRed, fmt.Sprintf("%d failed", failed)))
	}
	parts = append(parts, colorize(useColor, ansiDim, "Duration: "+FormatDuration(d)))
	return "  " + strings.Join(parts, "  ")
}

// RenderSummary writes a grouped, color-coded run summary to w.
//
// Jobs are displayed with status symbols (✓/✗/–). Rclone jobs targeting
// multiple remotes are grouped: identical results collapse to one line,
// differing results expand into a tree with ├/└ branches. Transfer details
// appear only when files were actually moved. Color output is controlled
// by useColor; when false, plain text is emitted for log files and pipes.
func RenderSummary(w io.Writer, s Summary, useColor bool) {
	fmt.Fprintln(w, colorize(useColor, ansiDim, summaryDivider))
	fmt.Fprintln(w, colorize(useColor, ansiBold+ansiBlue, " Sync Summary"))
	if s.DryRun {
		fmt.Fprintln(w, colorize(useColor, ansiYellow, "  [DRY RUN]"))
	}
	fmt.Fprintln(w)

	nameWidth := maxJobNameWidth(s.Jobs)
	var passed, failed int

	i := 0
	for i < len(s.Jobs) {
		job := s.Jobs[i]

		if len(job.Items) == 1 && job.Items[0].Status == StatusSkipped {
			renderSkippedJob(w, job, nameWidth, useColor)
			i++
			continue
		}

		if job.Remote != "" {
			group := collectGroup(s.Jobs, i)
			renderRcloneGroup(w, group, nameWidth, useColor)
			if groupStatus(group) == StatusFailed {
				failed++
			} else {
				passed++
			}
			i += len(group)
			continue
		}

		renderRsyncJob(w, job, nameWidth, useColor)
		if jobStatus(job) == StatusFailed {
			failed++
		} else {
			passed++
		}
		i++
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, colorize(useColor, ansiDim, summaryDivider))
	fmt.Fprintln(w, formatTally(passed, failed, s.Duration, useColor))

	if len(s.Errors) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, colorize(useColor, ansiRed, "  Errors:"))
		for _, e := range s.Errors {
			fmt.Fprintf(w, "    %s\n", colorize(useColor, ansiRed, "- "+e))
		}
	}
}
