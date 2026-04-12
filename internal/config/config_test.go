package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jkleinne/sync-station/internal/config"
)

func testdataPath(name string) string {
	// Tests run from the package directory; testdata is at repo root.
	return filepath.Join("..", "..", "testdata", name)
}

func TestLoad_ValidConfig_ParsesAllFields(t *testing.T) {
	cfg, err := config.LoadFile(testdataPath("config_valid.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ExternalDrive != "/Volumes/mybook-main" {
		t.Errorf("ExternalDrive = %q, want /Volumes/mybook-main", cfg.ExternalDrive)
	}
	if len(cfg.SyncJobs) != 2 {
		t.Fatalf("SyncJobs count = %d, want 2", len(cfg.SyncJobs))
	}
	if cfg.SyncJobs[0].Name != "manga" {
		t.Errorf("SyncJobs[0].Name = %q, want manga", cfg.SyncJobs[0].Name)
	}
	if cfg.SyncJobs[0].Delete {
		t.Error("SyncJobs[0].Delete = true, want false")
	}
	if cfg.SyncJobs[1].Delete != true {
		t.Error("SyncJobs[1].Delete = false, want true")
	}
	if len(cfg.SyncJobs[1].Sources) != 5 {
		t.Errorf("SyncJobs[1].Sources count = %d, want 5", len(cfg.SyncJobs[1].Sources))
	}
	if cfg.Cloud == nil {
		t.Fatal("Cloud is nil, want non-nil")
	}
	if cfg.Cloud.Mode != "sync" {
		t.Errorf("Cloud.Mode = %q, want sync", cfg.Cloud.Mode)
	}
	if len(cfg.Cloud.Remotes) != 2 {
		t.Errorf("Cloud.Remotes count = %d, want 2", len(cfg.Cloud.Remotes))
	}
	if len(cfg.Cloud.Items) != 9 {
		t.Errorf("Cloud.Items count = %d, want 9", len(cfg.Cloud.Items))
	}
	if cfg.Cloud.Tuning.Transfers != 6 {
		t.Errorf("Cloud.Tuning.Transfers = %d, want 6", cfg.Cloud.Tuning.Transfers)
	}
}

func TestLoad_MinimalConfig_DefaultsApplied(t *testing.T) {
	cfg, err := config.LoadFile(testdataPath("config_minimal.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.SyncJobs) != 1 {
		t.Fatalf("SyncJobs count = %d, want 1", len(cfg.SyncJobs))
	}
	if cfg.SyncJobs[0].Delete != false {
		t.Error("default Delete should be false")
	}
	if cfg.Cloud != nil {
		t.Error("Cloud should be nil when [cloud] section absent")
	}
}

func TestLoad_TildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	tomlData := `
external_drive = "/tmp/test"
[[sync]]
name = "tilde-test"
sources = ["~/Documents"]
destination = "~/backup"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantSrc := filepath.Join(home, "Documents")
	if cfg.SyncJobs[0].Sources[0] != wantSrc {
		t.Errorf("source = %q, want %q", cfg.SyncJobs[0].Sources[0], wantSrc)
	}
	wantDst := filepath.Join(home, "backup")
	if cfg.SyncJobs[0].Destination != wantDst {
		t.Errorf("destination = %q, want %q", cfg.SyncJobs[0].Destination, wantDst)
	}
}

func TestValidate_DuplicateJobNames_ReturnsError(t *testing.T) {
	_, err := config.LoadFile(testdataPath("config_invalid_duplicate_job.toml"))
	if err == nil {
		t.Fatal("expected error for duplicate job names, got nil")
	}
}

func TestValidate_JobNamedCloud_ReturnsError(t *testing.T) {
	_, err := config.LoadFile(testdataPath("config_invalid_cloud_job_name.toml"))
	if err == nil {
		t.Fatal("expected error for job named 'cloud', got nil")
	}
}

func TestValidate_InvalidMode_ReturnsError(t *testing.T) {
	_, err := config.LoadFile(testdataPath("config_invalid_mode.toml"))
	if err == nil {
		t.Fatal("expected error for invalid cloud mode, got nil")
	}
}

func TestValidate_EmptyRemoteName_ReturnsError(t *testing.T) {
	tomlData := `
external_drive = "/tmp/test"
[cloud]
mode = "sync"
[[cloud.remotes]]
name = ""
[[cloud.items]]
source = "/tmp/a"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for empty remote name, got nil")
	}
}

func TestValidate_DuplicateRemoteNames_ReturnsError(t *testing.T) {
	tomlData := `
external_drive = "/tmp/test"
[cloud]
mode = "sync"
[[cloud.remotes]]
name = "same"
[[cloud.remotes]]
name = "same"
[[cloud.items]]
source = "/tmp/a"
`
	_, err := config.LoadBytes([]byte(tomlData))
	if err == nil {
		t.Fatal("expected error for duplicate remote names, got nil")
	}
}
