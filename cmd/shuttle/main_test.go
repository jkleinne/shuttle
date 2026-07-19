package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jkleinne/shuttle/internal/log"
)

// shuttleBin holds the path to the built test binary, shared across all tests.
var shuttleBin string

// TestMain builds the shuttle binary once before any tests run, then tears
// down the temp directory after all tests complete. All test functions share
// the same binary to avoid repeated compilation overhead.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shuttle-cli-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		os.Exit(2)
	}

	shuttleBin = filepath.Join(dir, "shuttle")
	// Build from repo root; test files run from the package directory.
	buildCmd := exec.Command("go", "build", "-race", "-o", shuttleBin, "./cmd/shuttle")
	buildCmd.Dir = filepath.Join("..", "..")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building shuttle: %v\n%s\n", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(2)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// cliResult holds the captured output and exit code from a shuttle invocation.
type cliResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runShuttle invokes the shuttle binary with the given environment and
// arguments, captures stdout and stderr separately, and returns the exit code.
// The env slice replaces the process environment entirely so tests are
// isolated from the caller's environment.
func runShuttle(t *testing.T, env []string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(shuttleBin, args...)
	cmd.Env = env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()

	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("running shuttle: %v", err)
		}
	}

	return cliResult{
		stdout:   outBuf.String(),
		stderr:   errBuf.String(),
		exitCode: code,
	}
}

// writeConfig writes a config.toml file at <tempdir>/shuttle/config.toml and
// returns an env slice suitable for runShuttle. XDG_CONFIG_HOME and
// XDG_STATE_HOME both point to temp directories, HOME is set to avoid
// accidentally resolving the real home directory, and PATH is preserved so
// external tools (rsync, rclone) remain available.
func writeConfig(t *testing.T, toml string) []string {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), "xdg-config")
	stateDir := filepath.Join(t.TempDir(), "xdg-state")

	shuttleConfigDir := filepath.Join(configDir, "shuttle")
	if err := os.MkdirAll(shuttleConfigDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	configPath := filepath.Join(shuttleConfigDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(toml), 0o644); err != nil {
		t.Fatalf("writing config file: %v", err)
	}

	return []string{
		"XDG_CONFIG_HOME=" + configDir,
		"XDG_STATE_HOME=" + stateDir,
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
}

func TestResolveColor(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		stdoutIsTTY bool
		noColor     bool
		want        bool
	}{
		{"never + TTY", colorNever, true, false, false},
		{"never + non-TTY", colorNever, false, false, false},
		{"always + TTY", colorAlways, true, false, true},
		{"always + non-TTY", colorAlways, false, false, true},
		{"auto + TTY", colorAuto, true, false, true},
		{"auto + non-TTY", colorAuto, false, false, false},
		{"NO_COLOR overrides always + TTY", colorAlways, true, true, false},
		{"NO_COLOR overrides always + non-TTY", colorAlways, false, true, false},
		{"NO_COLOR overrides auto + TTY", colorAuto, true, true, false},
		{"NO_COLOR overrides auto + non-TTY", colorAuto, false, true, false},
		{"NO_COLOR with never", colorNever, true, true, false},
		{"NO_COLOR with never non-TTY", colorNever, false, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveColor(tc.mode, tc.stdoutIsTTY, tc.noColor)
			if got != tc.want {
				t.Errorf("resolveColor(%q, %v, %v) = %v, want %v",
					tc.mode, tc.stdoutIsTTY, tc.noColor, got, tc.want)
			}
		})
	}
}

func TestResolveVerbosity(t *testing.T) {
	tests := []struct {
		name    string
		quiet   bool
		verbose bool
		want    log.Verbosity
	}{
		{"neither flag -> normal", false, false, log.VerbosityNormal},
		{"quiet -> quiet", true, false, log.VerbosityQuiet},
		{"verbose -> verbose", false, true, log.VerbosityVerbose},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveVerbosity(tc.quiet, tc.verbose)
			if got != tc.want {
				t.Errorf("resolveVerbosity(%v, %v) = %v, want %v", tc.quiet, tc.verbose, got, tc.want)
			}
		})
	}
}

func TestValidateColorMode(t *testing.T) {
	valid := []string{colorAuto, colorAlways, colorNever}
	for _, m := range valid {
		if err := validateColorMode(m); err != nil {
			t.Errorf("validateColorMode(%q) returned error: %v", m, err)
		}
	}
	invalid := []string{"", "yes", "true", "on", "AUTO"}
	for _, m := range invalid {
		if err := validateColorMode(m); err == nil {
			t.Errorf("validateColorMode(%q) returned nil, want error", m)
		}
	}
}

func TestCLI_Version_PrintsVersionCommitAndDate(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH")}
	result := runShuttle(t, env, "version")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.exitCode)
	}
	for _, want := range []string{"shuttle", "commit:", "built:"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", result.stdout, want)
		}
	}
}

func TestCLI_Validate_ValidConfig_Succeeds(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	toml := fmt.Sprintf(`
[[job]]
name = "test"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env, "validate")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stdout, "config ok") {
		t.Errorf("stdout = %q, want it to contain 'config ok'", result.stdout)
	}
}

func TestCLI_Validate_MalformedConfig_ConfigError(t *testing.T) {
	env := writeConfig(t, "not valid toml {{{{")
	result := runShuttle(t, env, "validate")
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", result.exitCode)
	}
	if result.stderr == "" {
		t.Error("stderr should contain error text")
	}
}

func TestCLI_MalformedConfig_RunConfigError(t *testing.T) {
	env := writeConfig(t, "not valid toml {{{{")
	result := runShuttle(t, env)
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", result.exitCode)
	}
	if result.stderr == "" {
		t.Error("stderr should contain error text")
	}
}

func TestCLI_UnknownJobName_UsageError(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	toml := fmt.Sprintf(`
[[job]]
name = "backup"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env, "--skip", "nonexistent")
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", result.exitCode)
	}
	if !strings.Contains(result.stderr, "unknown job") {
		t.Errorf("stderr = %q, want it to contain 'unknown job'", result.stderr)
	}
}

func TestCLI_ValidRsyncRun_Succeeds(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	toml := fmt.Sprintf(`
[defaults.rsync]
flags = ["-a"]

[[job]]
name = "test-sync"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env)
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
}

func TestCLI_ColorFlag_InvalidValue_UsageError(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	toml := fmt.Sprintf(`
[[job]]
name = "x"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env, "--color", "bogus")
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, "invalid --color value") {
		t.Errorf("stderr = %q, want it to contain 'invalid --color value'", result.stderr)
	}
}

func TestCLI_QuietAndVerbose_MutuallyExclusive(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	toml := fmt.Sprintf(`
[[job]]
name = "x"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env, "--quiet", "--verbose")
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, "mutually exclusive") {
		t.Errorf("stderr = %q, want it to mention 'mutually exclusive'", result.stderr)
	}
}

func TestCLI_Quiet_SuppressesStdoutOnSuccess(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	toml := fmt.Sprintf(`
[defaults.rsync]
flags = ["-a"]

[[job]]
name = "quiet-test"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env, "--quiet")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	if result.stdout != "" {
		t.Errorf("stdout should be empty in quiet success, got: %q", result.stdout)
	}
}

func TestCLI_Quiet_PrintsSummaryOnStderrOnFailure(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	dst := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	toml := fmt.Sprintf(`
[defaults.rsync]
flags = ["-a"]

[[job]]
name = "quiet-fail"
engine = "rsync"
sources = [%q]
destination = %q
`, missing, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env, "--quiet")
	if result.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", result.exitCode, result.stderr)
	}
	if result.stdout != "" {
		t.Errorf("stdout should be empty even on failure, got: %q", result.stdout)
	}
	if !strings.Contains(result.stderr, "Log:") {
		t.Errorf("stderr should carry the summary + log path on failure, got: %q", result.stderr)
	}
}

func TestCLI_Verbose_PrintsExecLines(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	toml := fmt.Sprintf(`
[defaults.rsync]
flags = ["-a"]

[[job]]
name = "verbose-test"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env, "--verbose")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stdout, "exec: rsync") {
		t.Errorf("verbose stdout should contain 'exec: rsync ...', got: %q", result.stdout)
	}
}

func TestCLI_ColorAlways_EmitsAnsiEvenWhenPiped(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	toml := fmt.Sprintf(`
[defaults.rsync]
flags = ["-a"]

[[job]]
name = "color-test"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)
	// runShuttle captures stdout via an io.Writer, so stdout is not a TTY
	// from the child process's perspective. With --color=always, color codes
	// must still appear; NO_COLOR is not set in env.
	result := runShuttle(t, env, "--color", "always")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stdout, "\x1b[") {
		t.Errorf("--color=always should emit ANSI codes on piped stdout, got: %q", result.stdout)
	}
}

func TestCLI_LogRotation_PrunesStaleFilesOnStartup(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	// Write a stale pre-existing log file into what will become the log
	// directory. Use a retention of 7 days and a filename timestamped 30
	// days in the past so it falls outside the window.
	toml := fmt.Sprintf(`
[defaults]
log_retention_days = 7

[defaults.rsync]
flags = ["-a"]

[[job]]
name = "prune-test"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
	env := writeConfig(t, toml)

	// Extract the state dir from the env we just wrote so we can plant a
	// stale log file before launching the binary.
	var stateDir string
	for _, kv := range env {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			stateDir = strings.TrimPrefix(kv, "XDG_STATE_HOME=")
			break
		}
	}
	if stateDir == "" {
		t.Fatal("XDG_STATE_HOME missing from env")
	}
	logDir := filepath.Join(stateDir, "shuttle", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("creating log dir: %v", err)
	}
	staleName := time.Now().AddDate(0, 0, -30).Format("2006-01-02_150405") + ".log"
	stalePath := filepath.Join(logDir, staleName)
	if err := os.WriteFile(stalePath, []byte("ancient"), 0o644); err != nil {
		t.Fatalf("writing stale log: %v", err)
	}

	result := runShuttle(t, env)
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale log %q should have been pruned, stat err: %v", stalePath, err)
	}

	// And a fresh log for this run should exist.
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("reading log dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one log file from this run, got none")
	}
}

// writeConfigTo writes the given TOML to an explicit path (not the XDG
// default) and returns a minimal env slice. No XDG_CONFIG_HOME is set, so the
// child process can only find the config via --config or $SHUTTLE_CONFIG.
func writeConfigTo(t *testing.T, path, toml string) []string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating parent dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("writing config file: %v", err)
	}
	stateDir := filepath.Join(t.TempDir(), "xdg-state")
	return []string{
		"XDG_STATE_HOME=" + stateDir,
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
}

// rsyncJobTOML builds a minimal valid rsync job config for --config tests.
func rsyncJobTOML(t *testing.T, src, dst string) string {
	t.Helper()
	return fmt.Sprintf(`
[defaults.rsync]
flags = ["-a"]

[[job]]
name = "explicit-config"
engine = "rsync"
sources = [%q]
destination = %q
`, src, dst)
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		in   string
		want string
	}{
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~", home},
		{"~/config.toml", filepath.Join(home, "config.toml")},
		{"~/nested/dir/file", filepath.Join(home, "nested/dir/file")},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := expandHome(tc.in)
			if err != nil {
				t.Fatalf("expandHome(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("expandHome(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveConfigPath_DefaultRelativeXDG_ReturnsAbsolute(t *testing.T) {
	t.Setenv(envConfigPath, "")
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	want, err := filepath.Abs(filepath.Join("relative-config", "shuttle", "config.toml"))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}

	got, explicit, err := resolveConfigPath("")

	if err != nil {
		t.Fatalf("resolveConfigPath() error = %v", err)
	}
	if explicit {
		t.Error("explicit = true, want false for default path")
	}
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestLogDirectory_RelativeXDG_ReturnsAbsolute(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative-state")
	want, err := filepath.Abs(filepath.Join("relative-state", "shuttle", "logs"))
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}

	got, err := logDirectory()

	if err != nil {
		t.Fatalf("logDirectory() error = %v", err)
	}
	if got != want {
		t.Errorf("logDirectory() = %q, want %q", got, want)
	}
}

func TestLogDirectory_MissingHome_ReturnsError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")

	got, err := logDirectory()

	if err == nil {
		t.Fatalf("logDirectory() = (%q, nil), want home lookup error", got)
	}
	if !strings.Contains(err.Error(), "home") {
		t.Errorf("error = %q, want it to mention home", err)
	}
}

func TestCLI_ConfigFlag_ValidPath_Succeeds(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "alt-config.toml")
	env := writeConfigTo(t, configPath, rsyncJobTOML(t, src, dst))

	result := runShuttle(t, env, "--config", configPath)
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
}

func TestCLI_ConfigFlag_MissingPath_UsageError(t *testing.T) {
	env := []string{
		"XDG_STATE_HOME=" + t.TempDir(),
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
	missing := filepath.Join(t.TempDir(), "nope.toml")
	result := runShuttle(t, env, "--config", missing)
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, missing) {
		t.Errorf("stderr should mention the path %q, got: %q", missing, result.stderr)
	}
}

func TestCLI_ConfigFlag_TildeExpansion_Succeeds(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	home := t.TempDir()
	src := filepath.Join(home, "src")
	dst := filepath.Join(home, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "my-config.toml")
	if err := os.WriteFile(configPath, []byte(rsyncJobTOML(t, src, dst)), 0o644); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"HOME=" + home,
		"XDG_STATE_HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
	// The child expands ~ against its own HOME, which points at the temp dir
	// holding the config file.
	result := runShuttle(t, env, "--config", "~/my-config.toml", "validate")
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stdout, configPath) {
		t.Errorf("stdout should contain expanded path %q, got: %q", configPath, result.stdout)
	}
}

func TestCLI_ConfigFlag_RelativePath_ResolvedFromCWD(t *testing.T) {
	// Run from a subdirectory with a relative path. The resolver must absolve
	// the path against cwd so the lock hash stays stable and the validate
	// output prints an absolute path.
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "rel.toml")
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(configPath, []byte(rsyncJobTOML(t, src, dst)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a subdir to run from; relative path "../rel.toml" should resolve
	// back up to configPath.
	sub := filepath.Join(configDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + t.TempDir(),
		"XDG_STATE_HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}

	cmd := exec.Command(shuttleBin, "--config", "../rel.toml", "validate")
	cmd.Env = env
	cmd.Dir = sub
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("exit error: %v; stderr: %s", err, errBuf.String())
	}
	// The printed path should be absolute and equal to configPath.
	if !strings.Contains(out.String(), configPath) {
		t.Errorf("stdout should contain absolute path %q, got: %q", configPath, out.String())
	}
	if strings.Contains(out.String(), "../rel.toml") {
		t.Errorf("stdout should not contain the relative input, got: %q", out.String())
	}
}

func TestCLI_ConfigEnv_ValidPath_Succeeds(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "env-config.toml")
	env := writeConfigTo(t, configPath, rsyncJobTOML(t, src, dst))
	env = append(env, "SHUTTLE_CONFIG="+configPath)

	result := runShuttle(t, env)
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
}

func TestCLI_ConfigEnv_MissingPath_UsageError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.toml")
	env := []string{
		"XDG_STATE_HOME=" + t.TempDir(),
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"SHUTTLE_CONFIG=" + missing,
	}
	result := runShuttle(t, env, "validate")
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stderr, missing) {
		t.Errorf("stderr should mention the path %q, got: %q", missing, result.stderr)
	}
}

func TestCLI_ConfigFlag_OverridesEnv(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The env var points to a missing file; the flag points to a valid one.
	// If precedence works, the run succeeds. If the env wins, it fails with exit 2.
	good := filepath.Join(t.TempDir(), "good.toml")
	if err := os.WriteFile(good, []byte(rsyncJobTOML(t, src, dst)), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "nope.toml")

	env := []string{
		"XDG_STATE_HOME=" + t.TempDir(),
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"SHUTTLE_CONFIG=" + bad,
	}
	result := runShuttle(t, env, "--config", good)
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0 (flag should override env); stderr: %s",
			result.exitCode, result.stderr)
	}
}

func TestCLI_Validate_ConfigFlag_ValidPath_PrintsResolvedPath(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "validate-me.toml")
	env := writeConfigTo(t, configPath, rsyncJobTOML(t, src, dst))

	result := runShuttle(t, env, "validate", "--config", configPath)
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.exitCode, result.stderr)
	}
	if !strings.Contains(result.stdout, "config ok") {
		t.Errorf("stdout should contain 'config ok', got: %q", result.stdout)
	}
	if !strings.Contains(result.stdout, configPath) {
		t.Errorf("stdout should contain resolved path %q, got: %q", configPath, result.stdout)
	}
}

func TestCLI_Validate_ConfigFlag_MissingPath_UsageError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	env := []string{
		"XDG_STATE_HOME=" + t.TempDir(),
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
	result := runShuttle(t, env, "validate", "--config", missing)
	if result.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", result.exitCode, result.stderr)
	}
}

func TestCLI_MissingSource_PartialFailure(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	dst := t.TempDir()
	missingSource := filepath.Join(t.TempDir(), "does-not-exist")
	toml := fmt.Sprintf(`
[defaults.rsync]
flags = ["-a"]

[[job]]
name = "broken"
engine = "rsync"
sources = [%q]
destination = %q
`, missingSource, dst)
	env := writeConfig(t, toml)
	result := runShuttle(t, env)
	if result.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 (partial failure); stderr: %s",
			result.exitCode, result.stderr)
	}
}

func TestDoctor_CleanConfig_ExitZero(t *testing.T) {
	env := writeConfig(t, `
[[job]]
name = "local"
engine = "rsync"
sources = ["/tmp"]
destination = "/tmp/backup"
`)
	res := runShuttle(t, env, "doctor")
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", res.exitCode, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "shuttle doctor") {
		t.Errorf("stdout missing header: %q", res.stdout)
	}
}

func TestDoctor_FailingChecks_ExitTwo(t *testing.T) {
	// An rclone job referencing an undefined remote (no rclone config under the
	// test HOME) and a missing filter file both produce FAILs → exit 2.
	env := writeConfig(t, `
[[job]]
name = "cloud"
engine = "rclone"
source = "/tmp"
remotes = ["ghost_remote"]
mode = "copy"
filter_file = "/no/such/filter.txt"
`)
	res := runShuttle(t, env, "doctor")
	if res.exitCode != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", res.exitCode, res.stdout, res.stderr)
	}
}

func TestCLI_OptionalMissing_ExitZero(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not found on PATH; test requires rclone for prerequisite check")
	}
	// Config declares an rclone job with a non-existent local source and
	// optional = true. Run should exit 0, summary should show an optional
	// tally segment, and no "failed" count.
	missing := filepath.Join(t.TempDir(), "koreader-absent")
	toml := `
[[job]]
name = "koreader-to-cloud"
engine = "rclone"
source = "` + missing + `"
remotes = ["crypt_gdrive"]
mode = "copy"
optional = true
`
	env := writeConfig(t, toml)
	res := runShuttle(t, env, "run", "--color=never")

	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0. stderr: %s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "1 optional") {
		t.Errorf("stdout missing '1 optional' segment:\n%s", res.stdout)
	}
	if strings.Contains(res.stdout, "failed") {
		t.Errorf("stdout should not mention 'failed' when only optional-missing:\n%s", res.stdout)
	}
}

func TestResolveRclonePassword_EnvPreset_ReturnsEmpty(t *testing.T) {
	t.Setenv("RCLONE_CONFIG_PASS", "already-set")
	logger, err := log.NewWithWriter(&bytes.Buffer{}, filepath.Join(t.TempDir(), "t.log"), false, log.VerbosityNormal)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer logger.Close()

	if got := resolveRclonePassword(logger); got != "" {
		t.Errorf("resolveRclonePassword() = %q, want \"\" when env is preset", got)
	}
	if v := os.Getenv("RCLONE_CONFIG_PASS"); v != "already-set" {
		t.Errorf("RCLONE_CONFIG_PASS = %q, want unchanged 'already-set'", v)
	}
}

// assertSignalExitsWithSignalCode verifies the public exit-code contract for
// interrupted runs: the given signal during an active job yields exit 130 and
// the interrupt notice on stderr. The job is throttled via --bwlimit so the
// process is reliably still alive when the signal lands; the exit code is
// 130 regardless of which pipeline stage the cancellation interrupts.
func assertSignalExitsWithSignalCode(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not found on PATH")
	}
	src := t.TempDir()
	payload := make([]byte, 1<<20) // 1 MiB at --bwlimit=100 (KB/s) ≈ 10s window
	if err := os.WriteFile(filepath.Join(src, "big.bin"), payload, 0o644); err != nil {
		t.Fatalf("writing payload: %v", err)
	}
	dst := t.TempDir()

	env := writeConfig(t, fmt.Sprintf(`
[[job]]
name = "slow"
engine = "rsync"
sources = [%q]
destination = %q
extra_flags = ["--bwlimit=100"]
`, filepath.Join(src, "big.bin"), dst))

	cmd := exec.Command(shuttleBin, "run")
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting shuttle: %v", err)
	}

	// Signal only after the run has observably started so the process
	// cannot have exited before the kill lands.
	scanner := bufio.NewScanner(stdout)
	started := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "Shuttle Started") {
			started = true
			break
		}
	}
	if !started {
		_ = cmd.Process.Kill()
		t.Fatalf("never saw startup line; stderr: %s", stderrBuf.String())
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("sending %v: %v", sig, err)
	}
	// Drain remaining stdout so the child never blocks on a full pipe.
	go func() { _, _ = io.Copy(io.Discard, stdout) }()

	err = cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait() = %v, want ExitError with code 130", err)
	}
	if code := exitErr.ExitCode(); code != 130 {
		t.Errorf("exit code = %d, want 130; stderr: %s", code, stderrBuf.String())
	}
	if !strings.Contains(stderrBuf.String(), "Interrupted") {
		t.Errorf("stderr = %q, want interrupt notice", stderrBuf.String())
	}
}

func TestCLI_SIGINT_ExitsWithSignalCode(t *testing.T) {
	assertSignalExitsWithSignalCode(t, syscall.SIGINT)
}

// SIGTERM is what cron and launchd send on shutdown, so the second registered
// signal gets the same contract coverage as the interactive ctrl-C path.
func TestCLI_SIGTERM_ExitsWithSignalCode(t *testing.T) {
	assertSignalExitsWithSignalCode(t, syscall.SIGTERM)
}
