package engine

import (
	"strings"
	"testing"
)

func TestStatusSymbol_TimedOut_ReturnsFailed(t *testing.T) {
	// StatusTimedOut is a failure; it must share the ✗ symbol with StatusFailed.
	got := statusSymbol(StatusTimedOut, false)
	if got != symbolFailed {
		t.Errorf("statusSymbol(StatusTimedOut, false) = %q, want %q", got, symbolFailed)
	}
}

func TestStatusSymbol_TimedOut_ColorEnabled_WrapsRed(t *testing.T) {
	got := statusSymbol(StatusTimedOut, true)
	want := colorize(true, ansiRed, symbolFailed)
	if got != want {
		t.Errorf("statusSymbol(StatusTimedOut, true) = %q, want %q", got, want)
	}
}

func TestItemStatsText_TimedOut_PlainText(t *testing.T) {
	item := ItemResult{Status: StatusTimedOut}
	got := itemStatsText(item, false)
	if got != labelTimedOut {
		t.Errorf("itemStatsText(StatusTimedOut, false) = %q, want %q", got, labelTimedOut)
	}
}

func TestItemStatsText_TimedOut_ColorEnabled_WrapsRed(t *testing.T) {
	item := ItemResult{Status: StatusTimedOut}
	got := itemStatsText(item, true)
	want := colorize(true, ansiRed, labelTimedOut)
	if got != want {
		t.Errorf("itemStatsText(StatusTimedOut, true) = %q, want %q", got, want)
	}
}

func TestRenderSummary_TimedOut_ContainsTimedOutAndTalliesAsFailed(t *testing.T) {
	s := Summary{
		Jobs: []JobResult{
			{
				Name: "backup",
				Items: []ItemResult{
					{Name: "backup", Status: StatusTimedOut},
				},
			},
		},
	}
	var buf strings.Builder
	RenderSummary(&buf, s, false)
	output := buf.String()

	if !strings.Contains(output, "timed out") {
		t.Errorf("RenderSummary output does not contain %q:\n%s", "timed out", output)
	}
	if !strings.Contains(output, "1 failed") {
		t.Errorf("RenderSummary output does not contain %q:\n%s", "1 failed", output)
	}
}
