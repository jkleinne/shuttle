# Shuttle: Product Requirements Document

*Last updated: 2026-04-14*

---

## 1. Executive Summary

Shuttle is a personal backup and file synchronization CLI that orchestrates rsync (local syncs) and rclone (cloud uploads) through a single TOML configuration file. It replaces ad-hoc shell scripts with a structured pipeline that handles prerequisite checks, exclusive locking, partial failure recovery, and cloud archive retention. The tool is feature-complete and well-tested (~2,800 lines of Go, 51 tests, high coverage across all packages). It currently targets a single user (the author) running macOS with an external drive and encrypted rclone remotes.

The highest-priority remaining actions are: (1) add a `--color` override flag and structured logging option for scripted/cron usage, (2) set up CI and a release pipeline, and (3) decide whether Shuttle should remain a personal tool or grow into a community project, since that choice affects nearly every other decision (packaging, config schema stability, documentation depth, CI/CD).

---

## 2. Problem Statement and Target Audience

### The Problem

Managing personal backups across local drives and multiple cloud providers requires juggling rsync flags, rclone configurations, filter rules, and cleanup scripts. A typical setup involves:

- Multiple local sync jobs (media libraries, sensitive documents, project files)
- Multiple cloud remotes (Google Drive, Koofr, etc.), often encrypted
- Archive retention policies for safe cloud sync (keeping deleted files for N days)
- Bandwidth and performance tuning per provider
- Failure resilience: one broken source should not abort the entire backup run

Without a wrapper, this means maintaining fragile shell scripts that grow organically, lack error handling, and break silently when paths change or drives unmount.

### Target Audience

**Primary:** Technical users comfortable with CLI tools, TOML configuration, and rclone setup, who want a single command to run their entire backup pipeline.

**Not targeting:** Non-technical users, enterprise backup needs, team/multi-user environments, or real-time sync.

### User Personas

**Marcus, 34, software engineer with a home media server.** He has a 4TB external drive with manga, ebooks, audiobooks, and project archives. He syncs subsets to iCloud for mobile access and backs up everything to encrypted Google Drive and Koofr. Today he runs a 200-line bash script via cron. When a source path changed last month, the script silently skipped it. He wants a tool that validates paths upfront, continues past failures, and tells him exactly what happened.

**Lena, 28, freelance photographer.** She keeps RAW files on an external SSD and needs local copies on her laptop plus cloud backup to two providers for redundancy. She already uses rclone but finds the flag combinations hard to remember and worries about accidentally deleting cloud files during sync. She wants copy-vs-sync mode selection, automatic archive retention, and dry-run previews before committing.

---

## 3. Competitive Landscape

| Tool | What It Does | Where Shuttle Differs |
|------|-------------|----------------------|
| **Raw rsync + rclone** | Individual CLI tools | Shuttle orchestrates both in a single config-driven pipeline with partial failure handling, job selection, and summary reporting. |
| **Restic / BorgBackup** | Deduplicated, encrypted backup archives | These are snapshot-based backup tools. Shuttle does live sync (mirror), not incremental snapshots. Different mental model: Shuttle keeps a live copy, not versioned archives. |
| **Duplicati** | GUI-based backup with scheduling | GUI-oriented, heavier, targets less technical users. Shuttle is CLI-first, config-file-driven, composable with cron. |
| **Syncthing** | Real-time peer-to-peer file sync | Continuous sync daemon. Shuttle is batch-oriented (run once, sync everything, exit). Different use case: scheduled backup vs. live collaboration. |
| **Time Machine (macOS)** | Automatic local backup | Apple-only, opaque, no cloud support, no selective job control. Shuttle gives full control over what syncs where. |
| **Custom shell scripts** | Whatever you write | The direct competitor. Shuttle replaces fragile, unstructured scripts with validated config, error handling, stats parsing, and structured output. |

### Shuttle's Differentiation

1. **Unified config for two engines.** One TOML file describes both local rsync jobs and cloud rclone uploads, with shared path resolution and consistent error handling.
2. **Partial failure resilience.** One broken source does not abort the run. Failed items are logged and summarized; everything else completes.
3. **Archive-safe cloud sync.** Sync mode with `--backup-dir` moves deleted files to a timestamped archive on the remote, with configurable retention and automatic cleanup.
4. **Job selection at runtime.** `--skip`, `--only`, and `--remote` flags let you run subsets of the pipeline without editing config.
5. **Operational transparency.** Dual-stream logging (colored terminal + timestamped file), parsed transfer stats, and a structured summary at the end.

---

## 4. Feature Inventory

### Functional (Working as Intended)

| Feature | Description | Evidence |
|---------|-------------|----------|
| **Local rsync sync** | Sync multiple sources to a destination with archive mode, progress, and delete support | `internal/engine/rsync.go`, integration tests with real rsync |
| **Cloud rclone upload** | Copy or sync to any rclone remote with tuning parameters | `internal/engine/rclone.go` |
| **TOML configuration** | Full config parsing, tilde expansion, and structural validation | `internal/config/config.go`, 14 test cases |
| **Job selection** | `--skip`, `--only`, `--remote` flags for partial runs | `runner.go:shouldRunJob`, `runner.go:ValidateJobNames` |
| **Dry-run mode** | Preview changes without modifying files | Both executors inject `--dry-run` |
| **Exclusive locking** | Per-config flock at `<tempdir>/shuttle-<hash>.lock` prevents concurrent runs of the same config | `runner.go:acquireLock` |
| **Prerequisite checks** | Verifies rsync/rclone on PATH and filter file existence for active rclone jobs | `runner.go:checkPrerequisites` |
| **Stats parsing** | Extracts transfer stats from rsync stdout and rclone log files | `stats.go`, fixture-based tests |
| **Summary rendering** | Color-coded, status-first summary with rclone remote grouping, collapsing, and tally footer | `render.go:RenderSummary` |
| **Archive retention** | Moves deleted cloud files to timestamped backup dirs, purges old ones | `rclone.go:CleanupArchives` |
| **Dual-stream logging** | Colored terminal output + timestamped plain-text log file | `internal/log/logger.go` |
| **Signal handling** | Graceful shutdown on SIGINT/SIGTERM with exit code 130 | `cmd/shuttle/main.go` |
| **Password prompt** | Secure rclone config password input when not set via env var | `main.go:promptForPassword` |
| **XDG compliance** | Config, logs, and state follow XDG base directory spec | Config and log path resolution |
| **Rclone source handling** | Two-path resolution: rclone remote (contains `:`) or local path (absolute or tilde-expanded) | `engine/path.go:isRcloneRemote`, `engine/path.go:rcloneDestName` |
| **Rclone tuning** | Configurable transfers, checkers, bandwidth, chunk size, timeouts, etc. via `[defaults.rclone]` or per-job overrides | `[defaults.rclone]` config section, `flags.go:buildTuningFlags` |
| **Live progress display** | Brew-style spinner on the active job in interactive (TTY) mode; plain status lines in non-interactive mode (pipes, cron). Progress text is fed from `--info=progress2` (rsync) and `-P` (rclone) output via callbacks. | `internal/engine/progress.go`, `progress_test.go` |
| **Version command** | `shuttle version` with build-time injection | `main.go`, `-ldflags` |
| **Config validation** | `shuttle validate` loads and validates config without running jobs. Prints `config ok: <path>` on success, error details on failure. Exit 0/2. | `main.go:validateCmd` |
| **CI pipeline** | GitHub Actions: parallel test job (`go test -race ./...`, `go build ./...`) and lint job (`golangci-lint`, `govulncheck`). Triggers on push to main and PRs. | `.github/workflows/ci.yml` |

### Incomplete or Non-Functional

| Item | Status | Notes |
|------|--------|-------|
| **No install/release pipeline** | Missing | No Makefile, Goreleaser config, Homebrew formula, or release workflow. Users must `go install` or `go build` manually. |
| **No man page or `--help` beyond Cobra defaults** | Minimal | Cobra generates basic `--help` but there's no long description, examples section, or man page. |

### Not Yet Built (Absent but Potentially Needed)

| Feature | Priority | Rationale |
|---------|----------|-----------|
| **Scheduled execution** | Low | Shuttle is designed to be invoked by cron or launchd. Built-in scheduling would duplicate OS functionality. |
| **Parallel job execution** | Medium | All jobs run sequentially. For users with many independent jobs, parallel execution could cut total runtime significantly. |
| **Notifications** | Low | No post-run notification (email, webhook, macOS notification). Users must check terminal or log file. |
| ~~**Config validation command**~~ | ~~Medium~~ | **Resolved.** `shuttle validate` parses and validates config, reports errors or prints `config ok: <path>`. |
| **Structured output (JSON)** | Low | Summary is human-readable text only. JSON output would enable scripting and monitoring integration. |
| **Overall run progress indicator** | Low | No "Job 3 of 7" counter across the whole run. Each job shows live per-job progress via the ProgressWriter spinner. |
| **Rclone executor tests** | Medium | Integration tests exist for rsync but not rclone. Would require test remotes or a local rclone backend. |
| **`--color` / `--no-color` flag** | Low | Color is auto-detected. No manual override for edge cases (piped output with forced color, etc.). |

---

## 5. Gap Analysis

### Critical Gaps (Launch Blockers)

*"Launch" here means making the tool available to other users, not personal use (which already works).*

| Gap | Severity | Description |
|-----|----------|-------------|
| ~~**No README**~~ | ~~Critical~~ | **Resolved.** `README.md` covers installation, quick start, full configuration reference, CLI usage, exit codes, and platform support. |
| **No release/install mechanism** | Critical | No binary releases, no Homebrew formula, no Goreleaser. Users can `go install` but there are no prebuilt binaries. |
| ~~**No license file**~~ | ~~Critical~~ | **Resolved.** MIT license added. |

### Integrity Gaps (Misleading or Deceptive Behavior)

| Gap | Severity | Description |
|-----|----------|-------------|
| **`shuttle` (no args) silently does `run`** | Low | The root command defaults to `run`. This is intentional but could surprise users who expect `--help` from a bare invocation. Cobra's default behavior. |

### Quality Gaps (Fix Before or Shortly After Launch)

| Gap | Severity | Description |
|-----|----------|-------------|
| ~~**No config validation subcommand**~~ | ~~Medium~~ | **Resolved.** `shuttle validate` subcommand added. |
| **No CLI integration tests** | Medium | `main.go` is untested. Signal handling, flag parsing, and password prompt flow are manual-test-only. |
| **No rclone executor integration tests** | Medium | Rsync executor has integration tests with real files. Rclone progress parsing (`scanRcloneProgress`) is now tested, but full execution against a real rclone backend is not. |
| **No man page** | Low | `shuttle --help` works but a man page would be expected for a Unix tool. |

---

## 6. Recommended MVP Scope

*MVP defined as: "ready for other people to install, configure, and use without reading source code."*

### Phase 1: Ship (Launch Blockers)

| Item | Effort | Description |
|------|--------|-------------|
| ~~Add LICENSE file~~ | ~~Small~~ | **Done.** MIT license. |
| ~~Write README.md~~ | ~~Medium~~ | **Done.** Covers installation, quick start, configuration reference, CLI usage, platform support. |
| Add Goreleaser config | Medium | Automated binary builds for macOS (arm64, amd64) and Linux. GitHub Releases integration. |
| ~~Add `shuttle validate` subcommand~~ | ~~Small~~ | **Done.** Parses and validates config, prints errors or `config ok: <path>`, exits 0/2. |

### Phase 2: Harden (First Week Post-Launch)

| Item | Effort | Description |
|------|--------|-------------|
| CLI integration tests | Medium | Test flag parsing, exit codes, and error paths using `exec.Command` to invoke the built binary. |
| Rclone executor integration tests | Medium | Test against rclone's `local` backend (no remote needed). Verify flag construction, mode selection, archive cleanup. Progress parsing is already tested. |
| `--color` / `--no-color` flag | Small | Override auto-detection for scripted and piped usage. |
| Add `--verbose` / `--quiet` flags | Small | Control output verbosity. Quiet mode for cron (errors only). |
| ~~GitHub Actions CI~~ | ~~Small~~ | **Done.** Two parallel jobs: test (`go test -race`, `go build`) and lint (`golangci-lint`, `govulncheck`). Triggers on push to main and PRs. |

### Defer (Valuable but Not Launch-Critical)

| Item | Effort | Description |
|------|--------|-------------|
| Parallel job execution | Large | Run independent sync jobs concurrently. Requires careful output interleaving and error aggregation. |
| JSON output mode | Medium | `--output json` for scripting and monitoring integration. |
| Notifications (webhook/email) | Medium | Post-run notification with summary. |
| Homebrew formula | Small | After Goreleaser is set up, publish a tap. |
| Man page generation | Small | Use Cobra's built-in man page generator. |

### Cut (Remove or Rethink)

| Item | Rationale |
|------|-----------|
| Built-in scheduling | Cron and launchd exist. Adding a scheduler would duplicate OS functionality and create a daemon management burden. Provide a launchd plist / cron example in docs instead. |
| GUI or TUI | The tool's value is in being scriptable and config-driven. A TUI would add complexity without serving the target audience. |
| Multi-user / team features | This is a personal backup tool. Adding user management, permissions, or shared configs would change the product category entirely. |
| Windows support | The tool uses `syscall.Flock` (Unix-only) and assumes Unix path semantics. Supporting Windows would require significant platform abstraction for unclear demand. |

---

## 7. Success Metrics

| Metric | Measurement Method | Target | Rationale |
|--------|-------------------|--------|-----------|
| **Successful run rate** | Parse exit codes from cron/launchd logs over 30 days | > 95% exit code 0 | A backup tool that fails frequently isn't trustworthy. 5% tolerance accounts for transient network issues and unmounted drives. |
| **Mean run duration** | Parse timestamps from log files (start vs. end) | < 2x manual rsync+rclone time | Shuttle adds overhead (prerequisite checks, stats parsing, locking). Should not more than double the raw tool runtime. Baseline: time individual rsync/rclone commands and compare. |
| **Recovery from partial failure** | Count runs where exit code = 1 AND subsequent run completes the failed items | 100% of partial failures recovered on next run | rsync and rclone are idempotent. A failed source should succeed on the next run if the underlying issue (unmounted drive, network) is resolved. |
| **GitHub stars** (if published) | GitHub API | 50 in first 6 months | Comparable Go CLI tools in the backup space (e.g., `restic` started small, `rclone` itself is much larger). 50 stars indicates some organic discovery and interest. |
| **Config error rate** | Count exit code 2 runs / total runs | < 5% after initial setup | After a user configures Shuttle correctly, config errors should be rare. High rates indicate confusing config schema or poor validation messages. |
| **Archive cleanup success** | Check that archive directories older than retention are actually purged | 100% compliance with retention policy | The archive feature is a trust mechanism. If old archives aren't cleaned up, disk/cloud usage grows unboundedly. |

---

## 8. Key Assumptions and Risks

### Assumptions

| Assumption | Impact If Wrong | Mitigation |
|------------|-----------------|------------|
| **Users already have rsync and rclone installed and configured.** | If users expect Shuttle to handle rclone remote setup, the prerequisite check will confuse them. | Document rclone setup as a prerequisite in README. Link to rclone docs. Consider a `shuttle doctor` command that checks setup and provides guidance. |
| **TOML is an acceptable config format for the target audience.** | If users prefer YAML or JSON, they'll skip Shuttle. | TOML is standard in the Go ecosystem (Hugo, CUE, etc.) and well-suited to this config shape. Low risk. |
| **Sequential job execution is fast enough.** | If total runtime grows beyond ~30 minutes, users may want parallelism. | Monitor run duration. Parallel execution is in the Defer backlog. Most personal backup sets complete in under 15 minutes. |
| **macOS is the primary platform.** | macOS-specific rsync flags (e.g. `-E` for extended attributes) are not in the example config defaults but may appear in user configs. | The example config uses platform-neutral defaults. macOS-specific flags are documented as comments. |
| **Single config file is sufficient.** | Users who want different configs for different contexts (work vs. personal, daily vs. weekly) must use `XDG_CONFIG_HOME` overrides. | This works today (`XDG_CONFIG_HOME=/alt/path shuttle`). Document the pattern. |

### Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **rsync or rclone output format changes break stats parsing** | Low | Medium. Stats display breaks but sync itself still works. | Pin to known output formats. Stats parsing already handles missing fields gracefully. Add regression tests against multiple rsync/rclone versions. |
| **Rclone encrypted config password handling is insecure** | Low | High. Password set via environment variable is visible in `/proc/environ` on Linux. | This is rclone's standard mechanism. Document the risk. Consider supporting rclone's `--password-command` flag as an alternative. |
| **Lock file is not cleaned up on crash** | Low | Low. Flock is automatically released when the process exits, even on crash. The lock file itself persists but is harmless (flock checks the lock state, not file existence). | No mitigation needed. This is correct flock behavior. Lock path uses `os.TempDir()` for portability. |
| **Cloud sync with `--delete` + no backup-dir causes data loss** | Medium | High. If a user sets `mode = "sync"` and leaves `backup_path` empty, deleted local files are permanently removed from the remote. | The current design allows this. Consider requiring `backup_path` when mode is "sync", or at minimum printing a loud warning. |
| **No automated testing in CI** | High (current state) | Medium. Regressions can be introduced without detection. | Add GitHub Actions CI (Phase 2). |
| **Project stalls as a single-maintainer personal tool** | Medium | Low for the author, high for potential adopters. | Decide on the project's scope early. If community adoption is a goal, invest in docs, CI, and release automation. If not, the tool works as-is for personal use. |

---

## Appendix: Contradictions and Notes

### macOS-Specific Rsync Flags

The example config (`config.example.toml`) uses platform-neutral defaults. macOS-specific options (`.DS_Store` exclusion) are documented as comments. The binary hardcodes no flags; all flags come from `[defaults.rsync]` or per-job `extra_flags`. macOS users who want `-E` (extended attributes) should add it to their own config.

**File:** `config.example.toml` (`[defaults.rsync]` flags)
