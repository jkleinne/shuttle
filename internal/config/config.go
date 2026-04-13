// Package config handles TOML configuration loading and validation
// for shuttle. It exposes Load, LoadFile, and LoadBytes as the
// public API so callers never need to touch the TOML library directly.
//
// Path expansion (tilde to home directory) is applied to all path fields
// immediately after parsing, before validation.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Engine constants for Job.Engine field.
const (
	EngineRsync  = "rsync"
	EngineRclone = "rclone"
)

// ModeCopy and ModeSync are the valid values for rclone Job.Mode.
const (
	ModeCopy = "copy"
	ModeSync = "sync"
)

// Config is the top-level configuration for shuttle.
type Config struct {
	Defaults *Defaults `toml:"defaults"`
	Jobs     []Job     `toml:"job"`
}

// Defaults holds baseline configuration for each engine.
// Per-job fields override these values.
type Defaults struct {
	Rsync  *RsyncDefaults  `toml:"rsync"`
	Rclone *RcloneDefaults `toml:"rclone"`
}

// RsyncDefaults holds default flags for all rsync jobs.
type RsyncDefaults struct {
	Flags []string `toml:"flags"`
}

// RcloneDefaults holds default flags and tuning for all rclone jobs.
type RcloneDefaults struct {
	Flags           []string `toml:"flags"`
	FilterFile      string   `toml:"filter_file"`
	Transfers       int      `toml:"transfers"`
	Checkers        int      `toml:"checkers"`
	Bwlimit         string   `toml:"bwlimit"`
	DriveChunkSize  string   `toml:"drive_chunk_size"`
	BufferSize      string   `toml:"buffer_size"`
	UseMmap         bool     `toml:"use_mmap"`
	Timeout         string   `toml:"timeout"`
	Contimeout      string   `toml:"contimeout"`
	LowLevelRetries int      `toml:"low_level_retries"`
	OrderBy         string   `toml:"order_by"`
}

// Job defines a single backup/sync operation. The Engine field determines
// which fields are relevant: rsync jobs use Sources/Destination/Delete,
// rclone jobs use Source/Remotes/Mode and tuning overrides.
type Job struct {
	// Common fields
	Name       string   `toml:"name"`
	Engine     string   `toml:"engine"`
	ExtraFlags []string `toml:"extra_flags"`

	// Rsync fields
	Sources     []string `toml:"sources"`
	Destination string   `toml:"destination"`
	Delete      bool     `toml:"delete"`

	// Rclone fields
	Source              string   `toml:"source"`
	Remotes             []string `toml:"remotes"`
	Mode                string   `toml:"mode"`
	BackupPath          string   `toml:"backup_path"`
	BackupRetentionDays int      `toml:"backup_retention_days"`
	FilterFile          string   `toml:"filter_file"`

	// Rclone per-job tuning overrides
	Transfers       int    `toml:"transfers"`
	Checkers        int    `toml:"checkers"`
	Bwlimit         string `toml:"bwlimit"`
	DriveChunkSize  string `toml:"drive_chunk_size"`
	BufferSize      string `toml:"buffer_size"`
	UseMmap         bool   `toml:"use_mmap"`
	Timeout         string `toml:"timeout"`
	Contimeout      string `toml:"contimeout"`
	LowLevelRetries int    `toml:"low_level_retries"`
	OrderBy         string `toml:"order_by"`
}

// LoadFile reads and parses a TOML config file from disk.
// It returns an error with path context if the file cannot be read or parsed.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	return LoadBytes(data)
}

// LoadBytes parses TOML config from raw bytes, expands tilde paths, and validates.
// Useful for testing or when config content is already in memory.
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

// Load finds and loads the config from the XDG config path:
// ${XDG_CONFIG_HOME:-~/.config}/shuttle/config.toml.
// Returns an empty Config (no jobs) if no file exists yet.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	_, err = os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("checking config %s: %w", path, err)
	}
	return LoadFile(path)
}

// JobNames returns the names of all configured jobs in config order.
func (c *Config) JobNames() []string {
	names := make([]string, len(c.Jobs))
	for i, job := range c.Jobs {
		names[i] = job.Name
	}
	return names
}

// AllRemoteNames returns the deduplicated union of all rclone jobs'
// remote names, in first-seen order. Returns nil if no rclone jobs exist.
func (c *Config) AllRemoteNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, job := range c.Jobs {
		if job.Engine != EngineRclone {
			continue
		}
		for _, r := range job.Remotes {
			if !seen[r] {
				seen[r] = true
				names = append(names, r)
			}
		}
	}
	return names
}

// ConfigPath returns the canonical config file path, respecting XDG_CONFIG_HOME.
// Exported so the CLI can pass it to the runner for per-config locking.
func ConfigPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "shuttle", "config.toml"), nil
}

// expandTilde replaces a leading "~" with the user's home directory.
// Paths that do not start with "~" are returned unchanged.
// Returns an error if the path starts with "~" and os.UserHomeDir fails.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory for path %q: %w", path, err)
	}
	return filepath.Join(home, path[1:]), nil
}

// expandPaths applies tilde expansion to all path fields in the config.
// Returns an error if any tilde expansion fails.
func (c *Config) expandPaths() error {
	var err error

	if c.Defaults != nil {
		if c.Defaults.Rclone != nil {
			if c.Defaults.Rclone.FilterFile, err = expandTilde(c.Defaults.Rclone.FilterFile); err != nil {
				return err
			}
		}
	}

	for i := range c.Jobs {
		job := &c.Jobs[i]
		// Rsync paths
		for j := range job.Sources {
			if job.Sources[j], err = expandTilde(job.Sources[j]); err != nil {
				return err
			}
		}
		if job.Destination, err = expandTilde(job.Destination); err != nil {
			return err
		}
		// Rclone paths
		if job.Source, err = expandTilde(job.Source); err != nil {
			return err
		}
		if job.BackupPath, err = expandTilde(job.BackupPath); err != nil {
			return err
		}
		if job.FilterFile, err = expandTilde(job.FilterFile); err != nil {
			return err
		}
	}

	return nil
}

// validate enforces all structural constraints on the parsed config.
// It returns the first violation encountered, with enough context to act on it.
func (c *Config) validate() error {
	seen := make(map[string]bool, len(c.Jobs))
	for _, job := range c.Jobs {
		if job.Name == "" {
			return fmt.Errorf("job has empty name")
		}
		if seen[job.Name] {
			return fmt.Errorf("duplicate job name %q", job.Name)
		}
		seen[job.Name] = true

		switch job.Engine {
		case EngineRsync:
			if err := validateRsyncJob(job); err != nil {
				return err
			}
		case EngineRclone:
			if err := validateRcloneJob(job); err != nil {
				return err
			}
		default:
			return fmt.Errorf("job %q: invalid engine %q, must be %q or %q",
				job.Name, job.Engine, EngineRsync, EngineRclone)
		}
	}
	return nil
}

func validateRsyncJob(job Job) error {
	if len(job.Sources) == 0 {
		return fmt.Errorf("job %q: no sources", job.Name)
	}
	if job.Destination == "" {
		return fmt.Errorf("job %q: empty destination", job.Name)
	}
	return nil
}

func validateRcloneJob(job Job) error {
	if job.Source == "" {
		return fmt.Errorf("job %q: empty source", job.Name)
	}
	if len(job.Remotes) == 0 {
		return fmt.Errorf("job %q: no remotes", job.Name)
	}
	if job.Mode != ModeCopy && job.Mode != ModeSync {
		return fmt.Errorf("job %q: invalid mode %q, must be %q or %q",
			job.Name, job.Mode, ModeCopy, ModeSync)
	}
	remoteSeen := make(map[string]bool, len(job.Remotes))
	for _, r := range job.Remotes {
		if remoteSeen[r] {
			return fmt.Errorf("job %q: duplicate remote %q", job.Name, r)
		}
		remoteSeen[r] = true
	}
	return nil
}
