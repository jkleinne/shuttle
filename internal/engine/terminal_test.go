package engine

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeTerminalText_SanitizesControlRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "plain text", want: "plain text"},
		{name: "CSI", in: "a\x1b[31mb", want: "a[31mb"},
		{name: "OSC", in: "a\x1b]0;title\x07b", want: "a]0;titleb"},
		{name: "C0 newline and tab", in: "a\nb\tc", want: "abc"},
		{name: "C1 CSI", in: "a\u009b31mb", want: "a31mb"},
		{name: "DEL", in: "a\x7fb", want: "ab"},
		{name: "bidi controls", in: "a\u061c\u200e\u202e\u2066b\u2069", want: "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeTerminalText(tt.in); got != tt.want {
				t.Errorf("SanitizeTerminalText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestProgressWriter_SanitizesJobLabels(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  string
	}{
		{name: "CSI", label: "job\x1b[31mname", want: "job[31mname"},
		{name: "OSC", label: "job\x1b]0;title\x07name", want: "job]0;titlename"},
		{name: "C0 newline and tab", label: "job\n\tname", want: "jobname"},
		{name: "C1 CSI", label: "job\u009b31mname", want: "job31mname"},
		{name: "DEL", label: "job\x7fname", want: "jobname"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			progress := NewProgressWriter(&buf, ProgressOptions{})

			progress.StartJob(context.Background(), tt.label)
			progress.FinishJob(ItemResult{Status: StatusFailed})

			want := fmt.Sprintf("%s %s  %s\n", symbolFailed, tt.want, labelFailed)
			if got := buf.String(); got != want {
				t.Errorf("progress output = %q, want %q", got, want)
			}
		})
	}
}

func TestProgressWriter_SanitizesSkipLabels(t *testing.T) {
	var buf bytes.Buffer
	progress := NewProgressWriter(&buf, ProgressOptions{})

	progress.SkipJob("skip\x1b[2J\x1b]0;title\x07\n\t\u009b31m\x7flabel")

	want := fmt.Sprintf("%s skip[2J]0;title31mlabel  %s\n", symbolSkipped, labelSkipped)
	if got := buf.String(); got != want {
		t.Errorf("skip output = %q, want %q", got, want)
	}
}

func TestProgressWriter_UpdateProgressSanitizesSynchronously(t *testing.T) {
	var buf bytes.Buffer
	progress := NewProgressWriter(&buf, ProgressOptions{Mode: ProgressInteractive})

	progress.StartJob(context.Background(), "active")
	progress.UpdateProgress("copy\x1b[31m\x1b]0;title\x07\n\t\u009b32m\x7f 50%")
	progress.FinishJob(ItemResult{Status: StatusOK})

	output := buf.String()
	if !strings.Contains(output, "copy[31m]0;title32m 50%") {
		t.Errorf("interactive output missing sanitized progress text: %q", output)
	}
	for _, control := range []string{"\x1b[31m", "\x1b]0;title", "\u009b32m", "\x7f", "\t"} {
		if strings.Contains(output, control) {
			t.Errorf("interactive output retained external control %q: %q", control, output)
		}
	}
}

func TestRenderSummary_SanitizesExternalValues(t *testing.T) {
	summary := Summary{
		Jobs: []JobResult{
			{
				Name: "job\x1b[31m\nname",
				Items: []ItemResult{
					{
						Name:   "first\titem",
						Status: StatusOK,
						Stats: TransferStats{
							FilesTransferred: 1,
							BytesSent:        "12\x1b]0;title\x07 MiB",
							Speed:            "2\u009b31m MiB/s",
						},
					},
					{Name: "second\x7fitem", Status: StatusOK},
				},
			},
			{
				Name:   "cloud\x1b[2J",
				Remote: "remote\x1b]0;title\x07one",
				Items: []ItemResult{{
					Name:   "ignored",
					Status: StatusOK,
					Stats:  TransferStats{FilesChecked: 1},
				}},
			},
			{
				Name:   "cloud\x1b[2J",
				Remote: "remote\n\t\u009b31mtwo\x7f",
				Items: []ItemResult{{
					Name:   "ignored",
					Status: StatusOK,
					Stats:  TransferStats{FilesChecked: 2},
				}},
			},
		},
		Errors: []string{"failed\x1b[2J\x1b]0;title\x07\n\t\u009b31m\x7fdetail"},
	}
	var buf bytes.Buffer

	RenderSummary(&buf, summary, TerminalColorDisabled)

	output := buf.String()
	for _, want := range []string{
		"job[31mname",
		"firstitem",
		"seconditem",
		"1 transferred, 12]0;title MiB sent at 231m MiB/s",
		"cloud[2J",
		"remote]0;titleone",
		"remote31mtwo",
		"failed[2J]0;title31mdetail",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("summary output missing sanitized value %q: %q", want, output)
		}
	}
	for _, control := range []string{
		"\x1b[31m",
		"\x1b[2J",
		"\x1b]0;title",
		"\u009b31m",
		"\x7f",
		"\t",
	} {
		if strings.Contains(output, control) {
			t.Errorf("summary output retained external control %q: %q", control, output)
		}
	}
}

func TestRenderSummary_SanitizesBeforeColorWrapping(t *testing.T) {
	summary := Summary{Jobs: []JobResult{{
		Name: "colored",
		Items: []ItemResult{{
			Status: StatusOK,
			Stats: TransferStats{
				FilesTransferred: 1,
				BytesSent:        "12\x1b[2J MiB",
				Speed:            "2\x1b]0;title\x07 MiB/s",
			},
		}},
	}}}
	var buf bytes.Buffer

	RenderSummary(&buf, summary, TerminalColorEnabled)

	output := buf.String()
	wantTransfer := ansiGreen + "1 transferred, 12[2J MiB sent at 2]0;title MiB/s" + ansiReset
	if !strings.Contains(output, wantTransfer) {
		t.Errorf("colored output missing sanitized transfer wrapper %q: %q", wantTransfer, output)
	}
	if strings.Contains(output, "\x1b[2J") || strings.Contains(output, "\x1b]0;title") {
		t.Errorf("colored output retained injected terminal sequence: %q", output)
	}
}

func TestRenderReport_SanitizesNamesAndDetailsBeforeAlignment(t *testing.T) {
	report := Report{Checks: []CheckResult{
		{
			Name:   "tool\n",
			Level:  CheckOK,
			Detail: "detail\x1b[2J\x1b]0;title\x07\t\u009b31m\x7f",
		},
		{Name: "longer", Level: CheckWarn, Detail: "plain"},
	}}
	var buf bytes.Buffer

	RenderReport(&buf, report, TerminalColorDisabled)

	output := buf.String()
	if !strings.Contains(output, "  ✓ tool    detail[2J]0;title31m\n") {
		t.Errorf("doctor output did not align the sanitized name and detail: %q", output)
	}
	if !strings.Contains(output, "  ⚠ longer  plain\n") {
		t.Errorf("doctor output missing the longer aligned name: %q", output)
	}
	for _, control := range []string{"\x1b[2J", "\x1b]0;title", "\u009b31m", "\x7f", "\t"} {
		if strings.Contains(output, control) {
			t.Errorf("doctor output retained external control %q: %q", control, output)
		}
	}
}
