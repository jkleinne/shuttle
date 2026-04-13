package engine

import (
	"testing"
)

func TestValidateJobNames_ValidNames(t *testing.T) {
	jobNames := []string{"manga", "secrets", "docs-to-cloud"}
	err := ValidateJobNames([]string{"manga"}, nil, jobNames)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateJobNames_UnknownName(t *testing.T) {
	jobNames := []string{"manga", "secrets"}
	err := ValidateJobNames([]string{"typo"}, nil, jobNames)
	if err == nil {
		t.Fatal("expected error for unknown name, got nil")
	}
}

func TestValidateJobNames_SkipAndOnlyConflict(t *testing.T) {
	jobNames := []string{"manga"}
	err := ValidateJobNames([]string{"manga"}, []string{"manga"}, jobNames)
	if err == nil {
		t.Fatal("expected error for skip+only conflict, got nil")
	}
}

func TestShouldRunJob_SkipLogic(t *testing.T) {
	tests := []struct {
		name    string
		skip    []string
		only    []string
		jobName string
		wantRun bool
	}{
		{"no filters", nil, nil, "manga", true},
		{"skip manga", []string{"manga"}, nil, "manga", false},
		{"skip manga, run secrets", []string{"manga"}, nil, "secrets", true},
		{"only docs", nil, []string{"docs"}, "manga", false},
		{"only manga", nil, []string{"manga"}, "manga", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRunJob(tt.jobName, tt.skip, tt.only)
			if got != tt.wantRun {
				t.Errorf("shouldRunJob(%q) = %v, want %v", tt.jobName, got, tt.wantRun)
			}
		})
	}
}

func TestLockFilePath_DifferentConfigs(t *testing.T) {
	r1 := &Runner{configPath: "/home/user/.config/shuttle/config.toml"}
	r2 := &Runner{configPath: "/home/user/alt/shuttle/config.toml"}
	r3 := &Runner{configPath: "/home/user/.config/shuttle/config.toml"}

	if r1.lockFilePath() == r2.lockFilePath() {
		t.Error("different config paths should produce different lock paths")
	}
	if r1.lockFilePath() != r3.lockFilePath() {
		t.Error("same config path should produce same lock path")
	}
	if r1.lockFilePath()[:13] != "/tmp/shuttle-" {
		t.Errorf("lock path should start with /tmp/shuttle-, got %q", r1.lockFilePath())
	}
}

func TestTargetRemotes_NoSelection(t *testing.T) {
	r := &Runner{}
	got := r.targetRemotes([]string{"gdrive", "koofr"}, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 remotes, got %d", len(got))
	}
}

func TestTargetRemotes_WithSelection(t *testing.T) {
	r := &Runner{}
	got := r.targetRemotes([]string{"gdrive", "koofr"}, []string{"gdrive"})
	if len(got) != 1 || got[0] != "gdrive" {
		t.Errorf("expected [gdrive], got %v", got)
	}
}

func TestTargetRemotes_SelectionNotInJob(t *testing.T) {
	r := &Runner{}
	got := r.targetRemotes([]string{"gdrive"}, []string{"onedrive"})
	if len(got) != 0 {
		t.Errorf("expected empty (no overlap), got %v", got)
	}
}
