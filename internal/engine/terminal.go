package engine

import (
	"strings"
	"unicode"
)

// TerminalColorMode makes terminal styling an explicit presentation choice so
// external text can be sanitized before Shuttle adds its own ANSI sequences.
type TerminalColorMode uint8

const (
	// TerminalColorDisabled is the safe zero mode for plain terminal output.
	TerminalColorDisabled TerminalColorMode = iota
	// TerminalColorEnabled permits only Shuttle-owned styling after sanitization.
	TerminalColorEnabled
)

// ProgressMode distinguishes plain output from terminal cursor manipulation
// without making a boolean parameter carry presentation policy.
type ProgressMode uint8

const (
	// ProgressNonInteractive is the safe zero mode for pipes, cron, and captures.
	ProgressNonInteractive ProgressMode = iota
	// ProgressInteractive permits Shuttle-owned cursor controls for a terminal.
	ProgressInteractive
)

// ProgressOptions keeps cursor and color choices typed at the terminal I/O
// boundary, with a zero value that is safe for pipes, cron, and log capture.
type ProgressOptions struct {
	// Mode controls whether Shuttle may use cursor manipulation for live updates.
	Mode ProgressMode
	// Color controls whether Shuttle may add styling after external text is clean.
	Color TerminalColorMode
}

// SanitizeTerminalText removes every Unicode control rune so untrusted display
// text cannot drive a terminal while its printable content remains visible.
func SanitizeTerminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
