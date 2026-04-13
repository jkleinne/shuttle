package engine

import (
	"fmt"
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

// BuildRsyncArgs assembles the full argument list for an rsync invocation.
// Order: instrumentation (lowest precedence) → default flags → per-job
// extra_flags → behavioral flags (delete, dry-run, log-file) → source → dest.
//
// Instrumentation flags come first so user flags can override via last-flag-wins
// rsync semantics. isDeleteDir guards --delete-after so it is never applied when
// the source is a single file.
func BuildRsyncArgs(defaults *config.RsyncDefaults, job config.Job, source, destination string, isDeleteDir, dryRun bool, logFile string) []string {
	args := make([]string, 0, 20)

	// 1. Instrumentation (lowest precedence; user flags may shadow them).
	args = append(args, rsyncInstrumentationFlags...)

	// 2. Default flags from [defaults.rsync].
	if defaults != nil {
		args = append(args, defaults.Flags...)
	}

	// 3. Per-job extra_flags.
	args = append(args, job.ExtraFlags...)

	// 4. Behavioral flags.
	if job.Delete && isDeleteDir {
		args = append(args, "--delete-after")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if logFile != "" {
		args = append(args, "--log-file="+logFile)
	}

	// 5. Source and destination are always last.
	args = append(args, source, destination)

	return args
}

// BuildRcloneArgs assembles the full argument list for an rclone invocation.
// Order: subcommand → instrumentation → default flags → default tuning →
// per-job extra_flags → per-job tuning overrides → filter-from → backup-dir →
// dry-run → source → dest.
//
// Per-job tuning overrides appear after default tuning so rclone's last-flag-wins
// behaviour applies. backupDirArg is the pre-built "--backup-dir" value (or empty
// to omit it); the caller is responsible for constructing this string.
func BuildRcloneArgs(subcommand string, defaults *config.RcloneDefaults, job config.Job, source, destination string, isDir, dryRun bool, logFile string, backupDirArg string) []string {
	args := make([]string, 0, 40)

	// The subcommand (copy/sync) is always first.
	args = append(args, subcommand)

	// 1. Instrumentation.
	args = append(args, "--stats", "1s", "-P")
	if logFile != "" {
		args = append(args, "--log-file", logFile, "--log-level", "INFO")
	}

	// 2. Default flags from [defaults.rclone].
	if defaults != nil {
		args = append(args, defaults.Flags...)
	}

	// 3. Default tuning from [defaults.rclone] tuning fields.
	if defaults != nil {
		args = append(args, buildTuningFlags(defaults)...)
	}

	// 4. Per-job extra_flags.
	args = append(args, job.ExtraFlags...)

	// 5. Per-job tuning overrides (applied after defaults; last-flag-wins).
	args = append(args, buildJobTuningFlags(job)...)

	// 6. Filter file: job-level overrides default.
	filterFile := ""
	if defaults != nil {
		filterFile = defaults.FilterFile
	}
	if job.FilterFile != "" {
		filterFile = job.FilterFile
	}
	if filterFile != "" {
		args = append(args, "--filter-from", filterFile)
	}

	// 7. Backup dir (pre-built by caller; empty means omit).
	if backupDirArg != "" {
		args = append(args, "--backup-dir", backupDirArg)
	}

	// 8. Dry run.
	if dryRun {
		args = append(args, "--dry-run")
	}

	// 9. Source and destination are always last.
	args = append(args, source, destination)

	return args
}

// WarnFlagConflicts logs a warning for each user-provided flag that overlaps with
// Shuttle's instrumentation flags. Shuttle injects instrumentation flags to enable
// stats capture and progress display; user flags that duplicate them may produce
// unexpected output or break stats parsing.
//
// engineName must be "rsync" or "rclone". userFlags are the extra_flags values
// from the job config.
func WarnFlagConflicts(logger *log.Logger, engineName string, userFlags []string) {
	var keys []string
	if engineName == "rsync" {
		keys = rsyncInstrumentationKeys
	} else {
		keys = rcloneInstrumentationKeys
	}

	for _, flag := range userFlags {
		for _, key := range keys {
			if flag == key || strings.HasPrefix(flag, key+"=") || strings.HasPrefix(flag, key+" ") {
				logger.Warn(fmt.Sprintf(
					"flag %q conflicts with Shuttle's instrumentation; stats capture may be affected",
					flag,
				))
			}
		}
	}
}

// buildTuningFlags translates RcloneDefaults tuning fields into rclone flag
// strings. Zero-value fields (0, "", false) produce no output.
func buildTuningFlags(d *config.RcloneDefaults) []string {
	var flags []string
	if d.Transfers > 0 {
		flags = append(flags, "--transfers", fmt.Sprintf("%d", d.Transfers))
	}
	if d.Checkers > 0 {
		flags = append(flags, "--checkers", fmt.Sprintf("%d", d.Checkers))
	}
	if d.Bwlimit != "" {
		flags = append(flags, "--bwlimit", d.Bwlimit)
	}
	if d.DriveChunkSize != "" {
		flags = append(flags, "--drive-chunk-size", d.DriveChunkSize)
	}
	if d.BufferSize != "" {
		flags = append(flags, "--buffer-size", d.BufferSize)
	}
	if d.UseMmap {
		flags = append(flags, "--use-mmap")
	}
	if d.Timeout != "" {
		flags = append(flags, "--timeout", d.Timeout)
	}
	if d.Contimeout != "" {
		flags = append(flags, "--contimeout", d.Contimeout)
	}
	if d.LowLevelRetries > 0 {
		flags = append(flags, "--low-level-retries", fmt.Sprintf("%d", d.LowLevelRetries))
	}
	if d.OrderBy != "" {
		flags = append(flags, "--order-by", d.OrderBy)
	}
	return flags
}

// buildJobTuningFlags translates per-job tuning override fields into rclone
// flags. Only non-zero fields produce output, so unset overrides do not shadow
// the defaults.
func buildJobTuningFlags(job config.Job) []string {
	var flags []string
	if job.Transfers > 0 {
		flags = append(flags, "--transfers", fmt.Sprintf("%d", job.Transfers))
	}
	if job.Checkers > 0 {
		flags = append(flags, "--checkers", fmt.Sprintf("%d", job.Checkers))
	}
	if job.Bwlimit != "" {
		flags = append(flags, "--bwlimit", job.Bwlimit)
	}
	if job.DriveChunkSize != "" {
		flags = append(flags, "--drive-chunk-size", job.DriveChunkSize)
	}
	if job.BufferSize != "" {
		flags = append(flags, "--buffer-size", job.BufferSize)
	}
	if job.UseMmap {
		flags = append(flags, "--use-mmap")
	}
	if job.Timeout != "" {
		flags = append(flags, "--timeout", job.Timeout)
	}
	if job.Contimeout != "" {
		flags = append(flags, "--contimeout", job.Contimeout)
	}
	if job.LowLevelRetries > 0 {
		flags = append(flags, "--low-level-retries", fmt.Sprintf("%d", job.LowLevelRetries))
	}
	if job.OrderBy != "" {
		flags = append(flags, "--order-by", job.OrderBy)
	}
	return flags
}
