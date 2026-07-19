package log

import (
	"os"
	"strings"
	"testing"
)

func TestValidateLogDirectoryOwner_RejectsForeignOwner(t *testing.T) {
	directory := t.TempDir()
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatalf("lstat log directory: %v", err)
	}

	err = validateLogDirectoryOwner(directory, info, os.Getuid()+1)
	if err == nil {
		t.Fatal("validateLogDirectoryOwner returned nil error for a foreign owner")
	}
	if !strings.Contains(err.Error(), "log directory") || !strings.Contains(err.Error(), "not owned") {
		t.Errorf("foreign owner error lacks ownership context: %v", err)
	}
}
