package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func xdgRuntimeDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("chmod runtime dir: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", directory)
	return directory
}

func changeWorkingDirectory(t *testing.T, directory string) {
	t.Helper()
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatalf("changing working directory to %s: %v", directory, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDirectory); err != nil {
			t.Errorf("restoring working directory to %s: %v", previousDirectory, err)
		}
	})
}

func TestLockFilePath_DifferentConfigs(t *testing.T) {
	runtimeDirectory := xdgRuntimeDir(t)

	firstPath, err := lockFilePath("/home/user/.config/shuttle/config.toml")
	if err != nil {
		t.Fatalf("lockFilePath first config: %v", err)
	}
	secondPath, err := lockFilePath("/home/user/alt/shuttle/config.toml")
	if err != nil {
		t.Fatalf("lockFilePath second config: %v", err)
	}
	repeatedPath, err := lockFilePath("/home/user/.config/shuttle/config.toml")
	if err != nil {
		t.Fatalf("lockFilePath repeated config: %v", err)
	}

	if firstPath == secondPath {
		t.Error("different config paths should produce different lock paths")
	}
	if firstPath != repeatedPath {
		t.Error("same config path should produce same lock path")
	}
	wantPrefix := filepath.Join(runtimeDirectory, "shuttle-")
	if !strings.HasPrefix(firstPath, wantPrefix) {
		t.Errorf("lock path should start with %q, got %q", wantPrefix, firstPath)
	}
}

func TestLockDirectory_HonorsXDGRuntimeDir(t *testing.T) {
	runtimeDirectory := xdgRuntimeDir(t)
	got, err := lockDirectory()
	if err != nil {
		t.Fatalf("lockDirectory: %v", err)
	}
	if got != runtimeDirectory {
		t.Errorf("lockDirectory() = %q, want %q", got, runtimeDirectory)
	}
}

func TestLockDirectory_RejectsRelativeXDGRuntimeDir(t *testing.T) {
	workingDirectory := t.TempDir()
	relativeRuntimeDirectory := "runtime"
	if err := os.Mkdir(
		filepath.Join(workingDirectory, relativeRuntimeDirectory),
		lockDirectoryPermissions,
	); err != nil {
		t.Fatalf("creating relative runtime directory: %v", err)
	}
	changeWorkingDirectory(t, workingDirectory)
	t.Setenv("XDG_RUNTIME_DIR", relativeRuntimeDirectory)

	got, err := lockDirectory()

	if err == nil {
		t.Fatalf("lockDirectory() = (%q, nil), want relative XDG_RUNTIME_DIR error", got)
	}
	if !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") ||
		!strings.Contains(err.Error(), "not absolute") {
		t.Errorf("error = %q, want relative XDG_RUNTIME_DIR context", err)
	}
}

func TestLockDirectory_RejectsRelativeTempDirectory(t *testing.T) {
	workingDirectory := t.TempDir()
	relativeTempDirectory := "relative-temp"
	if err := os.Mkdir(
		filepath.Join(workingDirectory, relativeTempDirectory),
		lockDirectoryPermissions,
	); err != nil {
		t.Fatalf("creating relative temporary directory: %v", err)
	}
	changeWorkingDirectory(t, workingDirectory)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", relativeTempDirectory)

	got, err := lockDirectory()

	if err == nil {
		t.Fatalf("lockDirectory() = (%q, nil), want relative temporary directory error", got)
	}
	if !strings.Contains(err.Error(), "temporary directory") ||
		!strings.Contains(err.Error(), "not absolute") {
		t.Errorf("error = %q, want relative temporary directory context", err)
	}
}

func TestLockDirectory_RejectsInsecureXDGRuntimeDir(t *testing.T) {
	insecure := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(insecure, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(insecure, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", insecure)
	if _, err := lockDirectory(); err == nil {
		t.Error("lockDirectory should reject a group/other-accessible XDG_RUNTIME_DIR")
	}
}

func TestLockDirectory_FallbackCreatesPrivateSubdir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", t.TempDir())
	got, err := lockDirectory()
	if err != nil {
		t.Fatalf("lockDirectory: %v", err)
	}
	info, err := os.Lstat(got)
	if err != nil {
		t.Fatalf("stat lock dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("lock dir is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("lock dir perm = %o, want 700", info.Mode().Perm())
	}
}

func TestEnsureSecureLockDirectory_RejectsInsecurePaths(t *testing.T) {
	t.Run("fresh dir created 0700", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "lock")
		if err := ensureSecureLockDirectory(directory); err != nil {
			t.Fatalf("ensureSecureLockDirectory: %v", err)
		}
		info, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("stat created directory: %v", err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("perm = %o, want 700", info.Mode().Perm())
		}
	})
	t.Run("group or other access rejected", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "loose")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(directory, 0o777); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if err := ensureSecureLockDirectory(directory); err == nil {
			t.Error("expected error for 0777 dir")
		}
	})
	t.Run("symlink rejected", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := ensureSecureLockDirectory(link); err == nil {
			t.Error("expected error for symlinked dir")
		}
	})
	t.Run("non-directory rejected", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := ensureSecureLockDirectory(file); err == nil {
			t.Error("expected error for non-directory")
		}
	})
}

func TestFileRunLocker_Acquire_RejectsSymlinkedLockPath(t *testing.T) {
	xdgRuntimeDir(t)
	configPath := "/some/config.toml"
	lockPath, err := lockFilePath(configPath)
	if err != nil {
		t.Fatalf("lockFilePath: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), lockPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	locker := NewFileRunLocker()
	err = locker.Acquire(configPath)

	if err == nil {
		t.Error("Acquire should reject a symlinked lock path (O_NOFOLLOW)")
	}
}

func TestFileRunLocker_Acquire_CreatesPrivateLockFile(t *testing.T) {
	xdgRuntimeDir(t)
	configPath := "/some/config.toml"
	locker := NewFileRunLocker()
	t.Cleanup(func() {
		if locker.lockFile != nil {
			_ = locker.lockFile.Close()
		}
	})

	if err := locker.Acquire(configPath); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	lockPath, err := lockFilePath(configPath)
	if err != nil {
		t.Fatalf("lockFilePath: %v", err)
	}
	info, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("lock file perm = %o, want 600", info.Mode().Perm())
	}
}

func TestClassifyRunLockError(t *testing.T) {
	systemFailure := errors.New("system failure")
	tests := []struct {
		name               string
		cause              error
		wantActiveInstance bool
	}{
		{name: "contention", cause: syscall.EWOULDBLOCK, wantActiveInstance: true},
		{name: "system failure", cause: systemFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyRunLockError("/tmp/shuttle.lock", tt.cause)

			if !errors.Is(err, tt.cause) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.cause)
			}
			hasActiveInstance := strings.Contains(err.Error(), "another instance is already running")
			if hasActiveInstance != tt.wantActiveInstance {
				t.Errorf("active-instance message = %v, want %v: %v",
					hasActiveInstance, tt.wantActiveInstance, err)
			}
		})
	}
}
