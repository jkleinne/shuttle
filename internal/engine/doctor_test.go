package engine

import (
	"bytes"
	"strings"
	"testing"
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
