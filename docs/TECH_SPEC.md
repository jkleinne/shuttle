# Shuttle: Technical Specification

*Last updated: 2026-04-14*

---

## 1. Executive Summary

Shuttle is a ~2,800-line Go CLI that orchestrates rsync and rclone via `os/exec` to perform local file synchronization and cloud uploads in a single config-driven pipeline. The architecture is clean: four packages with strict layering (CLI, engine, config, log), no circular dependencies, and explicit error propagation throughout. Test coverage is high for config parsing, stats extraction, path resolution, and rsync integration, though the rclone executor and CLI entry point lack automated tests.

**Production-readiness assessment: approximately 85% ready for personal use, 70% ready for public distribution.** The sync pipeline is solid, well-tested, and handles partial failures gracefully. README, LICENSE (MIT), and platform-neutral example config are in place. What's still missing for public distribution is release infrastructure (CI, binary builds, packaging) and a config validation subcommand. There are no security vulnerabilities in the application itself, though the reliance on environment variables for rclone password handling inherits rclone's own exposure surface.

---

## 2. Architecture Overview

### Component Diagram

```
                         ┌─────────────────────────────┐
                         │     cmd/shuttle/main.go      │
                         │  ┌─────────┐  ┌───────────┐ │
                         │  │  Cobra   │  │  Signal   │ │
                         │  │  CLI     │  │  Handler  │ │
                         │  └────┬────┘  └─────┬─────┘ │
                         │       │              │       │
                         │  ┌────┴──────────────┴────┐  │
                         │  │    executeRun()         │  │
                         │  │  - load config          │  │
                         │  │  - validate names       │  │
                         │  │  - create logger        │  │
                         │  │  - create ProgressWriter│  │
                         │  │  - prompt password      │  │
                         │  │  - call engine          │  │
                         │  │  - render summary       │  │
                         │  │  - map exit codes       │  │
                         │  └────────────┬────────────┘  │
                         └───────────────┼───────────────┘
                                         │
                    ┌────────────────────┼────────────────────┐
                    │                    │                    │
           ┌────────▼────────┐  ┌────────▼────────┐  ┌───────▼───────┐
           │ internal/config │  │ internal/engine  │  │ internal/log  │
           │                 │  │                  │  │               │
           │ Load()          │  │ Runner           │  │ Logger        │
           │ LoadFile()      │  │  .Run()          │  │  .Header()   │
           │ LoadBytes()     │  │  .checkPrereqs() │  │  .Info()     │
           │ ConfigPath()    │  │  .acquireLock()  │  │  .FileHeader()│
           │ validate()      │  │  .runRsyncJob()  │  │  .FileInfo() │
           │ expandPaths()   │  │  .runRcloneJob() │  │  .FileError()│
           │                 │  │                  │  │  .Success()  │
           │ Config struct   │  │ BuildRsyncArgs   │  │  .Warn()     │
           │ Defaults struct │  │ BuildRcloneArgs  │  │  .Error()    │
           │ Job struct      │  │ NewRsyncExecutor │  │  .Close()    │
           │                 │  │ NewRcloneExecutor│  │  .LogPath()  │
           │                 │  │ ProgressWriter   │  │ Terminal +   │
           │                 │  │ Summary/Stats    │  │ File output  │
           └─────────────────┘  └────────┬─────────┘  └───────────────┘
                                         │
                              ┌──────────┴──────────┐
                              │    os/exec calls     │
                              │                      │
                         ┌────┴────┐          ┌──────┴─────┐
                         │  rsync  │          │   rclone   │
                         │ (local) │          │  (cloud)   │
                         └─────────┘          └────────────┘
```

### Package Responsibilities

| Package | Responsibility | Imports From |
|---------|---------------|-------------|
| `cmd/shuttle` | CLI parsing, signal handling, exit codes, password prompt, log setup | `config`, `engine`, `log` |
| `internal/config` | TOML parsing, path expansion, structural validation | (none, leaf package) |
| `internal/engine` | Sync pipeline orchestration, executor wrappers, stats parsing, summary | `config`, `log` |
| `internal/log` | Dual-stream logger (colored terminal + plain-text file) | (none, leaf package) |

### Layering Rules

- **No sideways imports:** `config` cannot import `log`, `log` cannot import `config`.
- **No upward imports:** `engine` cannot import `cmd/shuttle`.
- **All `os.Exit` in `main.go`:** Engine and config return errors; CLI maps them to exit codes.
- **All I/O at boundaries:** Config reads files, engine shells out to tools, log writes streams. Business logic (validation, path resolution, stats parsing) operates on values.

### Data Flow

```
Config file (TOML)
  → config.Load() → Config struct
    → ValidateJobNames(skip, only, jobNames)
    → validateRemoteNames(selected, allRemoteNames)
      → engine.NewProgressWriter(stdout, interactive, useColor)
      → engine.NewRunner(config, configPath, logger, pw, dryRun, logFile)
        → runner.Run(ctx, opts)
            per job: pw.StartJob → executor.Exec(ctx, args, onProgress)
                                        → pw.UpdateProgress (via callback)
                                        → pw.FinishJob(result)
          → Summary
          → render.RenderSummary(summary, useColor) → terminal
            → main.go maps Summary.HasErrors() to exit code
```

### Local Development vs. Production

There is no distinction between development and production. Shuttle is a single binary with no server component, no deployment target, and no environment-specific configuration beyond XDG paths. Local development is:

```bash
go build -o shuttle ./cmd/shuttle    # Build
go test ./...                        # Test
./shuttle --dry-run                  # Run with preview
./shuttle                            # Run for real
```

Version injection at build time (`-ldflags "-X main.version=X.Y.Z"`) is the only build-time variation.

---

## 3. Data Model

Shuttle has no database and no persistent state beyond:

1. **Configuration file** (`~/.config/shuttle/config.toml`): read-only input.
2. **Log files** (`~/.local/state/shuttle/logs/*.log`): append-only output.
3. **Lock file** (`<tempdir>/shuttle-<hash>.lock`): ephemeral flock, released on process exit. The hash is the first 8 hex characters of SHA-256 of the config file path, allowing different configs to run concurrently. The temp directory is determined by `os.TempDir()` for portability.

### In-Memory Data Structures

```
Config
├── Defaults: *Defaults
│   ├── Rsync: *RsyncDefaults
│   │   └── Flags: []string
│   └── Rclone: *RcloneDefaults
│       ├── Flags: []string
│       ├── FilterFile: string
│       ├── Transfers: int
│       ├── Checkers: int
│       ├── Bwlimit: string
│       ├── DriveChunkSize: string
│       ├── BufferSize: string
│       ├── UseMmap: bool
│       ├── Timeout: string
│       ├── Contimeout: string
│       ├── LowLevelRetries: int
│       └── OrderBy: string
└── Jobs: []Job
    ├── Name: string
    ├── Engine: string ("rsync" | "rclone")
    ├── ExtraFlags: []string
    │   (rsync fields)
    ├── Sources: []string
    ├── Destination: string
    ├── Delete: bool
    │   (rclone fields)
    ├── Source: string
    ├── Remotes: []string
    ├── Mode: string ("copy" | "sync")
    ├── BackupPath: string
    ├── BackupRetentionDays: int
    ├── FilterFile: string
    │   (rclone per-job tuning overrides — same fields as RcloneDefaults tuning)
    ├── Transfers: int
    ├── Checkers: int
    ├── Bwlimit: string
    ├── DriveChunkSize: string
    ├── BufferSize: string
    ├── UseMmap: bool
    ├── Timeout: string
    ├── Contimeout: string
    ├── LowLevelRetries: int
    └── OrderBy: string

RunOptions
├── DryRun: bool
├── SkipJobs: []string
├── OnlyJobs: []string
└── SelectedRemotes: []string

Summary (HasErrors() bool)
├── Jobs: []JobResult
│   ├── Name: string   (job name from config, e.g. "manga", "docs-to-cloud")
│   ├── Remote: string (rclone remote name, e.g. "crypt_gdrive"; empty for rsync)
│   └── Items: []ItemResult (guaranteed non-empty)
│       ├── Name: string
│       ├── Status: Status ("ok" | "failed" | "skipped" | "not_found")
│       └── Stats: TransferStats
│           ├── FilesChecked: int
│           ├── FilesTransferred: int
│           ├── FilesDeleted: int
│           ├── BytesSent: string
│           ├── Speed: string
│           └── Elapsed: time.Duration
├── Errors: []string
├── Duration: time.Duration
└── DryRun: bool
```

### Persistent Storage Needs

None for current features. If notifications, run history, or scheduling were added, a lightweight store (SQLite or JSON file) might be warranted, but the current design correctly avoids persistence beyond logs.

---

## 4. API Surface

Shuttle is a CLI tool, not a server. Its "API" is the command-line interface and the config file schema.

### CLI Commands

| Command | Method | Purpose | Auth | Input | Output |
|---------|--------|---------|------|-------|--------|
| `shuttle` (bare) | — | Alias for `shuttle run` | None | Config file + flags | Sync pipeline execution |
| `shuttle run` | — | Execute sync pipeline | None | Config file + flags | Terminal output + log file + exit code |
| `shuttle validate` | — | Validate config without running jobs | None | Config file (XDG path) | `config ok: <path>` to stdout, or error to stderr |
| `shuttle version` | — | Print version string | None | None | `shuttle <version>` to stdout |

### CLI Flags (for `run`)

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--dry-run`, `-n` | bool | `false` | Preview without modifying files |
| `--skip` | string (repeatable) | — | Skip named job(s); mutually exclusive with `--only` |
| `--only` | string (repeatable) | — | Run only named job(s); mutually exclusive with `--skip` |
| `--remote` | string (repeatable) | — | Target specific cloud remote(s); validated against union of all rclone jobs' remotes |

### Config File Schema

See `config.example.toml` for the full schema. Top-level TOML sections:

- `[defaults.rsync]` — baseline `flags` applied to all rsync jobs (optional)
- `[defaults.rclone]` — baseline `flags`, `filter_file`, and tuning fields applied to all rclone jobs (optional)
- `[[job]]` — one or more job entries, each with `name`, `engine`, and engine-specific fields

Key validation rules:

- Job names: unique, non-empty
- `engine`: must be `"rsync"` or `"rclone"`
- Rsync jobs: at least one source, non-empty destination
- Rclone jobs: non-empty source, at least one remote, non-duplicate remotes, mode must be `"copy"` or `"sync"`

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | All jobs succeeded |
| 1 | Partial failure (at least one item failed, others completed) |
| 2 | Config or usage error |
| 130 | Interrupted by signal (SIGINT/SIGTERM) |

### Undocumented or Inconsistent Behaviors

- **`shuttle` (no args) silently runs the full pipeline.** The root command defaults to `run`. This is intentional but could surprise users who expect `--help` from a bare invocation. Cobra's default behavior.

---

## 5. Dependencies Audit

### Direct Dependencies

| Package | Version | Status | Purpose |
|---------|---------|--------|---------|
| `github.com/spf13/cobra` | v1.10.2 | Active | CLI framework |
| `github.com/pelletier/go-toml/v2` | v2.3.0 | Active | TOML config parsing |
| `golang.org/x/term` | v0.42.0 | Active | Secure password input (`ReadPassword`) |

### Transitive Dependencies

| Package | Version | Status | Pulled By |
|---------|---------|--------|-----------|
| `github.com/spf13/pflag` | v1.0.9 | Active (indirect) | cobra |
| `github.com/inconshreveable/mousetrap` | v1.1.0 | Active (indirect) | cobra (Windows) |
| `golang.org/x/sys` | v0.43.0 | Active (indirect) | term |

### Audit Summary

**The dependency tree is clean.**

- No unused dependencies.
- No redundant packages (no two packages solving the same problem).
- All dependencies are well-maintained, widely-used Go ecosystem packages.
- Total: 3 direct imports + 3 transitive = 6 total dependencies. Minimal for a Go CLI.
- `mousetrap` is Windows-only (cobra includes it for cross-platform subcommand handling). It's harmless but technically unnecessary on macOS/Linux.

### Version Currency

All dependencies appear to be on recent versions as of the Go 1.26.2 toolchain. No known security advisories. Run `go list -m -u all` to check for available updates.

---

## 6. Environment and Configuration

### Environment Variables

| Variable | Used By | Required | Purpose |
|----------|---------|----------|---------|
| `XDG_CONFIG_HOME` | config.Load() | No | Config directory override. Default: `~/.config` |
| `XDG_STATE_HOME` | main.go:logDirectory() | No | Log directory override. Default: `~/.local/state` |
| `RCLONE_CONFIG_PASS` | main.go:executeRun() | No | Rclone encrypted config password. Prompted if unset and TTY available. |

### Hardcoded Values

| Value | Location | Risk | Recommendation |
|-------|----------|------|----------------|
| `<tempdir>/shuttle-<hash>.lock` | `runner.go:lockFilePath` | Low | Uses `os.TempDir()` for portability. Hash of config path prevents conflicts between configs. |
| `--stats`, `--info=progress2` | `flags.go:rsyncInstrumentationFlags` | Low | Rsync 3.1+ feature (`--info=progress2`). Not a concern on macOS (ships with 3.x) but could affect some Linux distros. |
| `--stats 1s`, `-P` | `flags.go:BuildRcloneArgs` | Low | Instrumentation flags injected before user flags. Users can shadow them via `extra_flags` (Shuttle logs a warning when they do). |

### Secrets Management

- **Rclone config password:** Handled via `RCLONE_CONFIG_PASS` env var or interactive prompt. The env var approach is rclone's standard mechanism. On Linux, the password is visible in `/proc/<pid>/environ` while the process runs. On macOS, this is less exposed but still readable by root.
- **No `.env` file or `.env.example`:** Not needed. The tool reads config from TOML, not env vars (except XDG overrides and rclone password).
- **No secrets in config:** The TOML config references rclone remote names, not credentials. Credentials live in rclone's own config file (`~/.config/rclone/rclone.conf`).

---

## 7. Security Assessment

### Findings by Severity

#### Moderate

| Finding | Description | Affected Code | Mitigation |
|---------|-------------|--------------|------------|
| **Command injection surface** | Rsync and rclone are invoked via `exec.Command` with user-controlled config values (paths, extra_opts). However, `exec.Command` in Go does NOT invoke a shell, so shell metacharacters in paths are safe. `extra_opts` are passed as individual args, not shell-interpolated. | `rsync.go:Exec`, `rclone.go:Exec` | **Currently safe.** `exec.Command` uses `execve` directly. No shell injection vector exists. The risk would increase if the code ever used `exec.Command("sh", "-c", ...)` instead. |
| **Rclone password in environment** | `RCLONE_CONFIG_PASS` is set via `os.Setenv`, making it visible to child processes and (on Linux) readable from `/proc/*/environ`. | `main.go:executeRun` | This is rclone's documented approach. Alternative: use `--password-command` to read from a keychain or pass manager. |

#### Low

| Finding | Description | Affected Code | Mitigation |
|---------|-------------|--------------|------------|
| **Lock file permissions** | `<tempdir>/shuttle-<hash>.lock` is created with default permissions (0644). Other users can read it. | `runner.go:acquireLock` | The file contains no sensitive data (empty or near-empty). The flock mechanism is what provides exclusion, not file permissions. No action needed. |
| **Log files may contain paths** | Log output includes source and destination paths, which could reveal directory structure. | `log/logger.go` | Log files are in the user's state directory with default permissions. Standard for CLI tools. Users who need stricter controls can restrict directory permissions. |
| **No path traversal validation** | Config paths are not checked for `../` sequences. | `config/config.go` | Since this is a personal tool reading a user-authored config, the user is both the attacker and the victim. Path traversal validation is unnecessary here. |

#### Not Applicable

- **Authentication/Authorization:** Not applicable. Local CLI tool, single user, no network service.
- **CORS:** Not applicable. No HTTP server.
- **Rate Limiting:** Not applicable. No API endpoints.
- **Input Validation (untrusted input):** Config is authored by the user. CLI args are parsed by Cobra. No untrusted input surface.
- **Dependency Vulnerabilities:** Run `govulncheck ./...` to confirm. The dependency set is small and well-maintained.
- **Regulatory (PII, HIPAA, PCI):** Not applicable. The tool moves files; it doesn't process, store, or transmit user data beyond what's in the files themselves.

---

## 8. Performance Assessment

*This section is adapted for a CLI tool. Web-specific metrics (Lighthouse, CLS, LCP, bundle size) do not apply.*

### Execution Performance

| Aspect | Current State | Assessment |
|--------|--------------|------------|
| **Startup time** | Config parse + prerequisite checks. No lazy loading needed. | Fast (< 100ms before first sync job starts) |
| **Pipeline execution** | Sequential: sync jobs in order, then cloud items per remote. | Adequate for typical personal backup sets (< 20 jobs). Could bottleneck with many independent jobs. |
| **Memory usage** | Rsync output buffered in memory (one job at a time). Rclone output streamed to file. | Minimal. Even large rsync stats output is < 1KB. |
| **Disk I/O** | One log file write per log message. Lock file opened once. | Negligible overhead vs. actual sync I/O. |
| **Process overhead** | One `exec.Command` per source per job. No connection pooling. | Each rsync/rclone invocation has process startup cost (~10-50ms). Acceptable for batch operations. |

### Bottlenecks

| Bottleneck | Severity | Description |
|------------|----------|-------------|
| **Sequential job execution** | Medium | Jobs run in config order. For J rclone jobs each targeting R remotes, total time = sum of all J*R rclone invocations. The example config has 2 rclone jobs (1 and 2 remotes respectively), yielding 3 sequential calls. |
| **No resume/checkpoint** | Low | If interrupted mid-run, the next run restarts from the beginning. Rsync and rclone handle incremental sync internally, so re-processing is fast (only diffs), but the overhead of re-checking completed items scales with job count. |
| **Stats parsing from file** | Low | Rclone stats are parsed by reading the log file after each execution. File I/O is minimal but could be replaced with pipe-based parsing for lower latency. |

### Optimization Opportunities

1. **Parallel job execution** (Large effort): Run independent sync jobs concurrently. Would require goroutines, output serialization, and careful error aggregation.
2. **Rclone output parsing via pipe** (Small effort): Parse rclone's `--stats` JSON output from stdout instead of reading the log file post-execution.
3. **Skip unchanged jobs** (Medium effort): Cache last-run timestamps per job and skip jobs where no source has been modified since. Rsync handles this internally, but skipping the process spawn entirely would save startup overhead.

---

## 9. Technical Debt

| # | Issue | Severity | Affected Files | Effort | Recommended Fix |
|---|-------|----------|----------------|--------|-----------------|
| ~~1~~ | ~~**No CI pipeline**~~ | ~~High~~ | ~~(project-level)~~ | ~~Small~~ | **Resolved.** `.github/workflows/ci.yml` with parallel test and lint jobs. Test: `go build`, `go test -race`. Lint: `golangci-lint` (via `.golangci.yml`), `govulncheck`. Triggers on push to main and PRs. |
| ~~2~~ | ~~**Rclone executor integration tests still absent**~~ | ~~Medium~~ | ~~`engine/rclone.go`~~ | ~~Medium~~ | **Resolved.** `rclone_test.go` now has 15 tests: 5 `selectMode` unit tests, 5 executor integration tests against `local` backend (copy file, copy dir, sync delete, sync backup-dir, missing source), and 5 `CleanupArchives` tests (3 guard clauses + 2 integration). |
| ~~3~~ | ~~**No CLI integration tests**~~ | ~~Medium~~ | ~~`cmd/shuttle/main.go`~~ | ~~Medium~~ | **Resolved.** `cmd/shuttle/main_test.go` has 7 exit code integration tests that build the binary via `exec.Command` and assert codes 0/1/2 for valid config, missing config, invalid config, unknown job names, and partial failure scenarios. |
| 4 | **macOS-specific rsync flag (`-E`) in example config** | Low | `config.example.toml` | Small | Document that `-E` is macOS-specific; users on Linux should omit it from `[defaults.rsync]`. |
| 5 | **No structured (JSON) output** | Low | `engine/render.go` | Medium | Add `--output json` flag. Useful for scripting and monitoring. |

---

## 10. Testing Strategy

### Current State

| Package | Test File | Framework | Coverage Level |
|---------|-----------|-----------|----------------|
| `internal/config` | `config_test.go` | `testing` (stdlib) | High: parsing, validation, tilde expansion, XDG paths |
| `internal/engine` | `runner_test.go` | `testing` | Medium: job name validation, job selection logic (table-driven subtests) |
| `internal/engine` | `rsync_test.go` | `testing` | High: real rsync integration tests with temp dirs |
| `internal/engine` | `rclone_test.go` | `testing` | High: mode selection unit tests (5 cases), executor integration tests against `local` backend (5 cases), `CleanupArchives` guard clauses and integration tests (5 cases), progress parsing |
| `internal/engine` | `flags_test.go` | `testing` | Medium: arg-list construction, tuning flags, conflict detection |
| `internal/engine` | `path_test.go` | `testing` | High: stat helper, remote detection, dest-name derivation |
| `internal/engine` | `stats_test.go` | `testing` | High: fixture-based parsing, formatting, rendering |
| `internal/engine` | `progress_test.go` | `testing` | Medium: non-interactive status lines, SkipJob output, stats formatting |
| `internal/log` | `logger_test.go` | `testing` | High: dual-stream, color, file format |
| `cmd/shuttle` | `main_test.go` | `testing` | Medium: exit code integration tests (7 cases) building binary and asserting codes 0/1/2 via `exec.Command` |

**Framework:** Go standard library `testing` package. No external test frameworks. This is idiomatic Go.

**Test patterns:**
- Fixture files in `testdata/` for stats parsers and config samples
- Real rsync calls against temp directories (integration tests, not mocked)
- Real rclone calls against `local` backend with temp directories (no remote config needed)
- CLI integration tests that build the binary and invoke it via `exec.Command`
- Table-driven tests for validation and path resolution

### What's Missing

1. ~~**CLI integration tests:** Exit codes, flag parsing, error output, signal handling.~~ **Resolved.** 7 tests in `cmd/shuttle/main_test.go`.
2. ~~**Rclone executor tests:** Flag construction, mode selection, backup-dir calculation, archive cleanup with real rclone.~~ **Resolved.** 15 tests in `rclone_test.go` (5 mode selection, 5 executor, 5 cleanup).
3. **Full pipeline test:** End-to-end Runner.Run() with controlled config, mocked or local executors.

### Recommended Minimal Testing Plan

#### ~~Before Launch (High Priority)~~ Done

| Test | Why | Effort |
|------|-----|--------|
| ~~**Rclone mode selection unit tests**~~ | ~~`selectMode()` has four branches (copy/sync x file/dir) that determine data safety. A bug here could cause data loss.~~ | ~~Small~~ |
| ~~**CLI exit code integration tests**~~ | ~~Build binary, run against valid/invalid config, assert exit codes. Verifies the contract that scripts and cron rely on.~~ | ~~Medium~~ |
| ~~**Archive cleanup date parsing tests**~~ | ~~`CleanupArchives` parses directory names for dates. A parsing bug could delete recent archives.~~ | ~~Small~~ |

**Resolved.** All three items completed: 5 `selectMode` unit tests, 7 CLI exit code tests, 5 `CleanupArchives` tests (3 guard clauses + 2 integration).

#### Post-Launch (Medium Priority)

| Test | Why | Effort |
|------|-----|--------|
| ~~**Rclone integration tests**~~ | ~~Test against `local` backend (no remote needed). Verify copy vs. sync behavior, backup-dir creation, archive cleanup lifecycle.~~ | ~~Medium~~ |
| **Signal handling test** | Spawn subprocess, send SIGINT, verify exit code 130 and clean output. | Medium |
| **Password prompt test** | Verify `promptForPassword` behavior with mock stdin. | Small |
| **Full pipeline integration test** | Runner.Run() with a minimal config, temp dirs for rsync, local backend for rclone. Assert summary correctness. | Large |

Rclone integration tests resolved: 10 tests against `local` backend cover executor operations and archive cleanup.

---

## 11. Deployment Readiness

### What Exists

| Item | Status | Notes |
|------|--------|-------|
| **Buildable binary** | Yes | `go build -o shuttle ./cmd/shuttle` |
| **Version injection** | Yes | `-ldflags "-X main.version=X.Y.Z"` |
| **CI pipeline** | Yes | GitHub Actions: parallel test + lint jobs, triggered on push to main and PRs |
| **Linter config** | Yes | `.golangci.yml` with explicit default linter set (errcheck, govet, ineffassign, staticcheck, unused) |
| **Config validation** | Yes | `shuttle validate` subcommand for config-only validation |
| **Test suite** | Yes | High coverage across all packages including CLI exit codes, rclone executor, and archive cleanup |
| **Config example** | Yes | `config.example.toml` with all sections documented, platform-neutral defaults |
| **Dev guide** | Yes | `CLAUDE.md` covers commands, architecture, testing patterns, conventions |
| **XDG compliance** | Yes | Config, logs, and state follow XDG base directory spec |
| **README.md** | Yes | Installation, quick start, configuration reference, CLI usage, platform support |
| **LICENSE** | Yes | MIT license |

### What's Missing

| Item | Priority | Effort | Notes |
|------|----------|--------|-------|
| ~~**GitHub Actions CI**~~ | ~~High~~ | ~~Small~~ | **Done.** Two parallel jobs (test + lint) with golangci-lint and govulncheck. |
| **Goreleaser config** | High | Medium | Automated binary builds, GitHub Releases, checksums |
| **Homebrew formula** | Medium | Small | After Goreleaser, publish a tap |
| **Makefile** | Low | Small | Convenience targets for build, test, lint, release |
| **Monitoring/alerting** | Low | N/A | Out of scope for a CLI tool. Users can wrap with cron alerting. |

### Pre-Production Checklist

This list is ordered by priority. Items are concrete and actionable.

- [x] **Add LICENSE file** (Small). MIT license at repo root.
- [x] **Write README.md** (Medium). Covers installation, quick start, configuration reference, CLI usage, exit codes, platform support.
- [x] **Add GitHub Actions CI** (Small). `.github/workflows/ci.yml`: parallel test (`go build`, `go test -race`) and lint (`golangci-lint`, `govulncheck`) jobs.
- [x] **Add `shuttle validate` subcommand** (Small). Parse config, run validation, print errors or `config ok: <path>`. Exit 0/2.
- [x] **Add rclone mode selection tests** (Small). 5 unit tests for `selectMode()` covering all branch combinations.
- [x] **Add CLI exit code tests** (Medium). 7 tests in `cmd/shuttle/main_test.go` building binary and asserting codes 0/1/2.
- [ ] **Add Goreleaser config** (Medium). `.goreleaser.yml` with macOS arm64/amd64, Linux amd64. GitHub Releases.
- [ ] **Add Homebrew formula** (Small). After first Goreleaser release.
- [x] **Add rclone integration tests** (Medium). 10 tests against `local` backend covering executor and archive cleanup.
- [ ] **Add full pipeline integration test** (Large). End-to-end with temp dirs and local rclone.
