package engine

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jkleinne/shuttle/internal/config"
)

var (
	// ErrNoJobsSelected prevents operational I/O when selection yields no executable work.
	ErrNoJobsSelected = errors.New("no jobs selected")
	// ErrInvalidRunPlan distinguishes absent planning input from operational runner failures.
	ErrInvalidRunPlan = errors.New("invalid run plan")
)

// RunOptions carries borrowed user selection intent into pure run planning.
type RunOptions struct {
	// DryRun preserves preview intent in the immutable execution snapshot.
	DryRun bool
	// SkipJobs excludes named jobs while retaining their summary placeholders.
	SkipJobs []string
	// OnlyJobs limits execution to named jobs while retaining other placeholders.
	OnlyJobs []string
	// SelectedRemotes limits rclone expansion without changing configured remote order.
	SelectedRemotes []string
}

type plannedJob struct {
	job      config.Job
	selected bool
	remotes  []string
}

// RunPlan is an opaque, immutable snapshot shared by prerequisite and execution stages.
type RunPlan struct {
	rsyncDefaults  *config.RsyncDefaults
	rcloneDefaults *config.RcloneDefaults
	jobs           []plannedJob
	prerequisites  PrerequisiteRequest
	dryRun         bool
	valid          bool
}

// RequiresRclone lets boundaries resolve credentials only when selected work invokes rclone.
func (p RunPlan) RequiresRclone() bool {
	return p.valid && p.prerequisites.NeedsRclone
}

func (p RunPlan) prerequisiteRequest() PrerequisiteRequest {
	request := p.prerequisites
	request.FilterFiles = slices.Clone(request.FilterFiles)
	return request
}

// BuildRunPlan owns ordered job and remote selection before any operational I/O begins.
func BuildRunPlan(cfg *config.Config, options RunOptions) (RunPlan, error) {
	if cfg == nil {
		return RunPlan{}, fmt.Errorf("building run plan: config is nil: %w", ErrInvalidRunPlan)
	}

	plan := RunPlan{
		dryRun: options.DryRun,
		valid:  true,
	}
	if cfg.Defaults != nil {
		plan.rsyncDefaults = cloneRsyncDefaults(cfg.Defaults.Rsync)
		plan.rcloneDefaults = cloneRcloneDefaults(cfg.Defaults.Rclone)
	}

	operationCount := 0
	filterFiles := make(map[string]bool)
	for _, configuredJob := range cfg.Jobs {
		planned := buildPlannedJob(configuredJob, options)
		plan.jobs = append(plan.jobs, planned)
		if !planned.selected {
			continue
		}
		operationCount += plannedOperationCount(planned)
		recordPrerequisite(&plan, planned, filterFiles)
	}

	if operationCount == 0 {
		return RunPlan{}, fmt.Errorf("building run plan: %w", ErrNoJobsSelected)
	}
	return plan, nil
}

func buildPlannedJob(job config.Job, options RunOptions) plannedJob {
	planned := plannedJob{job: cloneJob(job)}
	if !shouldRunJob(planned.job.Name, options.SkipJobs, options.OnlyJobs) {
		return planned
	}
	if planned.job.Engine == config.EngineRclone {
		planned.remotes = selectRemotes(planned.job.Remotes, options.SelectedRemotes)
		planned.selected = len(planned.remotes) > 0
		return planned
	}
	planned.selected = planned.job.Engine == config.EngineRsync
	return planned
}

func plannedOperationCount(planned plannedJob) int {
	if planned.job.Engine == config.EngineRclone {
		return len(planned.remotes)
	}
	return 1
}

func recordPrerequisite(plan *RunPlan, planned plannedJob, filterFiles map[string]bool) {
	if planned.job.Engine == config.EngineRsync {
		plan.prerequisites.NeedsRsync = true
		return
	}
	plan.prerequisites.NeedsRclone = true
	filterFile := planned.job.FilterFile
	if filterFile == "" && plan.rcloneDefaults != nil {
		filterFile = plan.rcloneDefaults.FilterFile
	}
	if filterFile == "" || filterFiles[filterFile] {
		return
	}
	filterFiles[filterFile] = true
	plan.prerequisites.FilterFiles = append(plan.prerequisites.FilterFiles, filterFile)
}

func cloneRsyncDefaults(defaults *config.RsyncDefaults) *config.RsyncDefaults {
	if defaults == nil {
		return nil
	}
	clone := *defaults
	clone.Flags = slices.Clone(defaults.Flags)
	return &clone
}

func cloneRcloneDefaults(defaults *config.RcloneDefaults) *config.RcloneDefaults {
	if defaults == nil {
		return nil
	}
	clone := *defaults
	clone.Flags = slices.Clone(defaults.Flags)
	return &clone
}

func cloneJob(job config.Job) config.Job {
	clone := job
	clone.ExtraFlags = slices.Clone(job.ExtraFlags)
	clone.Sources = slices.Clone(job.Sources)
	clone.Remotes = slices.Clone(job.Remotes)
	return clone
}

func selectRemotes(configured, selected []string) []string {
	if len(selected) == 0 {
		return slices.Clone(configured)
	}
	filtered := make([]string, 0, len(configured))
	for _, remote := range configured {
		if slices.Contains(selected, remote) {
			filtered = append(filtered, remote)
		}
	}
	return filtered
}

// ValidateJobNames rejects conflicting or unknown selection before planning owns execution order.
func ValidateJobNames(skip, only, jobNames []string) error {
	if len(skip) > 0 && len(only) > 0 {
		return errors.New("--skip and --only are mutually exclusive")
	}

	valid := make(map[string]bool, len(jobNames))
	for _, name := range jobNames {
		valid[name] = true
	}

	for _, name := range skip {
		if !valid[name] {
			return fmt.Errorf("unknown job %q in --skip; valid names: %v", name, sortedKeys(valid))
		}
	}
	for _, name := range only {
		if !valid[name] {
			return fmt.Errorf("unknown job %q in --only; valid names: %v", name, sortedKeys(valid))
		}
	}
	return nil
}

func shouldRunJob(name string, skip, only []string) bool {
	if len(only) > 0 {
		return slices.Contains(only, name)
	}
	return !slices.Contains(skip, name)
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
