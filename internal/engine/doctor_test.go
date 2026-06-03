package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
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
