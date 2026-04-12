package log_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkleinne/sync-station/internal/log"
)

func TestLogger_WritesToBothStreams(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, false)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Info("hello world")

	termOut := termBuf.String()
	if !strings.Contains(termOut, "hello world") {
		t.Errorf("terminal missing message, got: %q", termOut)
	}

	fileBytes, _ := os.ReadFile(logFile)
	fileOut := string(fileBytes)
	if !strings.Contains(fileOut, "[INFO] hello world") {
		t.Errorf("log file missing message, got: %q", fileOut)
	}
}

func TestLogger_FileOutput_NoAnsiCodes(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, true)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Error("something broke")

	fileBytes, _ := os.ReadFile(logFile)
	fileOut := string(fileBytes)
	if strings.Contains(fileOut, "\033[") {
		t.Errorf("log file contains ANSI codes: %q", fileOut)
	}
	if !strings.Contains(fileOut, "[ERROR] something broke") {
		t.Errorf("log file missing message, got: %q", fileOut)
	}
}

func TestLogger_TerminalColor_WhenEnabled(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, true)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Error("fail")

	termOut := termBuf.String()
	if !strings.Contains(termOut, "\033[") {
		t.Errorf("terminal missing ANSI codes when color enabled: %q", termOut)
	}
}

func TestLogger_AllMethods(t *testing.T) {
	var termBuf bytes.Buffer
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test.log")

	logger, err := log.NewWithWriter(&termBuf, logFile, false)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
	}
	defer logger.Close()

	logger.Header("section")
	logger.Info("info msg")
	logger.Success("ok msg")
	logger.Warn("warn msg")
	logger.Error("err msg")

	fileBytes, _ := os.ReadFile(logFile)
	fileOut := string(fileBytes)

	for _, want := range []string{"==> section", "[INFO] info msg", "[OK] ok msg", "[WARN] warn msg", "[ERROR] err msg"} {
		if !strings.Contains(fileOut, want) {
			t.Errorf("log file missing %q", want)
		}
	}
}
