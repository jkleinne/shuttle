// Package engine implements the sync execution pipeline: stats types, output
// parsers, duration formatting, and summary rendering.
package engine

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Status represents the outcome of a single sync item.
type Status string

const (
	// StatusOK means the item synced successfully.
	StatusOK Status = "ok"
	// StatusFailed means the item encountered an error during sync.
	StatusFailed Status = "failed"
	// StatusSkipped means the item was excluded by job selection flags.
	StatusSkipped Status = "skipped"
	// StatusNotFound means the source path or remote could not be resolved.
	StatusNotFound Status = "not_found"
)

// TransferStats holds the quantitative output of a single rsync or rclone run.
// String fields (BytesSent, Speed) preserve the tool's own human-readable unit
// strings rather than converting to raw bytes, since they are only ever
// displayed to the user.
type TransferStats struct {
	FilesChecked     int
	FilesTransferred int
	FilesDeleted     int
	BytesSent        string        // human-readable, e.g. "234.5 MiB"
	Speed            string        // e.g. "5.6 MiB/s"
	Elapsed          time.Duration
}

// ItemResult holds the outcome of syncing a single source within a job.
type ItemResult struct {
	Name   string
	Status Status
	Stats  TransferStats
}

// JobResult groups the item-level outcomes for a named sync job or cloud remote.
type JobResult struct {
	// Name is the sync job name from the config (e.g. "manga", "docs-to-cloud").
	Name string
	// Remote is the rclone remote name for cloud jobs (e.g. "crypt_gdrive").
	// Empty for rsync jobs.
	Remote string
	Items  []ItemResult
}

// jobLabel returns a display-friendly identifier for a job result.
// For rclone jobs with a remote, it returns "name:remote".
// For rsync jobs (remote is empty), it returns just the name.
func jobLabel(name, remote string) string {
	if remote != "" {
		return name + ":" + remote
	}
	return name
}

// Summary is the top-level result returned after a full sync run.
type Summary struct {
	Jobs     []JobResult
	Errors   []string
	Duration time.Duration
	DryRun   bool
}

// HasErrors returns true when at least one item across all jobs has StatusFailed.
// Used by the CLI to choose a non-zero exit code after partial failures.
func (s Summary) HasErrors() bool {
	for _, job := range s.Jobs {
		for _, item := range job.Items {
			if item.Status == StatusFailed {
				return true
			}
		}
	}
	return false
}

// ParseRsyncStats extracts transfer metrics from the output produced by
// rsync --stats. It tolerates partial output by leaving unmatched fields at
// their zero values.
func ParseRsyncStats(data []byte) TransferStats {
	var stats TransferStats
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "Number of files:"):
			stats.FilesChecked = parseRsyncCount(line)
		case strings.HasPrefix(line, "Number of regular files transferred:"):
			stats.FilesTransferred = parseRsyncCount(line)
		case strings.HasPrefix(line, "Number of deleted files:"):
			stats.FilesDeleted = parseRsyncCount(line)
		case strings.HasPrefix(line, "sent "):
			stats.BytesSent, stats.Speed = parseRsyncSentLine(line)
		}
	}
	return stats
}

// parseRsyncCount extracts the first integer from a "Key: value (extra)" line.
// Commas used as thousands separators are stripped before parsing.
// Example: "Number of files: 14,101 (reg: 14,018, dir: 83)" → 14101
func parseRsyncCount(line string) int {
	// Take the portion after the colon.
	colonIdx := strings.IndexByte(line, ':')
	if colonIdx < 0 {
		return 0
	}
	rest := strings.TrimSpace(line[colonIdx+1:])
	// Drop any parenthetical suffix.
	if parenIdx := strings.IndexByte(rest, '('); parenIdx >= 0 {
		rest = strings.TrimSpace(rest[:parenIdx])
	}
	// Strip comma thousand-separators.
	rest = strings.ReplaceAll(rest, ",", "")
	n, _ := strconv.Atoi(rest)
	return n
}

// parseRsyncSentLine extracts BytesSent and Speed from rsync's summary line.
// Input: "sent 666.76K bytes  received 274 bytes  444.69K bytes/sec"
// Output: ("666.76K", "444.69K/s")
func parseRsyncSentLine(line string) (bytesSent, speed string) {
	fields := strings.Fields(line)
	// fields: ["sent", "666.76K", "bytes", "received", "274", "bytes",
	//          "444.69K", "bytes/sec"]
	if len(fields) >= 2 {
		bytesSent = fields[1]
	}
	// The speed value precedes "bytes/sec"; find that token.
	for i, f := range fields {
		if f == "bytes/sec" && i > 0 {
			// Strip the " bytes" qualifier from the speed value; append "/s".
			speed = fields[i-1] + "/s"
			break
		}
	}
	return bytesSent, speed
}

// ParseRcloneStats extracts transfer metrics from rclone's final stats output.
// It handles two forms of the "Transferred:" line: a bytes-with-speed line
// (contains "/s") and a count-only line (no "/s").
func ParseRcloneStats(data []byte) TransferStats {
	var stats TransferStats
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "Transferred:"):
			bytesSent, speed, count, hasBytes := parseRcloneTransferredLine(line)
			if hasBytes {
				stats.BytesSent = bytesSent
				stats.Speed = speed
			} else {
				stats.FilesTransferred = count
			}
		case strings.HasPrefix(line, "Checks:"):
			stats.FilesChecked = parseRcloneCount(line)
		case strings.HasPrefix(line, "Deleted:"):
			stats.FilesDeleted = parseRcloneDeletedLine(line)
		}
	}
	return stats
}

// parseRcloneTransferredLine distinguishes the two forms of rclone's
// "Transferred:" output line.
//
// Bytes-with-speed form (contains "/s"):
//
//	"Transferred:       1.082 GiB / 1.082 GiB, 100%, 32.709 KiB/s, ETA 0s"
//	→ bytesSent="1.082 GiB", speed="32.709 KiB/s", hasBytes=true
//
// Count-only form (no "/s"):
//
//	"Transferred:            12 / 50, 24%"
//	→ count=12, hasBytes=false
func parseRcloneTransferredLine(line string) (bytesSent, speed string, count int, hasBytes bool) {
	colonIdx := strings.IndexByte(line, ':')
	if colonIdx < 0 {
		return
	}
	rest := strings.TrimSpace(line[colonIdx+1:])
	if strings.Contains(rest, "/s") {
		// Bytes-with-speed line.
		// Format: "<amount> <unit> / <total> <unit>, <pct>%, <speed>/s, ETA <t>"
		// Split on "/" to isolate the "sent" portion.
		parts := strings.SplitN(rest, "/", 2)
		sent := strings.TrimSpace(parts[0])
		sentFields := strings.Fields(sent)
		if len(sentFields) >= 2 {
			bytesSent = sentFields[0] + " " + sentFields[1]
		}
		// The speed field contains "/s"; find it among comma-separated segments.
		for _, seg := range strings.Split(rest, ",") {
			seg = strings.TrimSpace(seg)
			if strings.Contains(seg, "/s") && !strings.HasPrefix(seg, "ETA") {
				speed = seg
				break
			}
		}
		hasBytes = true
		return
	}
	// Count-only line: "<n> / <total>, <pct>%"
	slashIdx := strings.IndexByte(rest, '/')
	if slashIdx < 0 {
		return
	}
	countStr := strings.TrimSpace(rest[:slashIdx])
	count, _ = strconv.Atoi(countStr)
	return
}

// parseRcloneCount extracts the left-hand value from "Key:   N / Total, pct%".
// Example: "Checks:   919 / 919, 100%" → 919
func parseRcloneCount(line string) int {
	colonIdx := strings.IndexByte(line, ':')
	if colonIdx < 0 {
		return 0
	}
	rest := strings.TrimSpace(line[colonIdx+1:])
	slashIdx := strings.IndexByte(rest, '/')
	if slashIdx < 0 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(rest[:slashIdx]))
	return n
}

// parseRcloneDeletedLine extracts only the file count from rclone's Deleted line.
// Example: "Deleted:   3 (files), 0 (dirs), 156 B (freed)" → 3
func parseRcloneDeletedLine(line string) int {
	colonIdx := strings.IndexByte(line, ':')
	if colonIdx < 0 {
		return 0
	}
	rest := strings.TrimSpace(line[colonIdx+1:])
	// Find the "(files)" marker and take the token immediately before it.
	filesIdx := strings.Index(rest, "(files)")
	if filesIdx < 0 {
		return 0
	}
	before := strings.TrimSpace(rest[:filesIdx])
	fields := strings.Fields(before)
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(fields[len(fields)-1])
	return n
}

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

const summaryDivider = "─────────────────────────────────────────────"

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

const (
	secondsPerMinute = 60
	secondsPerHour   = 3600
)

// FormatDuration formats a duration as a compact human-readable string.
//
// Formats:
//   - Under 60s: "38s"
//   - Under 1h:  "2m 05s"
//   - 1h or more: "1h 02m 05s"
func FormatDuration(d time.Duration) string {
	total := int(d.Seconds())
	seconds := total % secondsPerMinute
	minutes := (total / secondsPerMinute) % secondsPerMinute
	hours := total / secondsPerHour

	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %02dm %02ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
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
