package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkleinne/shuttle/internal/config"
)

func testdataPath(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func TestLoad_ValidConfig_ParsesAllFields(t *testing.T) {
	cfg, err := config.LoadFile(testdataPath("config_valid.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Defaults == nil {
		t.Fatal("Defaults is nil, want non-nil")
	}
	if cfg.Defaults.Rsync == nil {
		t.Fatal("Defaults.Rsync is nil, want non-nil")
	}
	if len(cfg.Defaults.Rsync.Flags) != 4 {
		t.Errorf("Defaults.Rsync.Flags len = %d, want 4", len(cfg.Defaults.Rsync.Flags))
	}
	if cfg.Defaults.Rclone == nil {
		t.Fatal("Defaults.Rclone is nil, want non-nil")
	}
	if cfg.Defaults.Rclone.Transfers != 4 {
		t.Errorf("Defaults.Rclone.Transfers = %d, want 4", cfg.Defaults.Rclone.Transfers)
	}
	if len(cfg.Defaults.Rclone.Flags) != 2 {
		t.Errorf("Defaults.Rclone.Flags len = %d, want 2", len(cfg.Defaults.Rclone.Flags))
	}

	if len(cfg.Jobs) != 4 {
		t.Fatalf("Jobs count = %d, want 4", len(cfg.Jobs))
	}
	if cfg.Jobs[0].Name != "photos" {
		t.Errorf("Jobs[0].Name = %q, want photos", cfg.Jobs[0].Name)
	}
	if cfg.Jobs[0].Engine != config.EngineRsync {
		t.Errorf("Jobs[0].Engine = %q, want %s", cfg.Jobs[0].Engine, config.EngineRsync)
	}
	if cfg.Jobs[0].Delete {
		t.Error("Jobs[0].Delete = true, want false")
	}
	if cfg.Jobs[1].Delete != true {
		t.Error("Jobs[1].Delete = false, want true")
	}

	// Rclone job fields
	if cfg.Jobs[2].Engine != config.EngineRclone {
		t.Errorf("Jobs[2].Engine = %q, want %s", cfg.Jobs[2].Engine, config.EngineRclone)
	}
	if cfg.Jobs[2].Mode != config.ModeSync {
		t.Errorf("Jobs[2].Mode = %q, want %s", cfg.Jobs[2].Mode, config.ModeSync)
	}
	if len(cfg.Jobs[2].Remotes) != 2 {
		t.Errorf("Jobs[2].Remotes len = %d, want 2", len(cfg.Jobs[2].Remotes))
	}
	if cfg.Jobs[2].BackupRetentionDays != 365 {
		t.Errorf("Jobs[2].BackupRetentionDays = %d, want 365", cfg.Jobs[2].BackupRetentionDays)
	}

	// Per-job tuning override
	if cfg.Jobs[3].Bwlimit != "2M" {
		t.Errorf("Jobs[3].Bwlimit = %q, want 2M", cfg.Jobs[3].Bwlimit)
	}
}

func TestLoad_MinimalConfig_NoDefaults(t *testing.T) {
	cfg, err := config.LoadFile(testdataPath("config_minimal.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Jobs) != 1 {
		t.Fatalf("Jobs count = %d, want 1", len(cfg.Jobs))
	}
	if cfg.Defaults != nil {
		t.Error("Defaults should be nil when [defaults] section absent")
	}
	if cfg.Jobs[0].Delete != false {
		t.Error("default Delete should be false")
	}
}

func TestLoad_TildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	tomlData := `
[[job]]
name = "tilde-test"
engine = "rsync"
sources = ["~/Documents"]
destination = "~/backup"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantSrc := filepath.Join(home, "Documents")
	if cfg.Jobs[0].Sources[0] != wantSrc {
		t.Errorf("source = %q, want %q", cfg.Jobs[0].Sources[0], wantSrc)
	}
	wantDst := filepath.Join(home, "backup")
	if cfg.Jobs[0].Destination != wantDst {
		t.Errorf("destination = %q, want %q", cfg.Jobs[0].Destination, wantDst)
	}
}

func TestLoad_TildeExpansion_RcloneFields(t *testing.T) {
	home, _ := os.UserHomeDir()
	tomlData := `
[defaults.rclone]
filter_file = "~/.config/rclone/filters.txt"

[[job]]
name = "cloud-tilde"
engine = "rclone"
source = "~/Documents"
remotes = ["test"]
mode = "copy"
backup_path = "~/backups/archive"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantFilter := filepath.Join(home, ".config/rclone/filters.txt")
	if cfg.Defaults.Rclone.FilterFile != wantFilter {
		t.Errorf("FilterFile = %q, want %q", cfg.Defaults.Rclone.FilterFile, wantFilter)
	}
	wantSrc := filepath.Join(home, "Documents")
	if cfg.Jobs[0].Source != wantSrc {
		t.Errorf("Source = %q, want %q", cfg.Jobs[0].Source, wantSrc)
	}
	wantBackup := filepath.Join(home, "backups/archive")
	if cfg.Jobs[0].BackupPath != wantBackup {
		t.Errorf("BackupPath = %q, want %q", cfg.Jobs[0].BackupPath, wantBackup)
	}
}

func TestValidate_DuplicateJobNames_ReturnsError(t *testing.T) {
	_, err := config.LoadFile(testdataPath("config_invalid_duplicate_job.toml"))
	if err == nil {
		t.Fatal("expected error for duplicate job names, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q should mention \"duplicate\"", err.Error())
	}
}

func TestValidate_InvalidMode_ReturnsError(t *testing.T) {
	_, err := config.LoadFile(testdataPath("config_invalid_mode.toml"))
	if err == nil {
		t.Fatal("expected error for invalid rclone mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error %q should mention \"invalid\"", err.Error())
	}
}

func TestValidate_EmptyName_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = ""
engine = "rsync"
sources = ["/tmp/a"]
destination = "/tmp/b"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	if !strings.Contains(err.Error(), "empty name") {
		t.Errorf("error %q should mention \"empty name\"", err.Error())
	}
}

func TestValidate_InvalidEngine_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = "bad"
engine = "scp"
sources = ["/tmp/a"]
destination = "/tmp/b"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for invalid engine, got nil")
	}
	if !strings.Contains(err.Error(), "invalid engine") {
		t.Errorf("error %q should mention \"invalid engine\"", err.Error())
	}
}

func TestValidate_RsyncNoSources_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = "no-src"
engine = "rsync"
sources = []
destination = "/tmp/b"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for no sources, got nil")
	}
	if !strings.Contains(err.Error(), "no sources") {
		t.Errorf("error %q should mention \"no sources\"", err.Error())
	}
}

func TestValidate_RcloneNoRemotes_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = "no-remotes"
engine = "rclone"
source = "/tmp/a"
remotes = []
mode = "copy"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for no remotes, got nil")
	}
	if !strings.Contains(err.Error(), "no remotes") {
		t.Errorf("error %q should mention \"no remotes\"", err.Error())
	}
}

func TestValidate_RcloneEmptyRemoteName_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = "empty-remote"
engine = "rclone"
source = "/tmp/a"
remotes = ["gdrive", ""]
mode = "copy"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for empty remote name, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q should mention \"empty\"", err.Error())
	}
}

func TestValidate_RcloneEmptySource_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = "no-src"
engine = "rclone"
source = ""
remotes = ["test"]
mode = "copy"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for empty source, got nil")
	}
	if !strings.Contains(err.Error(), "empty source") {
		t.Errorf("error %q should mention \"empty source\"", err.Error())
	}
}

func TestValidate_RsyncEmptyDestination_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = "no-dst"
engine = "rsync"
sources = ["/tmp/a"]
destination = ""
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for empty destination, got nil")
	}
	if !strings.Contains(err.Error(), "empty destination") {
		t.Errorf("error %q should mention \"empty destination\"", err.Error())
	}
}

func TestValidate_RcloneDuplicateRemotes_ReturnsError(t *testing.T) {
	tomlData := `
[[job]]
name = "dup-remotes"
engine = "rclone"
source = "/tmp/a"
remotes = ["same", "same"]
mode = "copy"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for duplicate remotes, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate remote") {
		t.Errorf("error %q should mention \"duplicate remote\"", err.Error())
	}
}

func TestJobNames_ReturnsNamesInOrder(t *testing.T) {
	tomlData := `
[[job]]
name = "alpha"
engine = "rsync"
sources = ["/tmp/a"]
destination = "/tmp/dst"

[[job]]
name = "beta"
engine = "rsync"
sources = ["/tmp/b"]
destination = "/tmp/dst"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.JobNames()
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("JobNames() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("JobNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAllRemoteNames_ReturnsUnion(t *testing.T) {
	tomlData := `
[[job]]
name = "job1"
engine = "rclone"
source = "/tmp/a"
remotes = ["gdrive", "koofr"]
mode = "copy"

[[job]]
name = "job2"
engine = "rclone"
source = "/tmp/b"
remotes = ["gdrive", "onedrive"]
mode = "copy"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.AllRemoteNames()
	// Deduplicated, first-seen order: gdrive, koofr, onedrive
	want := []string{"gdrive", "koofr", "onedrive"}
	if len(got) != len(want) {
		t.Fatalf("AllRemoteNames() len = %d, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("AllRemoteNames()[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestAllRemoteNames_NoRcloneJobs_ReturnsNil(t *testing.T) {
	tomlData := `
[[job]]
name = "rsync-only"
engine = "rsync"
sources = ["/tmp/src"]
destination = "/tmp/dst"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.AllRemoteNames(); got != nil {
		t.Errorf("AllRemoteNames() = %v, want nil when no rclone jobs", got)
	}
}

func TestLogRetentionDays_Unset_ReturnsDefault(t *testing.T) {
	tomlData := `
[[job]]
name = "j"
engine = "rsync"
sources = ["/tmp/a"]
destination = "/tmp/b"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.ResolvedLogRetentionDays()
	if got != config.DefaultLogRetentionDays {
		t.Errorf("ResolvedLogRetentionDays() = %d, want %d", got, config.DefaultLogRetentionDays)
	}
}

func TestLogRetentionDays_ExplicitZero_DisablesPruning(t *testing.T) {
	tomlData := `
[defaults]
log_retention_days = 0

[[job]]
name = "j"
engine = "rsync"
sources = ["/tmp/a"]
destination = "/tmp/b"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.ResolvedLogRetentionDays(); got != 0 {
		t.Errorf("ResolvedLogRetentionDays() = %d, want 0", got)
	}
}

func TestLogRetentionDays_ExplicitPositive_UsesValue(t *testing.T) {
	tomlData := `
[defaults]
log_retention_days = 7

[[job]]
name = "j"
engine = "rsync"
sources = ["/tmp/a"]
destination = "/tmp/b"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.ResolvedLogRetentionDays(); got != 7 {
		t.Errorf("ResolvedLogRetentionDays() = %d, want 7", got)
	}
}

func TestLogRetentionDays_Negative_ReturnsError(t *testing.T) {
	tomlData := `
[defaults]
log_retention_days = -1

[[job]]
name = "j"
engine = "rsync"
sources = ["/tmp/a"]
destination = "/tmp/b"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for negative log_retention_days, got nil")
	}
	if !strings.Contains(err.Error(), "log_retention_days") {
		t.Errorf("error %q should mention \"log_retention_days\"", err.Error())
	}
}

func TestLogRetentionDays_NoDefaultsSection_ReturnsDefault(t *testing.T) {
	cfg, err := config.LoadBytes([]byte(``))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.ResolvedLogRetentionDays(); got != config.DefaultLogRetentionDays {
		t.Errorf("ResolvedLogRetentionDays() = %d, want %d", got, config.DefaultLogRetentionDays)
	}
}

func TestLoad_NoJobs_EmptySlice(t *testing.T) {
	tomlData := ``
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Jobs) != 0 {
		t.Errorf("Jobs len = %d, want 0", len(cfg.Jobs))
	}
}

func TestLoad_XDGConfigHome_ResolvesPath(t *testing.T) {
	tmpDir := t.TempDir()
	confDir := filepath.Join(tmpDir, "shuttle")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	tomlData := `
[[job]]
name = "xdg-job"
engine = "rsync"
sources = ["/tmp/src"]
destination = "/tmp/dst"
`
	if err := os.WriteFile(filepath.Join(confDir, "config.toml"), []byte(tomlData), 0o644); err != nil {
		t.Fatalf("writing config file: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Jobs) != 1 || cfg.Jobs[0].Name != "xdg-job" {
		t.Errorf("Jobs = %v, want one job named xdg-job", cfg.Jobs)
	}
}
