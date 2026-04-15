// Package log provides a dual-stream logger that writes colored output to
// the terminal and timestamped plain text to a log file.
package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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

// Verbosity controls how much terminal output Logger emits. File output
// is always written at full detail (including Debug) regardless of level.
type Verbosity int

// Verbosity levels. VerbosityNormal is the default; callers change it via
// SetVerbosity after constructing a Logger.
const (
	VerbosityQuiet   Verbosity = -1
	VerbosityNormal  Verbosity = 0
	VerbosityVerbose Verbosity = 1
)

// Logger writes to both a terminal stream (with optional color) and a plain
// text log file (with timestamps). Informational messages (Header, Info,
// Success, Debug) go to terminal; diagnostics (Warn, Error) go to stderr.
// By default stderr == terminal (backwards-compatible for tests capturing a
// single writer); New wires the real os.Stderr. Callers must call Close
// when done.
type Logger struct {
	terminal  io.Writer
	stderr    io.Writer
	file      *os.File
	useColor  bool
	verbosity Verbosity
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

	return &Logger{terminal: os.Stdout, stderr: os.Stderr, file: f, useColor: useColor}, logPath, nil
}

// NewWithWriter creates a Logger with a custom terminal writer and a log file at
// logPath. Intended for use in tests where terminal output needs to be captured.
// The same writer receives both informational and diagnostic messages; callers
// that need to assert stream separation should use SetStderr.
func NewWithWriter(terminal io.Writer, logPath string, useColor bool) (*Logger, error) {
	f, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("creating log file %s: %w", logPath, err)
	}
	return &Logger{terminal: terminal, stderr: terminal, file: f, useColor: useColor}, nil
}

// SetStderr overrides the writer used for Warn and Error output. Tests that
// need to distinguish stdout from stderr output call this to point them at
// a separate buffer.
func (l *Logger) SetStderr(w io.Writer) {
	l.stderr = w
}

// Close closes the underlying log file. Should be called via defer after New or NewWithWriter.
func (l *Logger) Close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}

// LogPath returns the path to the log file.
func (l *Logger) LogPath() string {
	if l.file == nil {
		return ""
	}
	return l.file.Name()
}

// SetVerbosity changes the terminal verbosity level. File output is unaffected:
// every message type always lands in the log file at full detail.
func (l *Logger) SetVerbosity(v Verbosity) {
	l.verbosity = v
}

// Verbosity returns the current terminal verbosity level.
func (l *Logger) Verbosity() Verbosity {
	return l.verbosity
}

// Header logs a section separator with the given label.
// Terminal: bold blue "==> label" (hidden in quiet mode). File: "==> label".
func (l *Logger) Header(msg string) {
	if l.verbosity >= VerbosityNormal {
		l.termf("\n%s%s==> %s%s\n", colorBold, colorBlue, msg, colorReset)
	}
	l.filef("==> %s", msg)
}

// Info logs an informational message (hidden in quiet mode).
// Terminal: blue "[INFO] msg". File: "[INFO] msg".
func (l *Logger) Info(msg string) {
	if l.verbosity >= VerbosityNormal {
		l.termf("%s[INFO]%s %s\n", colorBlue, colorReset, msg)
	}
	l.filef("[INFO] %s", msg)
}

// Success logs a success message (hidden in quiet mode).
// Terminal: green "[OK] msg". File: "[OK] msg".
func (l *Logger) Success(msg string) {
	if l.verbosity >= VerbosityNormal {
		l.termf("%s[OK]%s %s\n", colorGreen, colorReset, msg)
	}
	l.filef("[OK] %s", msg)
}

// Warn logs a warning message to stderr. Hidden in quiet mode so cron jobs
// using --quiet stay silent on first-run noise and benign issues; the log
// file always records it.
func (l *Logger) Warn(msg string) {
	if l.verbosity >= VerbosityNormal {
		l.errf("%s[WARN]%s %s\n", colorYellow, colorReset, msg)
	}
	l.filef("[WARN] %s", msg)
}

// Error logs an error message to stderr. Always shown regardless of verbosity:
// in quiet-on-failure cron workflows, errors are the signal that triggers the
// notification.
func (l *Logger) Error(msg string) {
	l.errf("%s[ERROR]%s %s\n", colorRed, colorReset, msg)
	l.filef("[ERROR] %s", msg)
}

// Debug logs a diagnostic message. Terminal output appears only when verbosity
// is VerbosityVerbose; the log file always receives the message.
// File format: "[DEBUG] msg".
func (l *Logger) Debug(msg string) {
	if l.verbosity >= VerbosityVerbose {
		l.termf("%s[DEBUG]%s %s\n", colorBold, colorReset, msg)
	}
	l.filef("[DEBUG] %s", msg)
}

// FileHeader writes a section header to the log file only.
// Used by the runner in interactive mode where terminal output
// is managed by the ProgressWriter.
func (l *Logger) FileHeader(msg string) {
	l.filef("==> %s", msg)
}

// FileInfo writes an informational message to the log file only.
func (l *Logger) FileInfo(msg string) {
	l.filef("[INFO] %s", msg)
}

// FileError writes an error message to the log file only.
func (l *Logger) FileError(msg string) {
	l.filef("[ERROR] %s", msg)
}

// termf formats and writes msg to the terminal stream. When useColor is false,
// ANSI escape sequences are stripped before writing.
func (l *Logger) termf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !l.useColor {
		msg = stripAnsi(msg)
	}
	_, _ = fmt.Fprint(l.terminal, msg)
}

// errf formats and writes msg to the stderr stream with the same color
// treatment as termf.
func (l *Logger) errf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !l.useColor {
		msg = stripAnsi(msg)
	}
	_, _ = fmt.Fprint(l.stderr, msg)
}

// filef formats msg with a timestamp prefix and writes it to the log file.
// File format: [YYYY-MM-DD HH:MM:SS] <formatted message>
func (l *Logger) filef(format string, args ...any) {
	if l.file == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(l.file, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// logFilePattern matches the timestamped log filenames produced by New.
// The creation time is encoded in the filename (big-endian date format),
// which makes lexicographic sort equivalent to chronological sort.
var logFilePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}_\d{6})\.log$`)

// logFilenameLayout mirrors the format used by New when creating a log file.
const logFilenameLayout = "2006-01-02_150405"

// PruneOldLogs deletes log files under logDir whose embedded timestamp is
// older than maxAgeDays relative to now. Only files matching the shuttle
// log-filename pattern (YYYY-MM-DD_HHMMSS.log) are considered; any other
// files in the directory are ignored.
//
// Pruning is best-effort per file: an individual deletion failure is
// recorded as a warning and the function continues with the rest.
// A non-nil err is returned only when the directory itself cannot be read.
//
// A maxAgeDays value of zero or negative disables pruning and returns
// (0, nil, nil) without touching the filesystem.
func PruneOldLogs(logDir string, maxAgeDays int, now time.Time) (deleted int, warnings []string, err error) {
	if maxAgeDays <= 0 {
		return 0, nil, nil
	}

	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			// First-ever run: the log directory hasn't been created yet.
			// Nothing to prune, and not a condition worth warning about.
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("reading log dir %s: %w", logDir, err)
	}

	cutoff := now.Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := logFilePattern.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		ts, parseErr := time.Parse(logFilenameLayout, m[1])
		if parseErr != nil {
			// The regex guarantees a parseable shape, but be defensive.
			continue
		}
		if !ts.Before(cutoff) {
			continue
		}
		path := filepath.Join(logDir, entry.Name())
		if rmErr := os.Remove(path); rmErr != nil {
			warnings = append(warnings, fmt.Sprintf("deleting %s: %v", path, rmErr))
			continue
		}
		deleted++
	}
	return deleted, warnings, nil
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
