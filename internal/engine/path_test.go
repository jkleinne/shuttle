package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath_Directory(t *testing.T) {
	dir := t.TempDir()
	resolved, isDir, err := expandPath(dir)
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

func TestExpandPath_File(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	os.WriteFile(f, []byte("hello"), 0o644)

	resolved, isDir, err := expandPath(f)
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

func TestExpandPath_Tilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	resolved, _, err := expandPath("~/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != home+"/" {
		t.Errorf("resolved = %q, want %q", resolved, home+"/")
	}
}

func TestExpandPath_NotFound(t *testing.T) {
	_, _, err := expandPath("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
}

func TestResolveCloudSource_RcloneRemote(t *testing.T) {
	resolved, isRemote, isDir, err := resolveCloudSource("my_gdrive:Documents", "/tmp/drive")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isRemote {
		t.Error("isRemote = false, want true")
	}
	if !isDir {
		t.Error("isDir = false, want true for remotes")
	}
	if resolved != "my_gdrive:Documents" {
		t.Errorf("resolved = %q, want my_gdrive:Documents", resolved)
	}
}

func TestResolveCloudSource_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	resolved, isRemote, isDir, err := resolveCloudSource(dir, "/tmp/drive")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isRemote {
		t.Error("isRemote = true, want false")
	}
	if !isDir {
		t.Error("isDir = false, want true")
	}
	if resolved != dir {
		t.Errorf("resolved = %q, want %q", resolved, dir)
	}
}

func TestResolveCloudSource_RelativePath(t *testing.T) {
	drive := t.TempDir()
	subdir := filepath.Join(drive, "media")
	os.Mkdir(subdir, 0o755)

	resolved, isRemote, isDir, err := resolveCloudSource("media", drive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isRemote {
		t.Error("isRemote = true, want false")
	}
	if !isDir {
		t.Error("isDir = false, want true")
	}
	if resolved != subdir {
		t.Errorf("resolved = %q, want %q", resolved, subdir)
	}
}

func TestResolveCloudSource_RelativePath_NotFound(t *testing.T) {
	drive := t.TempDir()
	_, _, _, err := resolveCloudSource("nonexistent", drive)
	if err == nil {
		t.Fatal("expected error for non-existent relative path, got nil")
	}
}
