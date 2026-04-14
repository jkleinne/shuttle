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
