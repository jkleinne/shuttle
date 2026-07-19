package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jkleinne/shuttle/internal/config"
)

// WarningLogger lets flag conflict reporting remain independent of logger storage.
type WarningLogger interface {
	// Warn surfaces argument conflicts without coupling flag assembly to a concrete logger.
	Warn(string)
}

// rsyncInstrumentationFlags are injected by Shuttle into every rsync call for
// stats parsing and progress display. They are placed first so user-provided
// flags can override via last-flag-wins semantics.
const (
	rsyncInfoProgressFlag = "--info=progress2"
	rsyncOutFormatFlag    = "--out-format=%i %n%L"
	rcloneUseJSONLogFlag  = "--use-json-log"
	transferStatsFlag     = "--stats"
)

var rsyncInstrumentationFlags = []string{
	transferStatsFlag,
	rsyncInfoProgressFlag,
	rsyncOutFormatFlag,
}

// rsyncInstrumentationKeys lists the flag prefixes Shuttle checks when warning
// about conflicts in rsync extra_flags.
var rsyncInstrumentationKeys = []string{
	transferStatsFlag,
	rsyncInfoProgressFlag,
	"--out-format",
	"--log-format",
}

// rcloneInstrumentationKeys lists the flag prefixes Shuttle checks when warning
// about conflicts in rclone extra_flags.
var rcloneInstrumentationKeys = []string{
	transferStatsFlag,
	rcloneUseJSONLogFlag,
	"--log-level",
	"--log-file",
}

// RsyncArgsRequest carries the inputs for one rsync argument assembly.
// Grouped into a struct (mirroring RunnerConfig) so call sites name each
// input and the builder stays within the project's argument-count budget.
type RsyncArgsRequest struct {
	// Defaults supplies baseline flags before job-level values apply.
	Defaults *config.RsyncDefaults
	// Job supplies the behavior and overrides selected for this invocation.
	Job config.Job
	// Source remains a distinct final argument so user-controlled paths never enter a shell.
	Source string
	// Destination remains a distinct final argument so user-controlled paths never enter a shell.
	Destination string
	// IsDeleteDir prevents directory deletion semantics from reaching single-file sources.
	IsDeleteDir bool
	// DryRun requests rsync's non-mutating execution mode at the boundary.
	DryRun bool
}

// BuildRsyncArgs assembles the full argument list for an rsync invocation.
// Order: instrumentation (lowest precedence) → default flags → per-job
// extra_flags → behavioral flags (delete, dry-run) → source → dest.
//
// Instrumentation flags come first so user flags can override via last-flag-wins
// rsync semantics.
func BuildRsyncArgs(req RsyncArgsRequest) []string {
	args := make([]string, 0, 20)

	// 1. Instrumentation (lowest precedence; user flags may shadow them).
	args = append(args, rsyncInstrumentationFlags...)

	// 2. Default flags from [defaults.rsync].
	if req.Defaults != nil {
		args = append(args, req.Defaults.Flags...)
	}

	// 3. Per-job extra_flags.
	args = append(args, req.Job.ExtraFlags...)

	// 4. Behavioral flags.
	if req.Job.Delete && req.IsDeleteDir {
		args = append(args, "--delete-after")
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	// 5. Source and destination are always last.
	args = append(args, req.Source, req.Destination)

	return args
}

// RcloneArgsRequest carries the inputs for one rclone argument assembly.
type RcloneArgsRequest struct {
	// Subcommand carries the validated copy or sync mode selected for this invocation.
	Subcommand string
	// Defaults supplies baseline flags and tuning before job-level values apply.
	Defaults *config.RcloneDefaults
	// Job supplies the behavior, filter, and tuning overrides selected for this invocation.
	Job config.Job
	// Source remains a distinct final argument so user-controlled paths never enter a shell.
	Source string
	// Destination remains a distinct final argument so user-controlled paths never enter a shell.
	Destination string
	// DryRun requests rclone's non-mutating execution mode at the boundary.
	DryRun bool
	// BackupDirArg carries the pre-built retention target, with an empty value omitting the flag.
	BackupDirArg string
}

// BuildRcloneArgs assembles the full argument list for an rclone invocation.
// Order: subcommand → instrumentation → default flags → default tuning →
// per-job extra_flags → per-job tuning overrides → filter-from → backup-dir →
// dry-run → source → dest.
//
// Per-job tuning overrides appear after default tuning so rclone's last-flag-wins
// behaviour applies. The caller is responsible for constructing BackupDirArg.
func BuildRcloneArgs(req RcloneArgsRequest) []string {
	args := make([]string, 0, 40)

	// The subcommand (copy/sync) is always first, immediately followed by
	// instrumentation flags (step 1).
	args = append(
		args,
		req.Subcommand,
		transferStatsFlag,
		"1s",
		"-P",
		rcloneUseJSONLogFlag,
		"--log-level",
		"INFO",
	)

	// 2. Default flags from [defaults.rclone].
	if req.Defaults != nil {
		args = append(args, req.Defaults.Flags...)
	}

	// 3. Default tuning from [defaults.rclone] tuning fields.
	if req.Defaults != nil {
		args = append(args, buildTuningFlags(req.Defaults.RcloneTuning)...)
	}

	// 4. Per-job extra_flags.
	args = append(args, req.Job.ExtraFlags...)

	// 5. Per-job tuning overrides (applied after defaults; last-flag-wins).
	args = append(args, buildTuningFlags(req.Job.RcloneTuning)...)

	// 6. Filter file: job-level overrides default.
	filterFile := ""
	if req.Defaults != nil {
		filterFile = req.Defaults.FilterFile
	}
	if req.Job.FilterFile != "" {
		filterFile = req.Job.FilterFile
	}
	if filterFile != "" {
		args = append(args, "--filter-from", filterFile)
	}

	// 7. Backup dir (pre-built by caller; empty means omit).
	if req.BackupDirArg != "" {
		args = append(args, "--backup-dir", req.BackupDirArg)
	}

	// 8. Dry run.
	if req.DryRun {
		args = append(args, "--dry-run")
	}

	// 9. Source and destination are always last.
	args = append(args, req.Source, req.Destination)

	return args
}

// WarnFlagConflicts logs a warning for each user-provided flag that overlaps with
// Shuttle's instrumentation flags. Shuttle injects instrumentation flags to enable
// stats capture and progress display; user flags that duplicate them may produce
// unexpected output or break stats parsing.
//
// engineName is config.EngineRsync or config.EngineRclone; anything else warns about nothing. userFlags are the extra_flags values
// from the job config.
func WarnFlagConflicts(logger WarningLogger, engineName string, userFlags []string) {
	var keys []string
	switch engineName {
	case config.EngineRsync:
		keys = rsyncInstrumentationKeys
	case config.EngineRclone:
		keys = rcloneInstrumentationKeys
	default:
		return // unknown engine: no instrumentation keys to conflict with
	}

	for _, flag := range userFlags {
		for _, key := range keys {
			if flag == key || strings.HasPrefix(flag, key+"=") {
				logger.Warn(fmt.Sprintf(
					"flag %q conflicts with Shuttle's instrumentation; stats capture may be affected",
					flag,
				))
			}
		}
	}
}

// buildTuningFlags translates RcloneTuning fields into rclone flag strings.
// Zero-value fields (0, "", false) produce no output, so an unset override
// never shadows a default. Called twice by BuildRcloneArgs — defaults first,
// then the job's overrides — so rclone's last-flag-wins applies.
func buildTuningFlags(t config.RcloneTuning) []string {
	var flags []string
	if t.Transfers > 0 {
		flags = append(flags, "--transfers", strconv.Itoa(t.Transfers))
	}
	if t.Checkers > 0 {
		flags = append(flags, "--checkers", strconv.Itoa(t.Checkers))
	}
	if t.Bwlimit != "" {
		flags = append(flags, "--bwlimit", t.Bwlimit)
	}
	if t.DriveChunkSize != "" {
		flags = append(flags, "--drive-chunk-size", t.DriveChunkSize)
	}
	if t.BufferSize != "" {
		flags = append(flags, "--buffer-size", t.BufferSize)
	}
	if t.UseMmap {
		flags = append(flags, "--use-mmap")
	}
	if t.Timeout != "" {
		flags = append(flags, "--timeout", t.Timeout)
	}
	if t.Contimeout != "" {
		flags = append(flags, "--contimeout", t.Contimeout)
	}
	if t.LowLevelRetries > 0 {
		flags = append(flags, "--low-level-retries", strconv.Itoa(t.LowLevelRetries))
	}
	if t.OrderBy != "" {
		flags = append(flags, "--order-by", t.OrderBy)
	}
	return flags
}
