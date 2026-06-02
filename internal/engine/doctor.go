package engine

import (
	"fmt"
	"io"
	"strings"
)

// symbolWarn is the doctor-only warning glyph. The ✓/✗ glyphs are shared with
// render.go via symbolOK/symbolFailed; WARN has no analog there because the
// sync Status type has no warning state.
const symbolWarn = "⚠"

// CheckLevel is the severity of a single diagnostic check.
type CheckLevel int

const (
	CheckOK   CheckLevel = iota
	CheckWarn            // non-fatal advisory
	CheckFail            // hard failure; drives the exit code via Report.HasFailures
)

// CheckResult is the outcome of one diagnostic probe. Name is the fixed label
// column ("rsync", "rclone", "config", "remote", "filter file"); Detail carries
// the specific subject and/or reason (a version, a resolved path, a remote
// name, or "<subject> — <one-line reason>" on WARN/FAIL).
type CheckResult struct {
	Name   string
	Level  CheckLevel
	Detail string
}

// Report is the full set of diagnostic results, in display order.
type Report struct {
	Checks []CheckResult
}

// HasFailures reports whether any check failed. It drives the doctor exit code.
func (r Report) HasFailures() bool {
	for _, c := range r.Checks {
		if c.Level == CheckFail {
			return true
		}
	}
	return false
}

// checkSymbol returns the colored glyph for a check level, reusing render.go's
// colorize helper and ANSI color constants.
func checkSymbol(level CheckLevel, useColor bool) string {
	switch level {
	case CheckFail:
		return colorize(useColor, ansiRed, symbolFailed)
	case CheckWarn:
		return colorize(useColor, ansiYellow, symbolWarn)
	default:
		return colorize(useColor, ansiGreen, symbolOK)
	}
}

// RenderReport writes the status-first checklist and a tally footer to w.
// useColor controls ANSI escape codes; pass the same value as RenderSummary.
// An empty Checks slice renders header + "no checks run" footer without panicking.
func RenderReport(w io.Writer, report Report, useColor bool) {
	_, _ = fmt.Fprintln(w, "shuttle doctor")
	_, _ = fmt.Fprintln(w)

	nameWidth := 0
	for _, c := range report.Checks {
		if len(c.Name) > nameWidth {
			nameWidth = len(c.Name)
		}
	}

	var ok, warn, fail int
	for _, c := range report.Checks {
		switch c.Level {
		case CheckOK:
			ok++
		case CheckWarn:
			warn++
		case CheckFail:
			fail++
		}
		_, _ = fmt.Fprintf(w, "  %s %-*s  %s\n", checkSymbol(c.Level, useColor), nameWidth, c.Name, c.Detail)
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "  "+doctorTally(ok, warn, fail))
}

// doctorTally formats the footer counts, omitting zero-count segments.
// Named doctorTally (not tally) to avoid a name collision with any future
// helper in render.go.
func doctorTally(ok, warn, fail int) string {
	var parts []string
	if ok > 0 {
		parts = append(parts, fmt.Sprintf("%d ok", ok))
	}
	if warn > 0 {
		label := "warning"
		if warn != 1 {
			label = "warnings"
		}
		parts = append(parts, fmt.Sprintf("%d %s", warn, label))
	}
	if fail > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", fail))
	}
	if len(parts) == 0 {
		return "no checks run"
	}
	return strings.Join(parts, " · ")
}
