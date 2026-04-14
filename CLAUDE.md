# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Personal backup and synchronization CLI. Orchestrates rsync (local syncs) and rclone (cloud uploads) via `os/exec`.

## Commands

    go build -o shuttle ./cmd/shuttle
    go test ./...
    go test ./internal/config/              # single package
    go test ./internal/engine/ -run Stats   # single test or pattern
    go build -ldflags "-X main.version=1.0" -o shuttle ./cmd/shuttle   # inject version
    golangci-lint run ./...                 # lint (config in .golangci.yml)
    ./shuttle validate                      # check config without running jobs

## Architecture

Four packages, strict layering: CLI shell (`cmd/shuttle`) calls engine, engine uses config and log. No package imports sideways or upward.

- `cmd/shuttle/main.go` — Cobra CLI, signal handling, exit codes, password prompt. All `os.Exit` lives here. Maps engine errors to exit codes: `errPartialFailure` → 1, other errors → 2, signal → 130.
- `internal/config/` — TOML parsing via `go-toml/v2`, tilde expansion, validation. Public API: `Load`, `LoadFile`, `LoadBytes`, `ConfigPath`. Tilde expansion runs once immediately after parsing, before validation.
- `internal/engine/` — the sync pipeline. Runner orchestrates: check prerequisites, acquire per-config flock, dispatch jobs in config order by engine type. `flags.go` assembles all CLI argument lists. Executors (`RsyncExecutor`, `RcloneExecutor`) accept pre-built arg slices and shell out via `os/exec`. `render.go` handles the color-coded summary display with rclone remote grouping and collapsing.
- `internal/log/` — dual-stream logger: colored terminal output + timestamped plain-text log file.

## Execution pipeline

`Runner.Run` follows a fixed sequence: prerequisites check (tool availability, filter file existence) → exclusive per-config flock at `<tempdir>/shuttle-<hash>.lock` → jobs in config order, dispatched by `job.Engine` (`"rsync"` or `"rclone"`). For rclone jobs, the runner iterates the job's `remotes` list and runs the job once per target remote. Partial failures are recorded but don't stop subsequent jobs.

## Unified job model

All sync operations are defined as `[[job]]` entries. Each job carries a `name`, an `engine` field (`"rsync"` or `"rclone"`), and engine-specific fields. There is no separate `[cloud]` section or `[[sync]]` array. Rclone jobs expand over their `remotes` list at runtime; `--remote` filters this list. Config validation enforces: job names unique and non-empty, engine must be `"rsync"` or `"rclone"`, rsync jobs require at least one source and a destination, rclone jobs require a source, at least one remote, and a valid mode (`"copy"` or `"sync"`).

## Defaults and flag assembly

`[defaults.rsync]` and `[defaults.rclone]` sections provide baseline flags and tuning for all jobs of the corresponding engine. No flags are hardcoded in the binary. `flags.go` exports `BuildRsyncArgs` and `BuildRcloneArgs`, which assemble the full argument list in a defined precedence order: instrumentation flags first (lowest precedence, can be overridden) → default flags → per-job `extra_flags` → behavioral flags (delete, dry-run, log-file) → source/destination. For rclone, default tuning fields appear before per-job tuning overrides so last-flag-wins applies. `WarnFlagConflicts` logs a warning when user flags overlap with Shuttle's instrumentation flags.

## Rclone mode selection

`selectMode` in `rclone.go` chooses the rclone subcommand. Copy is used when `mode = "copy"` or when the source is a file (rclone sync requires a directory). Sync mode with a configured `backup_path` constructs `--backup-dir` as `remote:<backup_path>/<timestamp>/<dest_subpath>/` to preserve deleted files. `CleanupArchives` purges backup directories older than `backup_retention_days` by parsing date prefixes from directory names.

## Cloud source and destination handling

`isRcloneRemote` in `path.go` detects rclone remote sources (contains `:`, does not start with `/` or `~`). `rcloneDestName` derives the destination folder name from the job's `destination` field or, if empty, from the source basename (or path after `:` for remote sources). Tilde expansion for all paths is done by the config package at parse time; `expandPath` in `engine/path.go` only stats the already-expanded path to determine if it is a directory.

## Stats capture

Rsync and rclone stats are captured differently. Rsync output is tee'd to a `bytes.Buffer` during execution and parsed from memory afterward (`ParseRsyncStats`). Rclone output goes to a shared log file; the executor records the line count before each call and reads only the new section afterward (`ParseRcloneStats`). Both parsers produce the same `TransferStats` struct.

## Summary rendering

`RenderSummary` in `render.go` writes a color-coded, status-first summary to the terminal. Each job gets a status symbol (✓/✗/–) with the outcome colored green/red/yellow. Rclone jobs targeting multiple remotes are grouped by adjacency: identical results (same `FilesChecked`, no transfers) collapse to one line ("N remotes · X checked"), differing results expand into a tree with ├/└ branches. Transfer details appear only when files were actually moved. Elapsed time is suppressed when under 1 second. `FilesChecked` counts use thousand separators. A tally footer shows pass/fail counts and duration. Color output is controlled by `useColor` (from `term.IsTerminal` in `main.go`).

`JobResult` carries a `Remote` field (non-empty for rclone jobs) so the renderer can group by job name without parsing concatenated strings. The runner guarantees that rclone `JobResult`s for the same config job are adjacent in the `Jobs` slice.

## Config

TOML at `${XDG_CONFIG_HOME:-~/.config}/shuttle/config.toml`. See `config.example.toml`. Top-level sections: `[defaults.rsync]`, `[defaults.rclone]`, and one or more `[[job]]` entries. `ConfigPath()` is exported so the CLI can pass it to `NewRunner` for per-config locking.

## Testing patterns

- Stats parsers and formatting use fixture files in `testdata/` (rsync/rclone output samples).
- Config tests use TOML fixtures in `testdata/` for valid, minimal, and invalid configs.
- Rsync executor tests do real rsync calls against temp directories (not mocked).
- Helper `readFixture(t, name)` and `testdataPath(name)` resolve fixtures relative to repo root from package test directories.
- No test frameworks beyond Go stdlib `testing`. Table-driven tests where applicable.

## Conventions

- Errors wrap context: `fmt.Errorf("doing X: %w", err)`
- No panics, no `os.Exit` outside `main.go`
- Partial failure is normal: jobs continue after individual source failures
- Exit codes: 0 success, 1 task failure, 2 config/usage error, 130 signal
- `expandTilde` lives only in `internal/config/config.go`; engine's `path.go` calls `os.Stat` directly on already-expanded paths
- External tools are invoked via `exec.CommandContext`, never `exec.Command("sh", "-c", ...)` (no shell injection surface)

## Project Docs

- @docs/CONVENTIONS.md — code style, naming, component patterns, error handling, dependency rules, and AI-assisted development instructions. Read when writing new code, adding files, or reviewing style decisions.
- @docs/PRD.md — product requirements, target audience, competitive landscape, feature inventory, gap analysis, and phased MVP scope. Read when implementing features, prioritizing work, or making product decisions.
- @docs/TECH_SPEC.md — architecture details, dependency audit, security assessment, performance characteristics, technical debt inventory, testing strategy, and deployment readiness checklist. Read when modifying architecture, adding dependencies, or planning infrastructure work.
