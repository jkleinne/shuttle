package engine

import (
	"strings"
	"testing"
)

func TestSelectMode(t *testing.T) {
	logger := newTestLogger(t)

	tests := []struct {
		name             string
		mode             string
		destination      string
		remoteName       string
		backupPath       string
		runTimestamp     string
		isDir            bool
		wantSubcommand   string
		wantBackupDirArg string
	}{
		{
			name:             "copy mode, directory source",
			mode:             "copy",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            true,
			wantSubcommand:   "copy",
			wantBackupDirArg: "",
		},
		{
			name:             "copy mode, file source",
			mode:             "copy",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            false,
			wantSubcommand:   "copy",
			wantBackupDirArg: "",
		},
		{
			name:             "sync mode, file source falls back to copy",
			mode:             "sync",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            false,
			wantSubcommand:   "copy",
			wantBackupDirArg: "",
		},
		{
			name:             "sync mode, directory source",
			mode:             "sync",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "",
			runTimestamp:     "2025-01-15_120000",
			isDir:            true,
			wantSubcommand:   "sync",
			wantBackupDirArg: "",
		},
		{
			name:             "sync mode, directory source, backup path",
			mode:             "sync",
			destination:      "myremote:photos/",
			remoteName:       "myremote",
			backupPath:       "backups",
			runTimestamp:     "2025-01-15_120000",
			isDir:            true,
			wantSubcommand:   "sync",
			wantBackupDirArg: "myremote:backups/2025-01-15_120000/photos/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subcommand, backupDirArg := selectMode(
				tt.mode, tt.destination, tt.remoteName,
				tt.backupPath, tt.runTimestamp, tt.isDir, logger,
			)
			if subcommand != tt.wantSubcommand {
				t.Errorf("subcommand = %q, want %q", subcommand, tt.wantSubcommand)
			}
			if backupDirArg != tt.wantBackupDirArg {
				t.Errorf("backupDirArg = %q, want %q", backupDirArg, tt.wantBackupDirArg)
			}
		})
	}
}

func TestScanRcloneProgress_ActiveTransfer_ShowsBytesLine(t *testing.T) {
	input := "Transferred:   1.082 GiB / 2.164 GiB, 50%, 32.709 KiB/s, ETA 30s\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) == 0 {
		t.Fatal("onProgress was never called")
	}
	if !strings.Contains(called[0], "GiB") {
		t.Errorf("progress = %q, want bytes-transferred line", called[0])
	}
}

func TestScanRcloneProgress_ZeroBytesTransferred_ReturnsEmpty(t *testing.T) {
	input := "Transferred:   0 B / 0 B, -, 0 B/s, ETA -\nChecks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) != 0 {
		t.Errorf("onProgress should not be called during check-only phase, got %d calls: %v", len(called), called)
	}
}

func TestScanRcloneProgress_ChecksOnly_ReturnsEmpty(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) != 0 {
		t.Errorf("onProgress should not be called for checks-only output, got %d calls", len(called))
	}
}

func TestScanRcloneProgress_CountOnlyTransferred_Ignored(t *testing.T) {
	// The count-only Transferred line (no /s) should not trigger progress
	input := "Transferred:   12 / 50, 24%\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) != 0 {
		t.Errorf("onProgress should not be called for count-only transferred line, got %d calls", len(called))
	}
}

func TestScanRcloneProgress_NilCallback(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	// Should not panic
	scanRcloneProgress(r, nil)
}

func TestScanRcloneProgress_ConcatenatedPerFileProgress(t *testing.T) {
	// When piped, rclone's per-file progress line has no trailing delimiter.
	// The next "Transferred:" bytes line gets concatenated onto it, forming a
	// segment like: "* file.bin: 40% /1Mi, 100Ki/s, 5sTransferred: 512 KiB ..."
	input := " *                                      test.bin: 40% /1Mi, 219.991Ki/s, 2sTransferred:   \t      604 KiB / 1 MiB, 59%, 206 KiB/s, ETA 2s\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) == 0 {
		t.Fatal("onProgress was never called for concatenated per-file + Transferred line")
	}
	if !strings.Contains(called[0], "604 KiB / 1 MiB") {
		t.Errorf("progress = %q, want '604 KiB / 1 MiB' bytes line", called[0])
	}
}

func TestScanRcloneProgress_CheckThenTransfer(t *testing.T) {
	// Simulates a real rclone run: check-only phase (0 B / 0 B) produces no
	// progress, then an active transfer starts and progress appears.
	input := "Transferred:   0 B / 0 B, -, 0 B/s, ETA -\n" +
		"Checks:        500 / 1000, 50%\n" +
		"Transferred:   0 / 0\n" +
		"Elapsed time:  1.0s\n" +
		"Transferred:   512 KiB / 2 MiB, 25%, 512 KiB/s, ETA 3s\n" +
		"Checks:       1000 / 1000, 100%\n" +
		"Transferred:   1 / 5, 20%\n" +
		"Elapsed time:  2.0s\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) == 0 {
		t.Fatal("onProgress was never called after transfer started")
	}
	if !strings.Contains(called[0], "512 KiB / 2 MiB") {
		t.Errorf("first progress = %q, want bytes line from transfer phase", called[0])
	}
}

func TestScanRcloneProgress_CRDelimitedLines(t *testing.T) {
	// Rclone uses \r for in-place updates during transfers
	input := "Transferred:   1 GiB / 3 GiB, 33%, 5 MiB/s, ETA 10m\rTransferred:   2 GiB / 3 GiB, 66%, 5 MiB/s, ETA 5m\r"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(called))
	}
	if !strings.Contains(called[len(called)-1], "2 GiB / 3 GiB") {
		t.Errorf("last progress = %q, want latest transfer line", called[len(called)-1])
	}
}
