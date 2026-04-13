package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkleinne/shuttle/internal/config"
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

	if cfg.ExternalDrive != "/Volumes/Backup" {
		t.Errorf("ExternalDrive = %q, want /Volumes/Backup", cfg.ExternalDrive)
	}
	if len(cfg.SyncJobs) != 2 {
		t.Fatalf("SyncJobs count = %d, want 2", len(cfg.SyncJobs))
	}
	if cfg.SyncJobs[0].Name != "photos" {
		t.Errorf("SyncJobs[0].Name = %q, want photos", cfg.SyncJobs[0].Name)
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
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q should mention \"duplicate\"", err.Error())
	}
}

func TestValidate_JobNamedCloud_ReturnsError(t *testing.T) {
	_, err := config.LoadFile(testdataPath("config_invalid_cloud_job_name.toml"))
	if err == nil {
		t.Fatal("expected error for job named 'cloud', got nil")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error %q should mention \"reserved\"", err.Error())
	}
}

func TestValidate_InvalidMode_ReturnsError(t *testing.T) {
	_, err := config.LoadFile(testdataPath("config_invalid_mode.toml"))
	if err == nil {
		t.Fatal("expected error for invalid cloud mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error %q should mention \"invalid\"", err.Error())
	}
	if !strings.Contains(err.Error(), "mirror") {
		t.Errorf("error %q should mention the bad mode value \"mirror\"", err.Error())
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
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q should mention \"empty\"", err.Error())
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
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q should mention \"duplicate\"", err.Error())
	}
}

func TestLoad_XDGConfigHome_ResolvesPath(t *testing.T) {
	tmpDir := t.TempDir()
	confDir := filepath.Join(tmpDir, "shuttle")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	tomlData := `
external_drive = "/tmp/xdg-test"
[[sync]]
name = "xdg-job"
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
	if cfg.ExternalDrive != "/tmp/xdg-test" {
		t.Errorf("ExternalDrive = %q, want /tmp/xdg-test", cfg.ExternalDrive)
	}
	if len(cfg.SyncJobs) != 1 || cfg.SyncJobs[0].Name != "xdg-job" {
		t.Errorf("SyncJobs = %v, want one job named xdg-job", cfg.SyncJobs)
	}
}

func TestJobNames_ReturnsNamesInOrder(t *testing.T) {
	tomlData := `
external_drive = "/tmp/test"
[[sync]]
name = "alpha"
sources = ["/tmp/a"]
destination = "/tmp/dst"
[[sync]]
name = "beta"
sources = ["/tmp/b"]
destination = "/tmp/dst"
[[sync]]
name = "gamma"
sources = ["/tmp/c"]
destination = "/tmp/dst"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.JobNames()
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("JobNames() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("JobNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRemoteNames_ReturnsNamesInOrder(t *testing.T) {
	tomlData := `
external_drive = "/tmp/test"
[cloud]
mode = "copy"
[[cloud.remotes]]
name = "gdrive"
[[cloud.remotes]]
name = "onedrive"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.RemoteNames()
	want := []string{"gdrive", "onedrive"}
	if len(got) != len(want) {
		t.Fatalf("RemoteNames() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RemoteNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRemoteNames_NilCloud_ReturnsNil(t *testing.T) {
	tomlData := `
external_drive = "/tmp/test"
[[sync]]
name = "no-cloud"
sources = ["/tmp/src"]
destination = "/tmp/dst"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.RemoteNames(); got != nil {
		t.Errorf("RemoteNames() = %v, want nil when Cloud is nil", got)
	}
}

func TestLoad_NoSyncJobs_EmptySlice(t *testing.T) {
	tomlData := `
external_drive = "/tmp/drive"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.SyncJobs) != 0 {
		t.Errorf("SyncJobs len = %d, want 0", len(cfg.SyncJobs))
	}
}

func TestLoad_TildeExpansion_CloudAndExternalDrive(t *testing.T) {
	home, _ := os.UserHomeDir()
	tomlData := `
external_drive = "~/Volumes/backup"
[cloud]
mode = "sync"
backup_path = "~/backups/archive"
[[cloud.items]]
source = "~/Documents/notes"
destination = "~/Cloud/notes"
`
	cfg, err := config.LoadBytes([]byte(tomlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantDrive := filepath.Join(home, "Volumes/backup")
	if cfg.ExternalDrive != wantDrive {
		t.Errorf("ExternalDrive = %q, want %q", cfg.ExternalDrive, wantDrive)
	}
	wantBackup := filepath.Join(home, "backups/archive")
	if cfg.Cloud.BackupPath != wantBackup {
		t.Errorf("Cloud.BackupPath = %q, want %q", cfg.Cloud.BackupPath, wantBackup)
	}
	wantSrc := filepath.Join(home, "Documents/notes")
	if cfg.Cloud.Items[0].Source != wantSrc {
		t.Errorf("Cloud.Items[0].Source = %q, want %q", cfg.Cloud.Items[0].Source, wantSrc)
	}
	wantDst := filepath.Join(home, "Cloud/notes")
	if cfg.Cloud.Items[0].Destination != wantDst {
		t.Errorf("Cloud.Items[0].Destination = %q, want %q", cfg.Cloud.Items[0].Destination, wantDst)
	}
}
