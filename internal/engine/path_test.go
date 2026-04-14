package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatPath_Directory(t *testing.T) {
	dir := t.TempDir()
	resolved, isDir, err := statPath(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != dir {
		t.Errorf("resolved = %q, want %q", resolved, dir)
	}
	if !isDir {
		t.Error("isDir = false, want true")
	}
}

func TestStatPath_File(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	resolved, isDir, err := statPath(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != f {
		t.Errorf("resolved = %q, want %q", resolved, f)
	}
	if isDir {
		t.Error("isDir = true, want false")
	}
}

func TestStatPath_NotFound(t *testing.T) {
	_, _, err := statPath("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
}

func TestIsRcloneRemote(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"my_gdrive:Documents", true},
		{"/absolute/path", false},
		{"~/home/path", false},
		{"remote:", true},
	}
	for _, tt := range tests {
		if got := isRcloneRemote(tt.source); got != tt.want {
			t.Errorf("isRcloneRemote(%q) = %v, want %v", tt.source, got, tt.want)
		}
	}
}

func TestRcloneDestName(t *testing.T) {
	tests := []struct {
		name     string
		jobDest  string
		source   string
		isRemote bool
		want     string
	}{
		{"explicit destination", "custom", "/tmp/src", false, "custom"},
		{"local source basename", "", "/tmp/Documents/", false, "Documents"},
		{"remote source path", "", "remote:path/to/docs", true, "docs"},
		{"remote root", "", "remote:/", true, ""},
		{"remote bare", "", "remote:", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rcloneDestName(tt.jobDest, tt.source, tt.isRemote)
			if got != tt.want {
				t.Errorf("rcloneDestName() = %q, want %q", got, tt.want)
			}
		})
	}
}
