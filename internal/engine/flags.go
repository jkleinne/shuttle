package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/log"
)

// rsyncInstrumentationFlags are injected by Shuttle into every rsync call for
// stats parsing and progress display. They are placed first so user-provided
// flags can override via last-flag-wins semantics.
var rsyncInstrumentationFlags = []string{"--stats", "--info=progress2"}

// rsyncInstrumentationKeys lists the flag prefixes Shuttle checks when warning
// about conflicts in rsync extra_flags.
var rsyncInstrumentationKeys = []string{"--stats", "--info=progress2"}

// rcloneInstrumentationKeys lists the flag prefixes Shuttle checks when warning
// about conflicts in rclone extra_flags.
var rcloneInstrumentationKeys = []string{"--stats", "--log-file", "--log-level"}

// RsyncArgsRequest carries the inputs for one rsync argument assembly.
// Grouped into a struct (mirroring RunnerConfig) so call sites name each
// input and the builder stays within the project's argument-count budget.
type RsyncArgsRequest struct {
	Defaults    *config.RsyncDefaults
	Job         config.Job
	Source      string
	Destination string
	IsDeleteDir bool // guards --delete-after; never set for single-file sources
	DryRun      bool
	LogFile     string
}

// BuildRsyncArgs assembles the full argument list for an rsync invocation.
// Order: instrumentation (lowest precedence) → default flags → per-job
// extra_flags → behavioral flags (delete, dry-run, log-file) → source → dest.
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
	if req.LogFile != "" {
		args = append(args, "--log-file="+req.LogFile)
	}

	// 5. Source and destination are always last.
	args = append(args, req.Source, req.Destination)

	return args
}

// RcloneArgsRequest carries the inputs for one rclone argument assembly.
type RcloneArgsRequest struct {
	Subcommand   string // rclone subcommand (config.ModeCopy or config.ModeSync spelling)
	Defaults     *config.RcloneDefaults
	Job          config.Job
	Source       string
	Destination  string
	DryRun       bool
	LogFile      string
	BackupDirArg string // pre-built --backup-dir value; empty omits the flag
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
	args = append(args, req.Subcommand, "--stats", "1s", "-P")
	if req.LogFile != "" {
		args = append(args, "--log-file", req.LogFile, "--log-level", "INFO")
	}

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
func WarnFlagConflicts(logger *log.Logger, engineName string, userFlags []string) {
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
