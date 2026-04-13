package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jkleinne/shuttle/internal/log"
)

func newTestLogger(t *testing.T) *log.Logger {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "test.log")
	logger, err := log.NewWithWriter(os.Stdout, logPath, false)
	if err != nil {
		t.Fatalf("creating test logger: %v", err)
	}
	t.Cleanup(func() { logger.Close() })
	return logger
}

func TestRsyncExec_TransfersFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644)

	executor := NewRsyncExecutor(newTestLogger(t), false, "")
	result := executor.Exec(context.Background(), src+"/", dst+"/", RsyncOpts{})

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Stats.FilesTransferred != 1 {
		t.Errorf("FilesTransferred = %d, want 1", result.Stats.FilesTransferred)
	}

	content, _ := os.ReadFile(filepath.Join(dst, "hello.txt"))
	if string(content) != "world" {
		t.Errorf("file content = %q, want world", string(content))
	}
}

func TestRsyncExec_DryRun_DoesNotTransfer(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644)

	executor := NewRsyncExecutor(newTestLogger(t), true, "")
	result := executor.Exec(context.Background(), src+"/", dst+"/", RsyncOpts{})

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}

	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Errorf("dst has %d entries, want 0 (dry run)", len(entries))
	}
}

func TestRsyncExec_DeleteAfter_ForDirectories(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	// Create a file in dst that doesn't exist in src
	os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("remove me"), 0o644)
	os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644)

	executor := NewRsyncExecutor(newTestLogger(t), false, "")
	result := executor.Exec(context.Background(), src+"/", dst+"/", RsyncOpts{Delete: true})

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}

	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Error("stale.txt should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.txt")); err != nil {
		t.Error("keep.txt should exist")
	}
}

func TestRsyncExec_ExtraOpts_Applied(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	os.WriteFile(filepath.Join(src, "include.txt"), []byte("yes"), 0o644)
	os.WriteFile(filepath.Join(src, ".hidden"), []byte("no"), 0o644)

	executor := NewRsyncExecutor(newTestLogger(t), false, "")
	result := executor.Exec(context.Background(), src+"/", dst+"/", RsyncOpts{
		ExtraOpts: []string{"--exclude=.*"},
	})

	if result.Status != StatusOK {
		t.Fatalf("Status = %q, want ok", result.Status)
	}

	if _, err := os.Stat(filepath.Join(dst, ".hidden")); !os.IsNotExist(err) {
		t.Error(".hidden should have been excluded")
	}
	if _, err := os.Stat(filepath.Join(dst, "include.txt")); err != nil {
		t.Error("include.txt should exist")
	}
}
