// Package log provides a dual-stream logger that writes colored output to
// the terminal and timestamped plain text to a log file.
package log

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

const (
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

// ToolSource is the closed set of external tools allowed to define log frames.
type ToolSource uint8

const (
	// ToolRsync identifies records captured from rsync.
	ToolRsync ToolSource = iota + 1
	// ToolRclone identifies records captured from rclone.
	ToolRclone
)

// hoursPerDay is used when converting a retention window expressed in days
// into a time.Duration. Extracted as a named constant per project rules
// against unlabelled numeric literals.
const hoursPerDay = 24

const (
	logDirectoryPermissions os.FileMode = 0o700
	logFilePermissions      os.FileMode = 0o600
)

// Verbosity controls how much terminal output Logger emits. File output
// is always written at full detail (including Debug) regardless of level.
type Verbosity int

// Verbosity levels. Callers pass the desired level to New/NewWithWriter at
// construction time; there is no mutator so the logger's level can't drift
// after the first write.
const (
	VerbosityQuiet   Verbosity = -1
	VerbosityNormal  Verbosity = 0
	VerbosityVerbose Verbosity = 1
)

// Options controls terminal rendering and message verbosity.
type Options struct {
	UseColor  bool
	Verbosity Verbosity
}

// Logger writes to three streams: an informational terminal stream
// (typically os.Stdout), a diagnostic stream (typically os.Stderr), and a
// plain-text log file with timestamps. Informational messages (Header,
// Info, Success, Debug) go to terminal; diagnostics (Warn, Error) go to
// stderr. Callers must call Close when done.
type Logger struct {
	terminal  io.Writer
	stderr    io.Writer
	file      *os.File
	fileMu    sync.Mutex
	useColor  bool
	verbosity Verbosity
}

// New creates a Logger that writes colored output to os.Stdout, diagnostic
// output to os.Stderr, and plain text to a timestamped log file under logDir.
// Returns the logger and the log file path. The log directory is created if
// it does not exist.
func New(logDir string, options Options) (*Logger, string, error) {
	if err := prepareLogDirectory(logDir); err != nil {
		return nil, "", err
	}
	timestamp := time.Now().Format(logFilenameLayout)

	f, err := os.CreateTemp(logDir, timestamp+"-*.log")
	if err != nil {
		return nil, "", fmt.Errorf("creating unique log file in %s: %w", logDir, err)
	}
	logPath := f.Name()

	return &Logger{
		terminal:  os.Stdout,
		stderr:    os.Stderr,
		file:      f,
		useColor:  options.UseColor,
		verbosity: options.Verbosity,
	}, logPath, nil
}

// NewWithWriter creates a Logger with a custom terminal writer and a log
// file at logPath. Intended for tests where terminal output needs to be
// captured. The same writer receives both informational and diagnostic
// messages; tests that need stream separation call SetStderr to redirect.
func NewWithWriter(terminal io.Writer, logPath string, options Options) (*Logger, error) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, logFilePermissions)
	if err != nil {
		return nil, fmt.Errorf("creating log file %s: %w", logPath, err)
	}
	return &Logger{
		terminal:  terminal,
		stderr:    terminal,
		file:      f,
		useColor:  options.UseColor,
		verbosity: options.Verbosity,
	}, nil
}

func prepareLogDirectory(directory string) error {
	exists, err := prepareExistingLogDirectory(directory)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if err := os.MkdirAll(directory, logDirectoryPermissions); err != nil {
		return fmt.Errorf(
			"creating log directory %s with permissions %o: %w",
			directory,
			logDirectoryPermissions,
			err,
		)
	}
	exists, err = prepareExistingLogDirectory(directory)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("checking log directory %s after creation: directory is missing", directory)
	}
	return nil
}

func prepareExistingLogDirectory(directory string) (bool, error) {
	info, err := inspectLogDirectory(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode().Perm() == logDirectoryPermissions {
		return true, nil
	}
	if err := os.Chmod(directory, logDirectoryPermissions); err != nil {
		return false, fmt.Errorf(
			"tightening log directory permissions %s to %o: %w",
			directory,
			logDirectoryPermissions,
			err,
		)
	}
	return true, nil
}

func inspectLogDirectory(directory string) (os.FileInfo, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("checking log directory identity %s: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("log directory %s is a symlink", directory)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("log directory %s is not a directory", directory)
	}
	if err := validateLogDirectoryOwner(directory, info, os.Getuid()); err != nil {
		return nil, err
	}
	return info, nil
}

func validateLogDirectoryOwner(directory string, info os.FileInfo, currentUID int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("log directory %s has unsupported ownership metadata", directory)
	}
	if int(stat.Uid) != currentUID {
		return fmt.Errorf("log directory %s is not owned by current user", directory)
	}
	return nil
}

// SetStderr overrides the writer used for Warn and Error output. Tests that
// need to distinguish stdout from stderr output call this to point them at
// a separate buffer.
func (l *Logger) SetStderr(w io.Writer) {
	l.stderr = w
}

// Close closes the underlying log file. Should be called via defer after New or NewWithWriter.
func (l *Logger) Close() {
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
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

// Header logs a section separator with the given label.
// Terminal: bold blue "==> label" (hidden in quiet mode). File: "==> label".
func (l *Logger) Header(msg string) {
	msg = sanitizeLogText(msg)
	if l.verbosity >= VerbosityNormal {
		l.termf("\n%s%s==> %s%s\n", colorBold, colorBlue, msg, colorReset)
	}
	l.filef("==> %s", msg)
}

// Info logs an informational message (hidden in quiet mode).
// Terminal: blue "[INFO] msg". File: "[INFO] msg".
func (l *Logger) Info(msg string) {
	msg = sanitizeLogText(msg)
	if l.verbosity >= VerbosityNormal {
		l.termf("%s[INFO]%s %s\n", colorBlue, colorReset, msg)
	}
	l.filef("[INFO] %s", msg)
}

// Success logs a success message (hidden in quiet mode).
// Terminal: green "[OK] msg". File: "[OK] msg".
func (l *Logger) Success(msg string) {
	msg = sanitizeLogText(msg)
	if l.verbosity >= VerbosityNormal {
		l.termf("%s[OK]%s %s\n", colorGreen, colorReset, msg)
	}
	l.filef("[OK] %s", msg)
}

// Warn logs a warning message to stderr. Hidden in quiet mode so cron jobs
// using --quiet stay silent on first-run noise and benign issues; the log
// file always records it.
func (l *Logger) Warn(msg string) {
	msg = sanitizeLogText(msg)
	if l.verbosity >= VerbosityNormal {
		l.errf("%s[WARN]%s %s\n", colorYellow, colorReset, msg)
	}
	l.filef("[WARN] %s", msg)
}

// Error logs an error message to stderr. Always shown regardless of verbosity:
// in quiet-on-failure cron workflows, errors are the signal that triggers the
// notification.
func (l *Logger) Error(msg string) {
	msg = sanitizeLogText(msg)
	l.errf("%s[ERROR]%s %s\n", colorRed, colorReset, msg)
	l.filef("[ERROR] %s", msg)
}

// Debug logs a diagnostic message. Terminal output appears only when verbosity
// is VerbosityVerbose; the log file always receives the message.
// File format: "[DEBUG] msg".
func (l *Logger) Debug(msg string) {
	msg = sanitizeLogText(msg)
	if l.verbosity >= VerbosityVerbose {
		l.termf("%s[DEBUG]%s %s\n", colorBold, colorReset, msg)
	}
	l.filef("[DEBUG] %s", msg)
}

// FileHeader writes a section header to the log file only.
// Used by the runner in interactive mode where terminal output
// is managed by the ProgressWriter.
func (l *Logger) FileHeader(msg string) {
	msg = sanitizeLogText(msg)
	l.filef("==> %s", msg)
}

// FileInfo writes an informational message to the log file only.
func (l *Logger) FileInfo(msg string) {
	msg = sanitizeLogText(msg)
	l.filef("[INFO] %s", msg)
}

// FileWarn writes a warning message to the log file only. Used by the
// runner in interactive mode where a live spinner owns the stdout TTY;
// writing to stderr would still visually interleave with the spinner since
// both streams share a terminal.
func (l *Logger) FileWarn(msg string) {
	msg = sanitizeLogText(msg)
	l.filef("[WARN] %s", msg)
}

// FileError writes an error message to the log file only.
func (l *Logger) FileError(msg string) {
	msg = sanitizeLogText(msg)
	l.filef("[ERROR] %s", msg)
}

// FileTool writes one complete file-only record from a known external tool.
// Unknown identities retain their sanitized context under Logger's trusted
// error frame instead of defining a new tool prefix.
func (l *Logger) FileTool(source ToolSource, record string) {
	record = sanitizeLogText(record)
	switch source {
	case ToolRsync:
		l.filef("[RSYNC] %s", record)
	case ToolRclone:
		l.filef("[RCLONE] %s", record)
	default:
		l.filef(
			"[ERROR] unexpected tool source %d: %s",
			source,
			record,
		)
	}
}

func sanitizeLogText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, text)
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
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if l.file == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(l.file, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// logFilePattern matches legacy timestamp-only filenames and the unique
// alphanumeric suffix filenames produced by New. The creation time is encoded
// in the first capture group in big-endian date format.
var logFilePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}_\d{6})(?:-[A-Za-z0-9]+)?\.log$`)

// logFilenameLayout mirrors the format used by New when creating a log file.
const logFilenameLayout = "2006-01-02_150405"

// PruneOldLogs deletes log files under logDir whose embedded timestamp is
// older than maxAgeDays relative to asOf. Legacy timestamp-only filenames and
// unique filenames with an alphanumeric suffix are considered; any other files
// in the directory are ignored. Timestamps are parsed in asOf's zone; callers
// pass time.Now() so the parse zone matches the zone New used when stamping the
// filename, keeping retention consistent for non-UTC users.
//
// Pruning is best-effort per file: an individual deletion failure is
// recorded as a warning and the function continues with the rest.
// A non-nil err is returned when the directory cannot be securely prepared or
// read. A missing directory is treated as "nothing to prune".
//
// A maxAgeDays value of zero or negative disables pruning and returns
// (0, nil, nil) without touching the filesystem.
func PruneOldLogs(logDir string, maxAgeDays int, asOf time.Time) (deleted int, warnings []string, err error) {
	if maxAgeDays <= 0 {
		return 0, nil, nil
	}

	exists, err := prepareExistingLogDirectory(logDir)
	if err != nil {
		return 0, nil, err
	}
	if !exists {
		return 0, nil, nil
	}

	entries, err := os.ReadDir(logDir)
	if err != nil {
		return 0, nil, fmt.Errorf("reading log directory %s: %w", logDir, err)
	}

	cutoff := asOf.Add(-time.Duration(maxAgeDays*hoursPerDay) * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := logFilePattern.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		// Parse in the caller's zone (carried by asOf). New() stamps
		// filenames from time.Now(), so callers pass time.Now() as asOf
		// and the zones line up without reading a package global.
		ts, parseErr := time.ParseInLocation(logFilenameLayout, m[1], asOf.Location())
		if parseErr != nil {
			// The regex validates shape only, not calendar correctness
			// (e.g. month 13 or day 45). time.Parse rejects those; skip.
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
