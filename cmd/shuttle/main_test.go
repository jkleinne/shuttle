package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	buildCmd := exec.Command("go", "build", "-o", shuttleBin, "./cmd/shuttle")
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
