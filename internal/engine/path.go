// Package engine implements the sync execution pipeline: stats types, output
// parsers, duration formatting, and summary rendering.
package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandPath stats the given path and returns whether it's a directory.
// Tilde expansion is handled by the config package at parse time, so
// paths arriving here are already absolute.
func expandPath(source string) (resolved string, isDir bool, err error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", source, err)
	}
	return source, info.IsDir(), nil
}

// isRcloneRemote returns true when the source looks like an rclone remote
// (contains ':' but does not start with '/' or '~').
func isRcloneRemote(source string) bool {
	return strings.Contains(source, ":") &&
		!strings.HasPrefix(source, "/") &&
		!strings.HasPrefix(source, "~")
}

// rcloneDestName determines the destination folder name for a cloud item.
// Uses jobDest if set, otherwise derives from the source basename.
// For remote sources (containing ':'), extracts the path after the colon.
func rcloneDestName(jobDest, source string, isRemote bool) string {
	if jobDest != "" {
		return jobDest
	}
	if isRemote {
		parts := strings.SplitN(source, ":", 2)
		if len(parts) > 1 && parts[1] != "" && parts[1] != "/" {
			return filepath.Base(strings.TrimRight(parts[1], "/"))
		}
		return ""
	}
	return filepath.Base(strings.TrimRight(source, "/"))
}
