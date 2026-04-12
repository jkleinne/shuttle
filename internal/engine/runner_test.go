package engine

import (
	"testing"
)

func TestValidateJobNames_ValidNames(t *testing.T) {
	jobNames := []string{"manga", "secrets"}
	err := ValidateJobNames([]string{"manga", "cloud"}, nil, jobNames)
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
	err := ValidateJobNames([]string{"manga"}, []string{"cloud"}, jobNames)
	if err == nil {
		t.Fatal("expected error for skip+only conflict, got nil")
	}
}

func TestValidateJobNames_CloudReserved(t *testing.T) {
	jobNames := []string{"manga"}
	err := ValidateJobNames(nil, []string{"cloud"}, jobNames)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
		{"only cloud", nil, []string{"cloud"}, "manga", false},
		{"only manga", nil, []string{"manga"}, "manga", true},
		{"skip cloud, run manga", []string{"cloud"}, nil, "manga", true},
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
