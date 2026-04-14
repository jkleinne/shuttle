package engine

import (
	"strings"
	"testing"
)

func TestScanRcloneProgress_ChecksLine(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) == 0 {
		t.Fatal("onProgress was never called")
	}
	if !strings.Contains(called[0], "500 / 1000") {
		t.Errorf("progress = %q, want something containing '500 / 1000'", called[0])
	}
}

func TestScanRcloneProgress_TransferredBytesLine_TakesPrecedence(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\nTransferred:   1.082 GiB / 2.164 GiB, 50%, 32.709 KiB/s, ETA 30s\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) < 2 {
		t.Fatalf("expected at least 2 calls, got %d", len(called))
	}
	last := called[len(called)-1]
	if !strings.Contains(last, "GiB") {
		t.Errorf("last progress = %q, want bytes-transferred line", last)
	}
}

func TestScanRcloneProgress_TransferredCountLine_Ignored(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\nTransferred:   12 / 50, 24%\n"
	r := strings.NewReader(input)

	var called []string
	scanRcloneProgress(r, func(text string) {
		called = append(called, text)
	})

	if len(called) == 0 {
		t.Fatal("onProgress was never called")
	}
	last := called[len(called)-1]
	if !strings.Contains(last, "500 / 1000") {
		t.Errorf("last progress = %q, want checks line (count-only transferred should not override)", last)
	}
}

func TestScanRcloneProgress_NilCallback(t *testing.T) {
	input := "Checks:        500 / 1000, 50%\n"
	r := strings.NewReader(input)

	// Should not panic
	scanRcloneProgress(r, nil)
}
