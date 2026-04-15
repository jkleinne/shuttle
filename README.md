# Shuttle

Backup and sync CLI that orchestrates [rsync](https://rsync.samba.org/) and [rclone](https://rclone.org/) through a single TOML config file. Define your local sync jobs and cloud uploads in one place, run them with one command, and get a summary of what happened.

## Features

- **One config, two engines.** Rsync jobs (local sync) and rclone jobs (cloud upload) live in the same TOML file.
- **Partial failure resilience.** A broken source doesn't abort the run. Failed items are logged and summarized; everything else completes.
- **Archive-safe cloud sync.** Sync mode with `backup_path` moves deleted files to timestamped archive directories on the remote, with configurable retention and automatic cleanup.
- **Runtime job selection.** `--skip`, `--only`, and `--remote` flags let you run subsets without editing config.
- **Dry-run preview.** See what would change before committing.
- **Live progress.** Spinner with transfer stats on interactive terminals, plain status lines in pipes and cron.
- **Dual logging.** Colored terminal output and a timestamped plain-text log file.
- **Exclusive locking.** Per-config flock prevents concurrent runs of the same pipeline.

## Requirements

- **Go 1.26+** (to build from source)
- **rsync** (for local sync jobs)
- **rclone** (for cloud jobs, must be [configured](https://rclone.org/docs/) with your remotes beforehand)
- **macOS or Linux.** Windows is not supported.

## Installation

### Homebrew (macOS, Linux)

```bash
brew install jkleinne/tools/shuttle
```

This installs the latest release binary and pulls in `rsync` and `rclone` if they are not already on your system.

### Prebuilt binaries

Download the archive for your platform from the [Releases page](https://github.com/jkleinne/shuttle/releases) and extract the `shuttle` binary onto your `PATH`.

### From source

```bash
go install github.com/jkleinne/shuttle/cmd/shuttle@latest
```

Or build manually:

```bash
git clone https://github.com/jkleinne/shuttle.git
cd shuttle
go build -o shuttle ./cmd/shuttle
```

To embed a version string:

```bash
go build -ldflags "-X main.version=1.0.0" -o shuttle ./cmd/shuttle
```

## Quick Start

1. Copy the example config to your config directory:

   ```bash
   mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/shuttle"
   cp config.example.toml "${XDG_CONFIG_HOME:-$HOME/.config}/shuttle/config.toml"
   ```

2. Edit the config to match your paths and remotes. See [Configuration](#configuration) below.

3. Preview what will happen:

   ```bash
   shuttle --dry-run
   ```

4. Run for real:

   ```bash
   shuttle
   ```

## Usage

```
shuttle [flags]          Run the sync pipeline (same as `shuttle run`)
shuttle run [flags]      Run the sync pipeline
shuttle version          Print version
```

### Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--dry-run` | `-n` | Preview changes without modifying files |
| `--skip <name>` | | Skip a job by name (repeatable, mutually exclusive with `--only`) |
| `--only <name>` | | Run only the named job(s) (repeatable, mutually exclusive with `--skip`) |
| `--remote <name>` | | Target a specific cloud remote (repeatable) |
| `--color <when>` | | Colorize terminal output: `auto` (default), `always`, or `never`. The `NO_COLOR` environment variable always forces color off. |
| `--quiet` | `-q` | Suppress stdout on success; on failure, route summary and log path to stderr. Mutually exclusive with `--verbose`. |
| `--verbose` | `-v` | Print executed commands (`exec: rsync ...` / `exec: rclone ...`) in addition to normal output. Mutually exclusive with `--quiet`. |

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | All jobs succeeded |
| 1 | Partial failure (some items failed, others completed) |
| 2 | Config or usage error |
| 130 | Interrupted by signal (SIGINT/SIGTERM) |

## Configuration

Config lives at `${XDG_CONFIG_HOME:-~/.config}/shuttle/config.toml`. See [`config.example.toml`](config.example.toml) for a complete annotated example.

### Defaults

Optional baseline settings applied to all jobs of a given engine. Per-job fields override these.

**`[defaults]`** (cross-cutting)

| Field | Type | Description |
|-------|------|-------------|
| `log_retention_days` | int | Age (in days) after which per-run log files are pruned on startup. Defaults to 30. Set to `0` to disable pruning. Negative values are rejected. |

**`[defaults.rsync]`**

| Field | Type | Description |
|-------|------|-------------|
| `flags` | string array | Flags passed to every rsync invocation |

**`[defaults.rclone]`**

| Field | Type | Description |
|-------|------|-------------|
| `flags` | string array | Flags passed to every rclone invocation |
| `filter_file` | string | Path to an rclone filter file |
| `transfers` | int | Number of parallel file transfers |
| `checkers` | int | Number of parallel checkers |
| `bwlimit` | string | Bandwidth limit (e.g. `"10M"`) |
| `drive_chunk_size` | string | Chunk size for drive uploads |
| `buffer_size` | string | In-memory buffer per transfer |
| `use_mmap` | bool | Use memory-mapped I/O |
| `timeout` | string | I/O idle timeout |
| `contimeout` | string | Connection timeout |
| `low_level_retries` | int | Low-level retry count |
| `order_by` | string | Transfer order (e.g. `"modtime,asc"`) |

### Jobs

Each `[[job]]` entry defines one sync operation.

**Common fields (all jobs)**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique job name |
| `engine` | string | yes | `"rsync"` or `"rclone"` |
| `extra_flags` | string array | no | Additional flags appended after defaults |

**Rsync jobs** (`engine = "rsync"`)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `sources` | string array | yes | Paths to sync from |
| `destination` | string | yes | Path to sync to |
| `delete` | bool | no | Delete extraneous files from destination |

**Rclone jobs** (`engine = "rclone"`)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `source` | string | yes | Local path or rclone remote to sync from |
| `remotes` | string array | yes | Target rclone remotes (must be pre-configured via `rclone config`) |
| `mode` | string | yes | `"copy"` (add files) or `"sync"` (mirror, may delete) |
| `backup_path` | string | no | Remote path prefix for archived deleted files (sync mode only) |
| `backup_retention_days` | int | no | Days to keep archived files before cleanup |
| `filter_file` | string | no | Per-job filter file (overrides default) |

Rclone jobs also accept all tuning fields from `[defaults.rclone]` as per-job overrides (`transfers`, `bwlimit`, etc.).

### Example

```toml
[defaults.rsync]
flags = ["-a", "-v", "-h", "-P", "-u"]

[defaults.rclone]
flags = ["--copy-links", "--fast-list"]
transfers = 4
bwlimit = "10M"

[[job]]
name = "photos"
engine = "rsync"
sources = ["~/photos/"]
destination = "/mnt/backup/photos/"

[[job]]
name = "docs-to-cloud"
engine = "rclone"
source = "~/documents"
remotes = ["my_gdrive", "my_s3"]
mode = "sync"
backup_path = "_archive"
backup_retention_days = 365
```

## Logging

Logs are written to `${XDG_STATE_HOME:-~/.local/state}/shuttle/logs/`. Each run creates a timestamped log file. The path is printed at the end of every run.

At startup Shuttle prunes log files older than `log_retention_days` (default 30) so the directory does not grow unbounded under regular cron use. Pruning is best-effort: a failure on any individual file is recorded as a warning and does not block the backup.

## Encrypted Rclone Remotes

If your rclone config is encrypted, Shuttle prompts for the password on interactive terminals. For unattended runs (cron, launchd), set `RCLONE_CONFIG_PASS` in the environment.

## Platform Support

Shuttle runs on **macOS** and **Linux**. Windows is not supported (the locking mechanism uses Unix `flock`).

The example config uses generic Unix paths. macOS users may want to add `"--exclude=.DS_Store"` to their `[defaults.rsync]` flags.

## License

[MIT](LICENSE)
