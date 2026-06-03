package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkleinne/shuttle/internal/config"
)

func TestRenderReport_Symbols(t *testing.T) {
	tests := []struct {
		name  string
		level CheckLevel
		want  string
	}{
		{"ok", CheckOK, "✓"},
		{"warn", CheckWarn, "⚠"},
		{"fail", CheckFail, "✗"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			RenderReport(&buf, Report{Checks: []CheckResult{{Name: "x", Level: tt.level, Detail: "d"}}}, false)
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output %q missing symbol %q", buf.String(), tt.want)
			}
		})
	}
}

func TestRenderReport_Tally_OmitsZeroSegments(t *testing.T) {
	var buf bytes.Buffer
	RenderReport(&buf, Report{Checks: []CheckResult{
		{Name: "rsync", Level: CheckOK, Detail: "3.2.7"},
		{Name: "remote", Level: CheckFail, Detail: "koofr — not found in rclone config"},
	}}, false)
	out := buf.String()
	if !strings.Contains(out, "1 ok") || !strings.Contains(out, "1 failed") {
		t.Errorf("tally missing counts: %q", out)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("tally should omit zero warnings: %q", out)
	}
}

func TestReport_HasFailures(t *testing.T) {
	if (Report{Checks: []CheckResult{{Level: CheckOK}, {Level: CheckWarn}}}).HasFailures() {
		t.Error("HasFailures = true, want false (ok+warn only)")
	}
	if !(Report{Checks: []CheckResult{{Level: CheckWarn}, {Level: CheckFail}}}).HasFailures() {
		t.Error("HasFailures = false, want true (has fail)")
	}
}

func TestRenderReport_Color_WrapsSymbolInANSI(t *testing.T) {
	var buf bytes.Buffer
	RenderReport(&buf, Report{Checks: []CheckResult{{Name: "rsync", Level: CheckOK, Detail: "3.2.7"}}}, true)
	out := buf.String()
	if !strings.Contains(out, ansiGreen+symbolOK+ansiReset) {
		t.Errorf("colored output should wrap the OK glyph in green ANSI codes; got %q", out)
	}
}

func TestRenderReport_EmptyReport_RendersNoChecksRun(t *testing.T) {
	var buf bytes.Buffer
	RenderReport(&buf, Report{}, false)
	out := buf.String()
	if !strings.Contains(out, "shuttle doctor") {
		t.Errorf("empty report should still render the header; got %q", out)
	}
	if !strings.Contains(out, "no checks run") {
		t.Errorf("empty report should render the no-checks tally; got %q", out)
	}
}

func findCheck(t *testing.T, rep Report, name string) CheckResult {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in report", name)
	return CheckResult{}
}

func TestDiagnose_ConfigOK(t *testing.T) {
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: &config.Config{}, Explicit: true})
	if findCheck(t, rep, "config").Level != CheckOK {
		t.Error("loaded config: want OK")
	}
}

func TestDiagnose_ConfigMissing_ExplicitFails(t *testing.T) {
	rep := Diagnose(context.Background(), ConfigStatus{
		Path:     "/no/such.toml",
		LoadErr:  fmt.Errorf("reading config /no/such.toml: %w", fs.ErrNotExist),
		Explicit: true,
	})
	if findCheck(t, rep, "config").Level != CheckFail {
		t.Error("explicit missing config: want FAIL")
	}
}

func TestDiagnose_ConfigMissing_DefaultWarns(t *testing.T) {
	rep := Diagnose(context.Background(), ConfigStatus{
		Path:    "/home/u/.config/shuttle/config.toml",
		LoadErr: fmt.Errorf("reading config: %w", fs.ErrNotExist),
	})
	if findCheck(t, rep, "config").Level != CheckWarn {
		t.Error("default missing config: want WARN")
	}
}

func TestDiagnose_ConfigInvalid_Fails(t *testing.T) {
	rep := Diagnose(context.Background(), ConfigStatus{
		Path:     "/tmp/c.toml",
		LoadErr:  errors.New("parsing config: bad toml"),
		Explicit: true,
	})
	if findCheck(t, rep, "config").Level != CheckFail {
		t.Error("invalid config: want FAIL")
	}
}

func TestDiagnose_ToolsPresent_OK(t *testing.T) {
	// rsync and rclone are on the dev/CI PATH (other integration tests require them).
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: &config.Config{}})
	for _, name := range []string{"rsync", "rclone"} {
		if findCheck(t, rep, name).Level != CheckOK {
			t.Errorf("%s: want OK (tool on PATH)", name)
		}
	}
}

func TestDiagnose_ToolMissing_SeverityByNeed(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // scrub PATH: no tools found
	cfg := &config.Config{Jobs: []config.Job{
		{Name: "local", Engine: config.EngineRsync, Sources: []string{"/x"}, Destination: "/y"},
	}}
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: cfg})
	if findCheck(t, rep, "rsync").Level != CheckFail {
		t.Error("rsync used + missing: want FAIL")
	}
	if findCheck(t, rep, "rclone").Level != CheckWarn {
		t.Error("rclone unused + missing: want WARN")
	}
}

// ---------------------------------------------------------------------------
// Remote and filter-file checks (Task 3)
// ---------------------------------------------------------------------------

func findRemote(t *testing.T, rep Report, remote string) CheckResult {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == "remote" && strings.HasPrefix(c.Detail, remote) {
			return c
		}
	}
	t.Fatalf("no remote check for %q", remote)
	return CheckResult{}
}

func TestDiagnose_RemoteDefined_OK(t *testing.T) {
	defer writeRcloneConfig(t)()
	cfg := &config.Config{Jobs: []config.Job{
		{Name: "cloud", Engine: config.EngineRclone, Source: "/x", Remotes: []string{"testlocal"}, Mode: config.ModeCopy},
	}}
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: cfg})
	if got := findRemote(t, rep, "testlocal"); got.Level != CheckOK {
		t.Errorf("defined remote: want OK, got %v (%s)", got.Level, got.Detail)
	}
}

func TestDiagnose_RemoteUndefined_Fails(t *testing.T) {
	defer writeRcloneConfig(t)()
	cfg := &config.Config{Jobs: []config.Job{
		{Name: "cloud", Engine: config.EngineRclone, Source: "/x", Remotes: []string{"ghost"}, Mode: config.ModeCopy},
	}}
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: cfg})
	if findRemote(t, rep, "ghost").Level != CheckFail {
		t.Error("undefined remote: want FAIL")
	}
}

func TestDiagnose_FilterFileMissing_Fails(t *testing.T) {
	defer writeRcloneConfig(t)()
	cfg := &config.Config{Jobs: []config.Job{
		{Name: "cloud", Engine: config.EngineRclone, Source: "/x", Remotes: []string{"testlocal"}, Mode: config.ModeCopy, FilterFile: "/no/such/filter.txt"},
	}}
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: cfg})
	if findCheck(t, rep, "filter file").Level != CheckFail {
		t.Error("missing filter file: want FAIL")
	}
}

func TestDiagnose_FilterFilePresent_OK(t *testing.T) {
	defer writeRcloneConfig(t)()
	ff := filepath.Join(t.TempDir(), "filter.txt")
	if err := os.WriteFile(ff, []byte("- *.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Jobs: []config.Job{
		{Name: "cloud", Engine: config.EngineRclone, Source: "/x", Remotes: []string{"testlocal"}, Mode: config.ModeCopy, FilterFile: ff},
	}}
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: cfg})
	if findCheck(t, rep, "filter file").Level != CheckOK {
		t.Error("present filter file: want OK")
	}
}

// Security: a remote name with shell metacharacters must be compared, never run.
func TestDiagnose_RemoteNameWithMetachars_NotExecuted(t *testing.T) {
	defer writeRcloneConfig(t)()
	tmp := t.TempDir()
	evil := "x; touch " + filepath.Join(tmp, "pwned")
	cfg := &config.Config{Jobs: []config.Job{
		{Name: "cloud", Engine: config.EngineRclone, Source: "/x", Remotes: []string{evil}, Mode: config.ModeCopy},
	}}
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: cfg})
	if findRemote(t, rep, evil).Level != CheckFail {
		t.Error("metachar remote should be reported not-found")
	}
	if _, err := os.Stat(filepath.Join(tmp, "pwned")); err == nil {
		t.Fatal("command injection: side-effect file was created")
	}
}

// Security: when rclone cannot read its config (here: an encrypted-marker
// config that rclone rejects under --ask-password=false), doctor reports a WARN
// (not a hang, not a FAIL), skips per-remote checks, and leaks no secret. The
// malformed encrypted blob is what makes listremotes exit non-zero; the empty
// RCLONE_CONFIG_PASS just guarantees no ambient password is in play.
func TestDiagnose_EncryptedRcloneConfig_Warns(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "rclone.conf")
	encrypted := "# Encrypted rclone configuration File\n\nRCLONE_ENCRYPT_V0:\nc29tZWdhcmJhZ2VlbmNyeXB0ZWRibG9i\n"
	if err := os.WriteFile(confPath, []byte(encrypted), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RCLONE_CONFIG", confPath)
	t.Setenv("RCLONE_CONFIG_PASS", "")

	cfg := &config.Config{Jobs: []config.Job{
		{Name: "cloud", Engine: config.EngineRclone, Source: "/x", Remotes: []string{"testlocal"}, Mode: config.ModeCopy},
	}}
	rep := Diagnose(context.Background(), ConfigStatus{Path: "/tmp/c.toml", Cfg: cfg})

	var warned bool
	for _, c := range rep.Checks {
		if c.Name == "remotes" && c.Level == CheckWarn {
			warned = true
		}
		if c.Name == "remote" {
			t.Errorf("per-remote check %q should be skipped on read failure", c.Detail)
		}
		if strings.Contains(c.Detail, "RCLONE_CONFIG_PASS") {
			t.Fatalf("check detail leaked secret reference: %q", c.Detail)
		}
	}
	if !warned {
		t.Error("encrypted config without password: want a 'remotes' WARN")
	}
}
