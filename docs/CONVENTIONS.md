# Shuttle: Project Conventions

*Last updated: 2026-04-14*

---

## 1. Project Overview

Shuttle is a personal backup and synchronization CLI written in Go. It orchestrates rsync (local file sync) and rclone (cloud uploads) through a single TOML configuration file, handling prerequisite checks, exclusive locking, partial failure recovery, stats parsing, and archive retention. The codebase is approximately 2,800 lines across four packages with strict layering, high test coverage on core logic, and no external test frameworks beyond Go's standard library.

---

## 2. Tech Stack

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.26.2 | Language runtime (pinned in `go.mod`) |
| `github.com/spf13/cobra` | v1.10.2 | CLI framework |
| `github.com/pelletier/go-toml/v2` | v2.3.0 | TOML configuration parsing |
| `golang.org/x/term` | v0.42.0 | Secure terminal password input |
| `golang.org/x/sys` | v0.43.0 | Syscalls (flock) |
| rsync | System-installed | Local file synchronization (external tool) |
| rclone | System-installed | Cloud upload/sync (external tool) |

No Makefile or task runner. Linting via `golangci-lint` (config at `.golangci.yml`). CI via GitHub Actions (`.github/workflows/ci.yml`). Build and test via `go build` and `go test` directly.

---

## 3. File and Folder Structure

```
cmd/
  shuttle/
    main.go              # CLI entry point. ALL os.Exit calls live here.

internal/
  config/
    config.go            # TOML parsing, validation, path expansion
    config_test.go       # Config tests

  engine/
    runner.go            # Pipeline orchestrator (prerequisites, locking, dispatch)
    flags.go             # Arg-list builders: BuildRsyncArgs, BuildRcloneArgs, WarnFlagConflicts
    rsync.go             # Rsync executor (output capture, stats parsing)
    rclone.go            # Rclone executor (mode selection, archive cleanup)
    path.go              # Path stat helper, rclone remote detection, dest-name derivation
    stats.go             # Result types, output parsers, duration formatting
    render.go            # Color-coded summary rendering with grouping and collapsing
    progress.go          # ProgressWriter: spinner and status lines during job execution
    runner_test.go       # Runner unit tests
    rsync_test.go        # Rsync integration tests
    rclone_test.go       # Rclone progress parsing tests
    flags_test.go        # Flag assembly and conflict-detection tests
    path_test.go         # Path resolution tests
    stats_test.go        # Stats parsing and rendering tests
    progress_test.go     # ProgressWriter behavior tests

  log/
    logger.go            # Dual-stream logger (terminal + file)
    logger_test.go       # Logger tests

testdata/
  config_*.toml          # Config fixtures
  rsync_stats_*.txt      # Rsync output fixtures
  rclone_log_*.txt       # Rclone log fixtures

docs/                    # Project documentation (PRD, tech spec, conventions)

.github/
  workflows/
    ci.yml               # GitHub Actions CI (test + lint jobs)

config.example.toml      # Full configuration reference
.golangci.yml            # golangci-lint configuration (explicit default linters)
README.md                # User-facing documentation
LICENSE                  # MIT license
CLAUDE.md                # Development guide for Claude Code
go.mod                   # Module declaration
go.sum                   # Dependency hashes
.gitignore               # Ignores: shuttle binary
```

### Placement Rules

- **New packages** go under `internal/`. Nothing is exported outside the module.
- **Test files** live next to the code they test (`foo_test.go` beside `foo.go`).
- **Test fixtures** go in `testdata/` at the repo root. Access via `testdataPath(name)` or `readFixture(t, name)` helpers in tests.
- **CLI commands** stay in `cmd/shuttle/main.go`. If the CLI grows, add subcommand files in the same directory (e.g., `cmd/shuttle/validate.go`).
- **No `pkg/` directory.** Everything is internal.
- **No `utils/` or `helpers/` packages.** If a utility is needed by two packages, evaluate whether a shared package is justified or if duplication is cheaper (see `expandTilde` precedent).

---

## 4. Code Style

### Formatting

- `go fmt` is the standard. No custom formatting rules.
- `golangci-lint` with the default linter set, configured in `.golangci.yml`. Linters are listed explicitly (`errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`) so the set is version-controlled and won't change silently on tool updates. CI runs golangci-lint on every push and PR.

### Naming

| Entity | Convention | Example |
|--------|-----------|---------|
| Packages | Single lowercase word | `config`, `engine`, `log` |
| Files | `snake_case.go` | `runner_test.go`, `path_test.go` |
| Exported types | `PascalCase` | `RsyncExecutor`, `RcloneDefaults` |
| Unexported types | `camelCase` | (none currently; use `camelCase` when needed) |
| Functions | `PascalCase` (exported), `camelCase` (unexported) | `LoadFile`, `expandTilde` |
| Constants | `PascalCase` (exported), `camelCase` (unexported) | `StatusOK`, `StatusFailed`, `ModeCopy` |
| Variables | `camelCase` | `lockFile`, `dryRun` |
| Booleans | Assertion-style prefix | `isDir`, `isRemote`, `hasErrors` |
| Enum-like strings | `Status` type with named constants | `StatusOK`, `StatusFailed`, `StatusNotFound`, `StatusSkipped` |
| Test functions | `TestUnit_Scenario_Expected` | `TestValidateJobNames_UnknownName_ReturnsError` |

### Import Ordering

Standard Go convention (enforced by `goimports`):

```go
import (
    "fmt"           // stdlib
    "os"

    "github.com/spf13/cobra"                    // third-party
    toml "github.com/pelletier/go-toml/v2"

    "github.com/jkleinne/shuttle/internal/config" // internal
)
```

Three groups separated by blank lines: stdlib, third-party, internal.

### Export Patterns

- **Config package:** Exports `Load`, `LoadFile`, `LoadBytes`, `ConfigPath`, `Config`, `Defaults`, `RsyncDefaults`, `RcloneDefaults`, `Job`, `EngineRsync`, `EngineRclone`, `ModeCopy`, `ModeSync`, and query methods (`JobNames`, `AllRemoteNames`).
- **Engine package:** Exports `Runner`, `NewRunner`, `RunOptions`, `Summary`, `RenderSummary`, `ValidateJobNames`, `BuildRsyncArgs`, `BuildRcloneArgs`, `WarnFlagConflicts`, `RsyncExecutor`, `NewRsyncExecutor`, `RcloneExecutor`, `NewRcloneExecutor`, `ParseRsyncStats`, `ParseRcloneStats`, `FormatDuration`, `Status`, `StatusOK`, `StatusFailed`, `StatusSkipped`, `StatusNotFound`, `TransferStats`, `ItemResult`, `JobResult`, `ProgressWriter`, `NewProgressWriter`.
- **Log package:** Exports `Logger`, `New`, `NewWithWriter`, `LogPath`.
- **Rule:** Export only what the consuming package needs. When in doubt, keep it unexported and promote later.

---

## 5. Component Patterns

### Executor Pattern

Both `RsyncExecutor` and `RcloneExecutor` accept a pre-assembled `[]string` argument list and follow the same shape:

```go
type FooExecutor struct {
    logger  *log.Logger
    // rclone also holds: logFile string
}

func (e *FooExecutor) Exec(ctx context.Context, args []string, onProgress func(string)) ItemResult {
    // 1. Derive display name from args (second-to-last element is source)
    // 2. Create exec.Command with context
    // 3. Pipe stdout/stderr through a goroutine that captures bytes for stats
    //    and calls onProgress with extracted progress text (nil disables callbacks)
    // 4. Run command
    // 5. Parse stats from captured output
    // 6. Return ItemResult with status and stats
}
```

Argument lists are built by `BuildRsyncArgs` and `BuildRcloneArgs` in `flags.go` before being passed to the executor. The executor does not construct flags itself.

**Follow this pattern for any new executor.** The executor is responsible for:
- Running the external tool with a pre-built argument list
- Parsing tool-specific output into common `ItemResult` / `TransferStats` types

The executor is NOT responsible for:
- Building flag lists (that's `flags.go`)
- Deciding whether to run (that's the runner's job)
- Logging job-level context (runner does this)
- Handling partial failures (runner accumulates results)

### Runner Pattern

`Runner` is the sole orchestrator. It:
1. Checks prerequisites
2. Acquires the lock
3. Iterates jobs in config order
4. Delegates to executors, passing an `onProgress` callback wired to `ProgressWriter`
5. Collects results into `Summary`

**New pipeline stages** (e.g., a notification step) go here, not in executors or main.go.

### ProgressWriter Pattern

`ProgressWriter` (`progress.go`) manages the live terminal display during job execution. It is constructed in `main.go` and injected into the runner via `NewRunner`.

The writer has two modes, selected at construction time:

- **Interactive mode** (`interactive: true`, set when stdout is a TTY): shows a braille spinner on the active job line, with elapsed time or tool-reported progress alongside. When the job finishes, the spinner line is cleared with ANSI `\033[2K\r` and replaced with a compact status line.
- **Non-interactive mode** (pipes, cron): `StartJob` and `UpdateProgress` are no-ops. `FinishJob` and `SkipJob` write plain status lines without cursor manipulation.

The runner calls `pw.StartJob(ctx, label)` before dispatching each executor and `pw.FinishJob(result)` after, with labels formatted as:

- Rclone: `"name → remote"`
- Rsync with multiple sources: `"name · source"`
- Rsync with single source: `"name"`

In interactive mode, the runner uses `logger.FileHeader` and `logger.FileInfo` (log file only) instead of `logger.Header` and `logger.Info` (both streams) to avoid interleaving plain-text output with the live spinner.

`ProgressWriter` reuses `statusSymbol`, `itemStatsText`, and `formatTransfer` from `render.go`. It does not duplicate formatting logic.

### Config Pattern

Config is a plain struct populated by TOML unmarshaling, then validated:

```go
func LoadBytes(data []byte) (*Config, error) {
    var cfg Config
    if err := toml.Unmarshal(data, &cfg); err != nil {
        return nil, fmt.Errorf("parsing config: %w", err)
    }
    if err := cfg.expandPaths(); err != nil {
        return nil, err
    }
    if err := cfg.validate(); err != nil {
        return nil, err
    }
    return &cfg, nil
}
```

**Tilde expansion happens once, immediately after parsing.** Validation runs on expanded paths. No lazy expansion.

---

## 6. Data Fetching

Not applicable in the traditional sense (no HTTP APIs, no database). The equivalent is:

- **Config loading:** Read file, parse TOML, validate. Done once at startup.
- **External tool execution:** `os/exec.Command` with context for cancellation. Stdout is piped through a goroutine that captures bytes for stats parsing and extracts progress text. For rsync, the goroutine also writes captured bytes to a buffer for `ParseRsyncStats`. For rclone, `-P` progress goes to stdout when piped; the goroutine parses "Transferred:" lines (handling concatenation with per-file progress lines that lack trailing delimiters). Stderr is captured to a buffer and written to the log file via `logger.FileError` for both engines. Stats are parsed from the captured bytes (rsync) or log file section (rclone) after execution completes.
- **File system checks:** `os.Stat` for path existence. `syscall.Flock` for locking.

**Rule:** Most I/O happens in `cmd/shuttle/main.go` (config loading, logger creation) or in executors (tool execution). The runner also performs orchestration-level I/O: `os.Stat` for prerequisite checks and path resolution, `os.OpenFile` + `syscall.Flock` for locking. Core business logic (stats parsing, job filtering) operates on values. Summary rendering (`render.go`) accepts an `io.Writer` and values, keeping I/O at the boundary.

---

## 7. Error Handling

### Wrapping Convention

Always wrap errors with context about what was being attempted:

```go
// Good
return fmt.Errorf("loading config from %s: %w", path, err)

// Bad
return err
return fmt.Errorf("error: %w", err)
return fmt.Errorf("failed: %w", err)
```

### Error vs. Partial Failure

- **Errors** (returned as `error`): Prerequisites not met, config invalid, lock unavailable. These stop the pipeline.
- **Partial failures** (recorded in `Summary.Errors`): Individual source not found, single sync command failed. These are logged and accumulated; the pipeline continues.

### Exit Code Mapping

Errors map to exit codes exclusively in `main.go`:

```go
if signaled { return 130 }
if errors.Is(err, errPartialFailure) { return 1 }
if err != nil { return 2 }
return 0
```

**No `os.Exit` outside `main.go`.** No panics anywhere.

### Log Level Selection

| Situation | Level | Example |
|-----------|-------|---------|
| Phase start (non-interactive) | `Header` | `==> Syncing: manga` |
| Phase start (interactive, TTY) | `FileHeader` | written to log file only; terminal uses spinner |
| Informational detail (non-interactive) | `Info` | `Source: /path/to/source` |
| Informational detail (interactive, TTY) | `FileInfo` | written to log file only |
| Item completed successfully | `Success` | `rsync success` |
| Non-fatal issue | `Warn` | `Archive cleanup: skipping unrecognized directory` |
| Item or phase failure (executor) | `FileError` | written to log file only; terminal shows status via ProgressWriter |

`FileHeader`, `FileInfo`, and `FileError` write to the log file only and produce no terminal output. They are used in interactive mode so that spinner/status lines on the terminal are not interleaved with plain-text log messages.

---

## 8. Environment Variables

| Variable | Convention | Where Documented | How to Add |
|----------|-----------|-----------------|------------|
| `XDG_CONFIG_HOME` | XDG standard | `CLAUDE.md`, `config.go` comments | Standard XDG. Do not add shuttle-specific env vars for paths. |
| `XDG_STATE_HOME` | XDG standard | `main.go` comments | Same as above. |
| `RCLONE_CONFIG_PASS` | Rclone standard | `main.go:promptForPassword` | Set by user or prompted at runtime. |

### Rules

- **No shuttle-specific environment variables.** Use XDG standards for paths and rclone standards for rclone config.
- **No `os.Getenv` deep in business logic.** Environment is read in `main.go` and passed as function arguments or struct fields.
- **Config is always TOML, never env vars.** The TOML file is the single source of truth for sync job definitions.

---

## 9. Git and Version Control

### Branch Naming

```
type/short-description
```

Types: `feat`, `fix`, `docs`, `refactor`, `chore`, `test`, `ci`, `perf`, `build`

Examples: `feat/add-validate-cmd`, `fix/archive-date-parsing`, `docs/add-readme`

### Commit Messages

Conventional Commits format:

```
type(scope): imperative description
```

- Subject under 72 characters
- Imperative mood: "add feature" not "added feature"
- Scope when change is clearly scoped: `feat(engine):`, `fix(config):`, `test(stats):`
- No body unless the diff genuinely needs context the code can't provide

**Banned words:** enhance, ensure, utilize, leverage, streamline, robust, comprehensive, facilitate, optimize (unless measured), improve (unless specific), seamless, efficient (unless measured)

### PR Expectations

- One logical change per PR
- `## Summary` and `## Test Plan` headers
- Direct and specific summary, not a restatement of the diff
- Test plan describes what was actually tested with specifics

---

## 10. Dependency Management

### Rules

- **Minimize dependencies.** The current set (3 direct imports, 3 transitive) is intentionally small. Do not add a dependency for something achievable in 20 lines of Go.
- **No test frameworks.** Use Go's standard `testing` package. No testify, no gomock, no ginkgo.
- **Pin via `go.sum`.** Go modules handle this automatically. Do not pin exact versions in `go.mod` beyond what `go mod tidy` produces.
- **Audit before adding.** Before adding a new dependency: check its maintenance status, license, transitive dependency count, and whether the standard library has an equivalent.
- **Update periodically.** Run `go list -m -u all` and `govulncheck ./...` to check for updates and vulnerabilities.

---

## 11. Security Patterns

### Command Execution

- **Always use `exec.Command`, never `exec.Command("sh", "-c", ...)`**. Direct `execve` prevents shell injection.
- **Pass arguments as separate strings**, not concatenated into a command string.
- **User config values (paths, flags) are CLI arguments**, not interpolated into shell commands.

### Secrets

- **No secrets in config.** Rclone credentials live in rclone's own config.
- **Password is prompted securely** via `term.ReadPassword` (echo-disabled).
- **Environment variable for password** is rclone's standard mechanism. Do not introduce alternatives unless they improve security (e.g., `--password-command` integration).

### Path Handling

- **Tilde expansion** uses `os.UserHomeDir()`, not shell expansion.
- **No shell glob expansion.** Paths are literal. rsync and rclone handle their own globbing.
- **Stat before use.** Paths are verified with `os.Stat` before being passed to executors.

---

## 12. Common Pitfalls

### Using `exec.Command("sh", "-c", ...)` for external tools

**Wrong:**
```go
cmd := exec.Command("sh", "-c", fmt.Sprintf("rsync -av %s %s", src, dst))
```

**Right:**
```go
cmd := exec.CommandContext(ctx, "rsync", "-a", "-v", src, dst)
```

Shell invocation opens a command injection surface and breaks on paths with spaces or special characters.

---

### Swallowing errors from executor results

**Wrong:**
```go
result := r.rsync.Exec(ctx, args)
// continue without checking result.Status
```

**Right:**
```go
result := r.rsync.Exec(ctx, args)
if result.Status == StatusFailed {
    r.logger.Error("rsync failed for %s", source)
    // record in job results, continue to next source
}
```

Every executor result must be checked. Failed items are recorded, not ignored.

---

### Adding `os.Exit` outside `main.go`

**Wrong (in engine or config):**
```go
if err != nil {
    fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
    os.Exit(1)
}
```

**Right:**
```go
if err != nil {
    return fmt.Errorf("doing X: %w", err)
}
```

`os.Exit` defeats deferred cleanup, prevents testing, and breaks the layered architecture. Return errors and let `main.go` decide the exit code.

---

### Reading environment variables deep in business logic

**Wrong (in engine):**
```go
func (r *Runner) Run(ctx context.Context, opts RunOptions) (*Summary, error) {
    drive := os.Getenv("EXTERNAL_DRIVE_PATH")
    // ...
}
```

**Right:**
```go
// main.go reads config and config path, passes both to the runner
runner := engine.NewRunner(cfg, configPath, logger, opts.DryRun, logPath)
```

Environment is read at the boundary (main.go). Business logic receives values via struct fields or function arguments.

---

### Creating `internal/utils` or `internal/helpers`

**Wrong:**
```go
// internal/utils/paths.go
func ExpandTilde(path string) (string, error) { ... }
```

Unless three or more packages need the same function, duplicate it. `expandTilde` lives in `internal/config/config.go` and is applied once at parse time. Engine code calls `os.Stat` directly on already-expanded paths; it does not need its own tilde expansion. A shared utils package creates a dependency magnet.

---

## 13. AI-Assisted Development Instructions

These rules apply when using Claude Code, Copilot, or any AI coding tool on this project.

- **Read before writing.** Always read the relevant source file before suggesting modifications. Understand existing patterns.
- **Follow existing patterns.** Check neighboring files for conventions before introducing a new approach. If the codebase uses table-driven tests, write table-driven tests.
- **No new dependencies without confirmation.** Do not add packages to `go.mod` without asking first. Check if the standard library can do it.
- **No new files without confirmation.** State the file's purpose and where it fits in the architecture before creating it.
- **Run tests before claiming success.** `go test ./...` must pass. `go vet ./...` must pass. Do not present code that you haven't verified.
- **One logical change per commit.** Do not batch unrelated changes. Do not "clean up while you're here."
- **Errors wrap context.** Every `fmt.Errorf` includes what was being attempted. Never bare `return err`.
- **No os.Exit outside main.go.** Return errors.
- **No panics.** Handle errors explicitly.
- **No premature abstraction.** Do not create interfaces, factories, or generic frameworks unless complexity demands it. Three similar lines are better than the wrong abstraction.
- **Test behavior, not implementation.** Tests assert what the code does, not how it does it.
- **Use fixtures for parser tests.** Put sample output in `testdata/`, not inline strings.
- **Respect the layering.** CLI does not import engine internals. Engine does not import CLI. Config and log are leaf packages.
