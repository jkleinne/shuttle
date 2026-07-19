package engine

import (
	"fmt"
	"os"
	"os/exec"
)

// PrerequisiteRequest identifies only the external prerequisites needed by selected jobs.
type PrerequisiteRequest struct {
	// NeedsRsync avoids probing rsync when no selected job uses it.
	NeedsRsync bool
	// NeedsRclone avoids probing rclone when no selected job uses it.
	NeedsRclone bool
	// FilterFiles prevents selected rclone jobs from starting with missing configuration.
	FilterFiles []string
}

// SystemPrerequisiteChecker performs the executable and filter probes requested by Runner.
type SystemPrerequisiteChecker struct{}

// NewSystemPrerequisiteChecker creates the concrete prerequisite boundary used by the CLI.
func NewSystemPrerequisiteChecker() *SystemPrerequisiteChecker {
	return &SystemPrerequisiteChecker{}
}

// Check preserves system causes so callers can distinguish missing tools and files.
func (*SystemPrerequisiteChecker) Check(request PrerequisiteRequest) error {
	if request.NeedsRsync {
		if _, err := exec.LookPath("rsync"); err != nil {
			return fmt.Errorf("rsync not found on PATH: %w", err)
		}
	}
	if request.NeedsRclone {
		if _, err := exec.LookPath("rclone"); err != nil {
			return fmt.Errorf("rclone not found on PATH: %w", err)
		}
	}
	for _, filterFile := range request.FilterFiles {
		if _, err := os.Stat(filterFile); err != nil {
			return fmt.Errorf("rclone filter file not found: %s: %w", filterFile, err)
		}
	}
	return nil
}
