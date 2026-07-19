package engine

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutableFixture(t *testing.T, directory, name string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing executable fixture %s: %v", name, err)
	}
}

func TestSystemPrerequisiteChecker_Check(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*testing.T, string) PrerequisiteRequest
		wantMessage string
		wantCause   error
	}{
		{
			name: "missing rsync",
			setup: func(_ *testing.T, _ string) PrerequisiteRequest {
				return PrerequisiteRequest{NeedsRsync: true}
			},
			wantMessage: "rsync",
			wantCause:   exec.ErrNotFound,
		},
		{
			name: "missing rclone",
			setup: func(_ *testing.T, _ string) PrerequisiteRequest {
				return PrerequisiteRequest{NeedsRclone: true}
			},
			wantMessage: "rclone",
			wantCause:   exec.ErrNotFound,
		},
		{
			name: "missing filter file",
			setup: func(t *testing.T, directory string) PrerequisiteRequest {
				writeExecutableFixture(t, directory, "rclone")
				return PrerequisiteRequest{
					NeedsRclone: true,
					FilterFiles: []string{filepath.Join(directory, "missing-filter.txt")},
				}
			},
			wantMessage: "missing-filter.txt",
			wantCause:   fs.ErrNotExist,
		},
		{
			name: "all present",
			setup: func(t *testing.T, directory string) PrerequisiteRequest {
				writeExecutableFixture(t, directory, "rsync")
				writeExecutableFixture(t, directory, "rclone")
				filterPath := filepath.Join(directory, "filter.txt")
				if err := os.WriteFile(filterPath, []byte("- secret\n"), 0o600); err != nil {
					t.Fatalf("writing filter fixture: %v", err)
				}
				return PrerequisiteRequest{
					NeedsRsync:  true,
					NeedsRclone: true,
					FilterFiles: []string{filterPath},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pathDirectory := t.TempDir()
			t.Setenv("PATH", pathDirectory)
			request := tt.setup(t, pathDirectory)

			err := NewSystemPrerequisiteChecker().Check(request)

			if tt.wantCause == nil {
				if err != nil {
					t.Fatalf("Check() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Check() error = nil, want cause %v", tt.wantCause)
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMessage)
			}
			if !errors.Is(err, tt.wantCause) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.wantCause)
			}
		})
	}
}
