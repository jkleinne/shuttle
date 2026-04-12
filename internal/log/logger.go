// Package log provides a dual-stream logger that writes colored output to
// the terminal and timestamped plain text to a log file.
package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

// Logger writes to both a terminal stream (with optional color) and a plain
// text log file (with timestamps). Callers must call Close when done.
type Logger struct {
	terminal io.Writer
	file     *os.File
	useColor bool
}

// New creates a Logger that writes colored output to os.Stdout and plain text
// to a timestamped log file under logDir. Returns the logger and the log file path.
// The log directory is created if it does not exist.
func New(logDir string, useColor bool) (*Logger, string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("creating log directory %s: %w", logDir, err)
	}
	timestamp := time.Now().Format("2006-01-02_150405")
	logPath := filepath.Join(logDir, timestamp+".log")

	f, err := os.Create(logPath)
	if err != nil {
		return nil, "", fmt.Errorf("creating log file %s: %w", logPath, err)
	}

	return &Logger{terminal: os.Stdout, file: f, useColor: useColor}, logPath, nil
}

// NewWithWriter creates a Logger with a custom terminal writer and a log file at
// logPath. Intended for use in tests where terminal output needs to be captured.
func NewWithWriter(terminal io.Writer, logPath string, useColor bool) (*Logger, error) {
	f, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("creating log file %s: %w", logPath, err)
	}
	return &Logger{terminal: terminal, file: f, useColor: useColor}, nil
}

// Close closes the underlying log file. Should be called via defer after New or NewWithWriter.
func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

// LogPath returns the path to the log file.
func (l *Logger) LogPath() string {
	if l.file == nil {
		return ""
	}
	return l.file.Name()
}

// Header logs a section separator with the given label.
// Terminal: bold blue "==> label". File: "==> label".
func (l *Logger) Header(msg string) {
	l.termf("\n%s%s==> %s%s\n", colorBold, colorBlue, msg, colorReset)
	l.filef("==> %s", msg)
}

// Info logs an informational message.
// Terminal: blue "[INFO] msg". File: "[INFO] msg".
func (l *Logger) Info(msg string) {
	l.termf("%s[INFO]%s %s\n", colorBlue, colorReset, msg)
	l.filef("[INFO] %s", msg)
}

// Success logs a success message.
// Terminal: green "[OK] msg". File: "[OK] msg".
func (l *Logger) Success(msg string) {
	l.termf("%s[OK]%s %s\n", colorGreen, colorReset, msg)
	l.filef("[OK] %s", msg)
}

// Warn logs a warning message.
// Terminal: yellow "[WARN] msg". File: "[WARN] msg".
func (l *Logger) Warn(msg string) {
	l.termf("%s[WARN]%s %s\n", colorYellow, colorReset, msg)
	l.filef("[WARN] %s", msg)
}

// Error logs an error message.
// Terminal: red "[ERROR] msg". File: "[ERROR] msg".
func (l *Logger) Error(msg string) {
	l.termf("%s[ERROR]%s %s\n", colorRed, colorReset, msg)
	l.filef("[ERROR] %s", msg)
}

// termf formats and writes msg to the terminal stream. When useColor is false,
// ANSI escape sequences are stripped before writing.
func (l *Logger) termf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !l.useColor {
		msg = stripAnsi(msg)
	}
	fmt.Fprint(l.terminal, msg)
}

// filef formats msg with a timestamp prefix and writes it to the log file.
// File format: [YYYY-MM-DD HH:MM:SS] <formatted message>
func (l *Logger) filef(format string, args ...any) {
	if l.file == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(l.file, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// stripAnsi removes ANSI escape sequences (e.g. "\033[31m") from s.
// Only handles SGR sequences (ESC [ ... m), which covers all color codes used here.
func stripAnsi(s string) string {
	var out []byte
	i := 0
	for i < len(s) {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			// Skip ESC [ ... m
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
