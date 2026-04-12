package engine

import (
	"testing"
)

func TestValidateJobNames_ValidNames(t *testing.T) {
	jobNames := []string{"photos", "projects"}
	err := ValidateJobNames([]string{"photos", "cloud"}, nil, jobNames)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateJobNames_UnknownName(t *testing.T) {
	jobNames := []string{"photos", "projects"}
	err := ValidateJobNames([]string{"typo"}, nil, jobNames)
	if err == nil {
		t.Fatal("expected error for unknown name, got nil")
	}
}

func TestValidateJobNames_SkipAndOnlyConflict(t *testing.T) {
	jobNames := []string{"photos"}
	err := ValidateJobNames([]string{"photos"}, []string{"cloud"}, jobNames)
	if err == nil {
		t.Fatal("expected error for skip+only conflict, got nil")
	}
}

func TestValidateJobNames_CloudReserved(t *testing.T) {
	jobNames := []string{"photos"}
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
		{"no filters", nil, nil, "photos", true},
		{"skip photos", []string{"photos"}, nil, "photos", false},
		{"skip photos, run secrets", []string{"photos"}, nil, "projects", true},
		{"only cloud", nil, []string{"cloud"}, "photos", false},
		{"only photos", nil, []string{"photos"}, "photos", true},
		{"skip cloud, run photos", []string{"cloud"}, nil, "photos", true},
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
