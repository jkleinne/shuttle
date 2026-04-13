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

// reservedJobName is the sync job name reserved for internal use.
const reservedJobName = "cloud"

// ModeCopy and ModeSync are the only valid values for CloudConfig.Mode.
const ModeCopy = "copy"
const ModeSync = "sync"

// Config is the top-level configuration for shuttle.
// SyncJobs maps to [[sync]] TOML array-of-tables; Cloud is optional.
type Config struct {
	ExternalDrive string      `toml:"external_drive"`
	SyncJobs      []SyncJob   `toml:"sync"`
	Cloud         *CloudConfig `toml:"cloud"`
}

// SyncJob defines an rsync-based local sync operation.
// Delete controls whether rsync uses --delete; ExtraOpts are appended verbatim.
type SyncJob struct {
	Name        string   `toml:"name"`
	Sources     []string `toml:"sources"`
	Destination string   `toml:"destination"`
	Delete      bool     `toml:"delete"`
	ExtraOpts   []string `toml:"extra_opts"`
}

// CloudConfig holds all rclone cloud sync configuration.
// Mode must be "copy" or "sync". BackupPath of "" disables backup archiving.
type CloudConfig struct {
	Mode                string      `toml:"mode"`
	BackupPath          string      `toml:"backup_path"`
	BackupRetentionDays int         `toml:"backup_retention_days"`
	RemotePath          string      `toml:"remote_path"`
	Tuning              CloudTuning `toml:"tuning"`
	Remotes             []Remote    `toml:"remotes"`
	Items               []CloudItem `toml:"items"`
}

// CloudTuning holds rclone performance tuning parameters.
// All string fields (Bwlimit, DriveChunkSize, etc.) are passed directly to rclone flags.
type CloudTuning struct {
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

// Remote identifies a configured rclone remote by name.
type Remote struct {
	Name string `toml:"name"`
}

// CloudItem defines a source path to upload to all configured cloud remotes.
// Destination is optional; when empty the runner derives it from the source basename.
type CloudItem struct {
	Source      string `toml:"source"`
	Destination string `toml:"destination"`
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
// Returns an empty Config (no jobs, no cloud) if no file exists yet.
func Load() (*Config, error) {
	path, err := configPath()
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

// JobNames returns the names of all configured sync jobs in config order.
func (c *Config) JobNames() []string {
	names := make([]string, len(c.SyncJobs))
	for i, job := range c.SyncJobs {
		names[i] = job.Name
	}
	return names
}

// RemoteNames returns the names of all configured cloud remotes, or nil if
// no [cloud] section is present.
func (c *Config) RemoteNames() []string {
	if c.Cloud == nil {
		return nil
	}
	names := make([]string, len(c.Cloud.Remotes))
	for i, r := range c.Cloud.Remotes {
		names[i] = r.Name
	}
	return names
}

// configPath returns the canonical config file path, respecting XDG_CONFIG_HOME.
// It returns an error if XDG_CONFIG_HOME is unset and os.UserHomeDir fails.
func configPath() (string, error) {
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
	if c.ExternalDrive, err = expandTilde(c.ExternalDrive); err != nil {
		return err
	}

	for i := range c.SyncJobs {
		for j := range c.SyncJobs[i].Sources {
			if c.SyncJobs[i].Sources[j], err = expandTilde(c.SyncJobs[i].Sources[j]); err != nil {
				return err
			}
		}
		if c.SyncJobs[i].Destination, err = expandTilde(c.SyncJobs[i].Destination); err != nil {
			return err
		}
	}

	if c.Cloud != nil {
		if c.Cloud.BackupPath, err = expandTilde(c.Cloud.BackupPath); err != nil {
			return err
		}
		for i := range c.Cloud.Items {
			if c.Cloud.Items[i].Source, err = expandTilde(c.Cloud.Items[i].Source); err != nil {
				return err
			}
			if c.Cloud.Items[i].Destination, err = expandTilde(c.Cloud.Items[i].Destination); err != nil {
				return err
			}
		}
	}

	return nil
}

// validate enforces all structural constraints on the parsed config.
// It returns the first violation encountered, with enough context to act on it.
func (c *Config) validate() error {
	seen := make(map[string]bool, len(c.SyncJobs))
	for _, job := range c.SyncJobs {
		if job.Name == "" {
			return fmt.Errorf("sync job has empty name")
		}
		if job.Name == reservedJobName {
			return fmt.Errorf("sync job name %q is reserved", job.Name)
		}
		if seen[job.Name] {
			return fmt.Errorf("duplicate sync job name %q", job.Name)
		}
		seen[job.Name] = true

		if len(job.Sources) == 0 {
			return fmt.Errorf("sync job %q has no sources", job.Name)
		}
		if job.Destination == "" {
			return fmt.Errorf("sync job %q has empty destination", job.Name)
		}
	}

	if c.Cloud == nil {
		return nil
	}

	if c.Cloud.Mode != ModeCopy && c.Cloud.Mode != ModeSync {
		return fmt.Errorf("invalid cloud mode %q: must be %q or %q", c.Cloud.Mode, ModeCopy, ModeSync)
	}

	remoteNames := make(map[string]bool, len(c.Cloud.Remotes))
	for _, r := range c.Cloud.Remotes {
		if r.Name == "" {
			return fmt.Errorf("cloud remote has empty name")
		}
		if remoteNames[r.Name] {
			return fmt.Errorf("duplicate cloud remote name %q", r.Name)
		}
		remoteNames[r.Name] = true
	}

	return nil
}
