// Package engine implements the sync execution pipeline: stats types, output
// parsers, duration formatting, and summary rendering.
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandPath expands tilde in the source path and stats it to determine
// whether it's a file or directory. Used for [[sync]] job sources.
func expandPath(source string) (resolved string, isDir bool, err error) {
	resolved = expandTilde(source)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", resolved, err)
	}
	return resolved, info.IsDir(), nil
}

// resolveCloudSource resolves a [[cloud.items]] source path. It handles three
// source types: rclone remotes, absolute local paths, and paths relative to
// the external drive.
func resolveCloudSource(source, externalDrive string) (resolved string, isRemote, isDir bool, err error) {
	// Rclone remote: contains ':' but doesn't start with '/' or '~'.
	if strings.Contains(source, ":") && !strings.HasPrefix(source, "/") && !strings.HasPrefix(source, "~") {
		return source, true, true, nil
	}

	// Absolute or tilde path: resolve in place.
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") {
		expanded := expandTilde(source)
		info, statErr := os.Stat(expanded)
		if statErr != nil {
			return "", false, false, fmt.Errorf("stat %s: %w", expanded, statErr)
		}
		return expanded, false, info.IsDir(), nil
	}

	// Relative path: resolve against the external drive.
	full := filepath.Join(externalDrive, source)
	info, statErr := os.Stat(full)
	if statErr != nil {
		return "", false, false, fmt.Errorf("stat %s (relative to %s): %w", source, externalDrive, statErr)
	}
	return full, false, info.IsDir(), nil
}

// expandTilde replaces a leading '~' with the current user's home directory.
// String concatenation is used instead of filepath.Join so that a trailing
// slash in the input (e.g. "~/") is preserved in the returned path.
//
// Note: this helper is intentionally duplicated from the config package.
// Extracting a shared internal/pathutil package for a single 6-line function
// would be premature abstraction.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, _ := os.UserHomeDir()
	return home + path[1:]
}
