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
	// Name is the sync job name (e.g. "manga") or "cloud:remote_name".
	Name  string
	Items []ItemResult
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

// FormatItemStats returns a single-line summary string for a completed item.
// The format varies by status:
//   - ok with transfers:  "N checked, M transferred, B sent at S (Ts)"
//   - ok without transfers: "N checked (Ts)"
//   - failed: "[failed]"
//   - skipped: "[skipped]"
//   - not_found: "[not found]"
func FormatItemStats(r ItemResult) string {
	switch r.Status {
	case StatusFailed:
		return "[failed]"
	case StatusSkipped:
		return "[skipped]"
	case StatusNotFound:
		return "[not found]"
	}
	// StatusOK
	s := r.Stats
	if s.FilesTransferred > 0 {
		return fmt.Sprintf("%d checked, %d transferred, %s sent at %s (%s)",
			s.FilesChecked, s.FilesTransferred, s.BytesSent, s.Speed, FormatDuration(s.Elapsed))
	}
	return fmt.Sprintf("%d checked (%s)", s.FilesChecked, FormatDuration(s.Elapsed))
}

// RenderSummary writes a grouped, human-readable run summary to w.
//
// Output format:
//
//	=== Sync Summary ===
//	manga:
//	  mangas: 100 checked (5s)
//	documents-to-cloud:crypt_gdrive:
//	  Documents: 50 checked, 3 transferred, 12.3 MiB sent at 2.1 MiB/s (15s)
//	Duration: 30s
//
// A "DRY RUN" notice is prepended when Summary.DryRun is true.
// Failed items are listed in a trailing "Errors:" section when present.
func RenderSummary(w io.Writer, s Summary) {
	if s.DryRun {
		fmt.Fprintln(w, "[DRY RUN]")
	}
	fmt.Fprintln(w, "=== Sync Summary ===")
	for _, job := range s.Jobs {
		fmt.Fprintf(w, "%s:\n", job.Name)
		for _, item := range job.Items {
			fmt.Fprintf(w, "  %s: %s\n", item.Name, FormatItemStats(item))
		}
	}
	fmt.Fprintf(w, "Duration: %s\n", FormatDuration(s.Duration))
	if len(s.Errors) > 0 {
		fmt.Fprintln(w, "Errors:")
		for _, e := range s.Errors {
			fmt.Fprintf(w, "  - %s\n", e)
		}
	}
}
