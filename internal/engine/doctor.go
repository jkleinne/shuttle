package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"github.com/jkleinne/shuttle/internal/config"
)

// symbolWarn is the doctor-only warning glyph. The ✓/✗ glyphs are shared with
// render.go via symbolOK/symbolFailed; WARN has no analog there because the
// sync Status type has no warning state.
const symbolWarn = "⚠"

// checkNameConfig is the fixed label for the config check. Extracted as a
// constant to satisfy goconst (the string appears in multiple CheckResult
// literals) and to make future refactors consistent.
const checkNameConfig = "config"

// checkNameRemote is the fixed label for per-remote check results.
const checkNameRemote = "remote"

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

// ConfigStatus is the resolved config-load outcome handed to Diagnose. Config
// file reads happen at the cmd boundary (matching run/validate); Diagnose only
// classifies the outcome. Grouped into a struct so Diagnose stays under the
// project's 3-parameter threshold.
type ConfigStatus struct {
	Path     string         // resolved config path, for display
	Cfg      *config.Config // nil when LoadErr != nil
	LoadErr  error          // nil on success; errors.Is(_, fs.ErrNotExist) marks "missing"
	Explicit bool           // path supplied via --config or $SHUTTLE_CONFIG
}

// Diagnose runs all checks and returns the report. It never returns an error;
// every problem is recorded as a CheckResult. ctx cancels the (fast, local)
// external-tool invocations on signal.
func Diagnose(ctx context.Context, cs ConfigStatus) Report {
	usesRsync, usesRclone := enginesUsed(cs.Cfg)

	// Absence severity: FAIL when the loaded config uses that engine (the tool
	// is required), WARN when not (tool is missing but not needed right now).
	rsyncAbsent := CheckWarn
	if usesRsync {
		rsyncAbsent = CheckFail
	}
	rcloneAbsent := CheckWarn
	if usesRclone {
		rcloneAbsent = CheckFail
	}

	checks := []CheckResult{
		toolCheck(ctx, "rsync", rsyncAbsent, rsyncVersion),
		toolCheck(ctx, "rclone", rcloneAbsent, rcloneVersion),
		classifyConfig(cs),
	}
	if cs.Cfg != nil {
		checks = append(checks, remoteChecks(ctx, cs.Cfg)...)
		checks = append(checks, filterFileChecks(cs.Cfg)...)
	}
	return Report{Checks: checks}
}

// enginesUsed reports whether the loaded config has any rsync / rclone jobs.
// Both are false when cfg is nil (config failed to load), so a missing tool is
// then a WARN rather than a FAIL.
func enginesUsed(cfg *config.Config) (usesRsync, usesRclone bool) {
	if cfg == nil {
		return false, false
	}
	for _, j := range cfg.Jobs {
		switch j.Engine {
		case config.EngineRsync:
			usesRsync = true
		case config.EngineRclone:
			usesRclone = true
		}
	}
	return usesRsync, usesRclone
}

// classifyConfig turns the config-load outcome into a single check. Pure logic
// over ConfigStatus (no I/O).
func classifyConfig(cs ConfigStatus) CheckResult {
	switch {
	case cs.LoadErr == nil:
		return CheckResult{Name: checkNameConfig, Level: CheckOK, Detail: cs.Path}
	case errors.Is(cs.LoadErr, fs.ErrNotExist):
		if cs.Explicit {
			return CheckResult{Name: checkNameConfig, Level: CheckFail, Detail: cs.Path + " — not found"}
		}
		return CheckResult{Name: checkNameConfig, Level: CheckWarn, Detail: cs.Path + " — no config file (create one, then `shuttle validate`)"}
	default:
		return CheckResult{Name: checkNameConfig, Level: CheckFail, Detail: oneLine(cs.LoadErr.Error())}
	}
}

// toolCheck reports whether an external tool is on PATH. absentLevel is the
// severity to use when the tool is missing (CheckFail when the loaded config
// uses this engine, CheckWarn when it does not). When present, the version
// func supplies the detail.
func toolCheck(ctx context.Context, name string, absentLevel CheckLevel, version func(context.Context) string) CheckResult {
	if _, err := exec.LookPath(name); err != nil {
		detail := "not found on PATH"
		if absentLevel == CheckWarn {
			detail = "not found on PATH (no " + name + " jobs configured)"
		}
		return CheckResult{Name: name, Level: absentLevel, Detail: detail}
	}
	return CheckResult{Name: name, Level: CheckOK, Detail: version(ctx)}
}

// rsyncVersion returns the rsync version token, or "installed" if it cannot be
// determined. Example first line: "rsync  version 3.2.7  protocol version 31".
func rsyncVersion(ctx context.Context) string {
	line, err := commandFirstLine(ctx, "rsync", "--version")
	if err != nil {
		return "installed"
	}
	fields := strings.Fields(line)
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return line
}

// rcloneVersion returns the rclone version token, or "installed" if it cannot
// be determined. Example first line: "rclone v1.66.0".
func rcloneVersion(ctx context.Context) string {
	line, err := commandFirstLine(ctx, "rclone", "version")
	if err != nil {
		return "installed"
	}
	if fields := strings.Fields(line); len(fields) >= 2 {
		return fields[1]
	}
	return line
}

// commandFirstLine runs name+args and returns the trimmed first line of stdout.
func commandFirstLine(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return oneLine(string(out)), nil
}

// oneLine returns the first line of s, trimmed. Used to keep check details to a
// single line (and to avoid dumping multi-line tool output).
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// remoteChecks verifies each configured rclone remote name exists in the user's
// rclone config. Skipped when there are no rclone remotes or rclone is absent.
// When the remote list cannot be read (e.g. an encrypted config without
// RCLONE_CONFIG_PASS), a single WARN is returned and per-remote checks are
// skipped — a fixable setup gap, not a hard failure.
func remoteChecks(ctx context.Context, cfg *config.Config) []CheckResult {
	names := cfg.AllRemoteNames()
	if len(names) == 0 {
		return nil
	}
	if _, err := exec.LookPath("rclone"); err != nil {
		return nil // rclone-missing is already reported by toolCheck
	}
	defined, err := listRcloneRemotes(ctx)
	if err != nil {
		return []CheckResult{{
			Name:   "remotes",
			Level:  CheckWarn,
			Detail: "could not read rclone remotes (encrypted config? supply the config password)",
		}}
	}
	checks := make([]CheckResult, 0, len(names))
	for _, n := range names {
		if defined[n] {
			checks = append(checks, CheckResult{Name: checkNameRemote, Level: CheckOK, Detail: n})
		} else {
			checks = append(checks, CheckResult{Name: checkNameRemote, Level: CheckFail, Detail: n + " — not found in rclone config"})
		}
	}
	return checks
}

// listRcloneRemotes returns the set of remote names defined in the user's rclone
// config. --ask-password=false makes rclone fail fast instead of prompting on
// stdin when the config is encrypted and no password is available.
func listRcloneRemotes(ctx context.Context) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "rclone", "listremotes", "--ask-password=false").Output()
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		// listremotes prints one "name:" per line.
		name := strings.TrimSuffix(strings.TrimSpace(line), ":")
		if name != "" {
			set[name] = true
		}
	}
	return set, nil
}

// filterFileChecks stats each unique rclone filter file referenced by the config.
func filterFileChecks(cfg *config.Config) []CheckResult {
	var checks []CheckResult
	for _, ff := range rcloneFilterFiles(cfg) {
		if _, err := os.Stat(ff); err != nil {
			checks = append(checks, CheckResult{Name: "filter file", Level: CheckFail, Detail: ff + " — not found"})
		} else {
			checks = append(checks, CheckResult{Name: "filter file", Level: CheckOK, Detail: ff})
		}
	}
	return checks
}

// rcloneFilterFiles returns the deduplicated filter-file paths referenced by all
// rclone jobs (default plus per-job overrides), in first-seen order. Unlike
// Runner.collectFilterFiles this is not run-filter-aware: doctor checks every
// configured job.
func rcloneFilterFiles(cfg *config.Config) []string {
	defaultFilter := ""
	if cfg.Defaults != nil && cfg.Defaults.Rclone != nil {
		defaultFilter = cfg.Defaults.Rclone.FilterFile
	}
	seen := make(map[string]bool)
	var files []string
	for _, job := range cfg.Jobs {
		if job.Engine != config.EngineRclone {
			continue
		}
		ff := job.FilterFile
		if ff == "" {
			ff = defaultFilter
		}
		if ff != "" && !seen[ff] {
			seen[ff] = true
			files = append(files, ff)
		}
	}
	return files
}
