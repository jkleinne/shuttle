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

// Summary-line symbols and labels. Kept as named constants so the
// rendering contract lives in one place. Tests assert on the literal
// strings (not these names) so the constants remain single-sourced
// while tests continue to verify user-observable output.
const (
	symbolOK               = "✓"
	symbolFailed           = "✗"
	symbolSkipped          = "–"
	symbolOptionalMissing  = "○"
	labelFailed            = "failed"
	labelNotFound          = "not found"
	labelTimedOut          = "timed out"
	labelSkipped           = "skipped"
	labelOptionalMissing   = "source missing (optional)"
	tallyLabelPassed       = "passed"
	tallyLabelOptional     = "optional"
	tallyLabelFailed       = "failed"
	collapseSuffixOptional = "source missing (optional)"
)

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
	case StatusFailed, StatusNotFound, StatusTimedOut:
		return colorize(useColor, ansiRed, symbolFailed)
	case StatusSkipped:
		return colorize(useColor, ansiYellow, symbolSkipped)
	case StatusOptionalMissing:
		return colorize(useColor, ansiDim, symbolOptionalMissing)
	default:
		return colorize(useColor, ansiGreen, symbolOK)
	}
}

// itemStatsText returns the formatted stats string for a single item,
// colored by status.
func itemStatsText(item ItemResult, useColor bool) string {
	switch item.Status {
	case StatusFailed:
		return colorize(useColor, ansiRed, labelFailed)
	case StatusNotFound:
		return colorize(useColor, ansiRed, labelNotFound)
	case StatusTimedOut:
		return colorize(useColor, ansiRed, labelTimedOut)
	case StatusSkipped:
		return colorize(useColor, ansiYellow, labelSkipped)
	case StatusOptionalMissing:
		return colorize(useColor, ansiDim, labelOptionalMissing)
	default:
		return colorize(useColor, ansiDim, formatChecked(item.Stats))
	}
}

// aggregateStatus folds a sequence of statuses into one of three buckets:
//
//   - StatusFailed: any input status satisfies Status.IsFailure
//   - StatusOptionalMissing: every input status is StatusOptionalMissing
//   - StatusOK: everything else (including mixed OK + optional-missing, or
//     an empty input — empty is treated as OK since "nothing happened" is
//     not itself a failure nor an explicit optional skip)
//
// jobStatus and groupStatus both delegate here so the classification rule
// is defined in one place.
func aggregateStatus(statuses []Status) Status {
	if len(statuses) == 0 {
		return StatusOK
	}
	allOptionalMissing := true
	for _, s := range statuses {
		if s.IsFailure() {
			return StatusFailed
		}
		if s != StatusOptionalMissing {
			allOptionalMissing = false
		}
	}
	if allOptionalMissing {
		return StatusOptionalMissing
	}
	return StatusOK
}

// jobStatus classifies a job into one of three buckets (see aggregateStatus).
// The returned value selects the job-level symbol via statusSymbol and the
// tally counter the job increments in RenderSummary.
func jobStatus(job JobResult) Status {
	statuses := make([]Status, len(job.Items))
	for i, item := range job.Items {
		statuses[i] = item.Status
	}
	return aggregateStatus(statuses)
}

// groupStatus classifies an rclone group using the same three-bucket rule
// as jobStatus, applied to each member's jobStatus.
func groupStatus(group []JobResult) Status {
	statuses := make([]Status, len(group))
	for i, jr := range group {
		statuses[i] = jobStatus(jr)
	}
	return aggregateStatus(statuses)
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

// canCollapseGroup returns true when a group of rclone results can be
// rendered as a single collapsed line instead of a tree. Two shapes
// collapse:
//
//  1. All remotes succeeded with the same FilesChecked count and zero
//     transfers — the uninteresting "everything up to date" case.
//  2. All remotes are StatusOptionalMissing — the "source not plugged in"
//     case, which would otherwise repeat the same line per remote.
//
// Relies on the JobResult.Items invariant: Items always has at least one element.
func canCollapseGroup(group []JobResult) bool {
	if len(group) <= 1 {
		return false
	}

	// Shape 2: all-optional-missing collapses regardless of stats.
	allOptionalMissing := true
	for _, jr := range group {
		if jr.Items[0].Status != StatusOptionalMissing {
			allOptionalMissing = false
			break
		}
	}
	if allOptionalMissing {
		return true
	}

	// Shape 1: uniform all-OK with same FilesChecked and zero transfers.
	// Any other shape (failed, mixed-with-optional, differing counts) falls
	// through and returns false via the guards below.
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

// renderRsyncJob writes one rsync job to the summary and returns the
// classified job status so the caller can tally it without re-scanning.
// Single-source jobs get one line; multi-source jobs get a header plus
// indented items.
func renderRsyncJob(w io.Writer, job JobResult, nameWidth int, useColor bool) Status {
	status := jobStatus(job)
	symbol := statusSymbol(status, useColor)

	if len(job.Items) == 1 {
		item := job.Items[0]
		stats := itemStatsText(item, useColor)
		_, _ = fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, job.Name, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			_, _ = fmt.Fprintf(w, "    %s\n", colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
		return status
	}

	_, _ = fmt.Fprintf(w, "  %s %s\n", symbol, job.Name)
	for _, item := range job.Items {
		stats := itemStatsText(item, useColor)
		_, _ = fmt.Fprintf(w, "      %s: %s\n", item.Name, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			_, _ = fmt.Fprintf(w, "      %s\n", colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
	}
	return status
}

// renderRcloneGroup writes a group of rclone results for the same config
// job and returns the classified group status so the caller can tally it
// without re-scanning. Groups with identical stats collapse into one line;
// others expand into a tree.
// Relies on the JobResult.Items invariant: Items always has at least one element.
func renderRcloneGroup(w io.Writer, group []JobResult, nameWidth int, useColor bool) Status {
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
		_, _ = fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, name, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			_, _ = fmt.Fprintf(w, "    %s\n", colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
		return status
	}

	if canCollapseGroup(group) {
		// Differentiate the two collapse shapes using the status already
		// computed above, no need to re-scan the group.
		if status == StatusOptionalMissing {
			label := fmt.Sprintf("%d remotes · %s", len(group), collapseSuffixOptional)
			_, _ = fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, name,
				colorize(useColor, ansiDim, label))
			return status
		}
		checked := group[0].Items[0].Stats.FilesChecked
		elapsed := maxGroupElapsed(group)
		collapsedStats := TransferStats{FilesChecked: checked, Elapsed: elapsed}
		label := fmt.Sprintf("%d remotes · %s", len(group), formatChecked(collapsedStats))
		_, _ = fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, name,
			colorize(useColor, ansiDim, label))
		return status
	}

	_, _ = fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, name,
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
		_, _ = fmt.Fprintf(w, "    %s %s  %s\n",
			colorize(useColor, ansiDim, branch), jr.Remote, stats)
		if item.Status == StatusOK && item.Stats.FilesTransferred > 0 {
			_, _ = fmt.Fprintf(w, "    %s %s\n",
				colorize(useColor, ansiDim, pipe),
				colorize(useColor, ansiGreen, formatTransfer(item.Stats)))
		}
	}
	return status
}

// renderSkippedJob writes a single skipped-job line.
func renderSkippedJob(w io.Writer, job JobResult, nameWidth int, useColor bool) {
	symbol := statusSymbol(StatusSkipped, useColor)
	_, _ = fmt.Fprintf(w, "  %s %-*s  %s\n", symbol, nameWidth, job.Name,
		colorize(useColor, ansiYellow, labelSkipped))
}

// incrementTally bumps the tally counter that corresponds to the given
// classified status: StatusFailed → failed; StatusOptionalMissing →
// optional; anything else (including mixed OK + optional-missing jobs)
// → passed. Keeps the mapping colocated with formatTally.
func incrementTally(status Status, passed, optional, failed *int) {
	switch status {
	case StatusFailed:
		*failed++
	case StatusOptionalMissing:
		*optional++
	default:
		*passed++
	}
}

// formatTally builds the colored footer tally line.
// The failed and optional segments are omitted when their counts are zero.
func formatTally(passed, optional, failed int, d time.Duration, useColor bool) string {
	var parts []string
	parts = append(parts, colorize(useColor, ansiGreen, fmt.Sprintf("%d %s", passed, tallyLabelPassed)))
	if optional > 0 {
		parts = append(parts, colorize(useColor, ansiDim, fmt.Sprintf("%d %s", optional, tallyLabelOptional)))
	}
	if failed > 0 {
		parts = append(parts, colorize(useColor, ansiRed, fmt.Sprintf("%d %s", failed, tallyLabelFailed)))
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
	_, _ = fmt.Fprintln(w, colorize(useColor, ansiDim, summaryDivider))
	_, _ = fmt.Fprintln(w, colorize(useColor, ansiBold+ansiBlue, " Sync Summary"))
	if s.DryRun {
		_, _ = fmt.Fprintln(w, colorize(useColor, ansiYellow, "  [DRY RUN]"))
	}
	_, _ = fmt.Fprintln(w)

	nameWidth := maxJobNameWidth(s.Jobs)
	var passed, optional, failed int

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
			status := renderRcloneGroup(w, group, nameWidth, useColor)
			incrementTally(status, &passed, &optional, &failed)
			i += len(group)
			continue
		}

		status := renderRsyncJob(w, job, nameWidth, useColor)
		incrementTally(status, &passed, &optional, &failed)
		i++
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, colorize(useColor, ansiDim, summaryDivider))
	_, _ = fmt.Fprintln(w, formatTally(passed, optional, failed, s.Duration, useColor))

	if len(s.Errors) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, colorize(useColor, ansiRed, "  Errors:"))
		for _, e := range s.Errors {
			_, _ = fmt.Fprintf(w, "    %s\n", colorize(useColor, ansiRed, "- "+e))
		}
	}
}
