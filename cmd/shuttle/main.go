package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/engine"
	"github.com/jkleinne/shuttle/internal/log"
)

// version, commit, and date are set at build time via -ldflags. They default
// to placeholder values so local `go build` invocations still produce a
// runnable binary with an honest "unknown" label.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	os.Exit(run())
}

// run is the real entry point, returning an exit code so main stays testable.
// Exit codes: 0 success, 1 partial task failure, 2 config/usage error, 130 signal.
func run() int {
	var runOpts engine.RunOptions
	var skipJobs, onlyJobs, selectedRemotes []string
	var colorMode string

	rootCmd := &cobra.Command{
		Use:   "shuttle",
		Short: "Automated backup and synchronization tool",
		// No subcommand: delegate to executeRun so `shuttle --dry-run` works.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return validateColorMode(colorMode)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRun(cmd.Context(), skipJobs, onlyJobs, selectedRemotes, colorMode, runOpts)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Execute sync tasks (default when no subcommand given)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRun(cmd.Context(), skipJobs, onlyJobs, selectedRemotes, colorMode, runOpts)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("shuttle %s\n", version)
			fmt.Printf("commit: %s\n", commit)
			fmt.Printf("built:  %s\n", date)
		},
	}

	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Check configuration file for errors",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.ConfigPath()
			if err != nil {
				return err
			}
			if _, err := config.LoadFile(path); err != nil {
				return err
			}
			fmt.Printf("config ok: %s\n", path)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Register the same flags on both root and run so both invocation styles
	// (`shuttle --dry-run` and `shuttle run --dry-run`) accept them.
	for _, cmd := range []*cobra.Command{rootCmd, runCmd} {
		cmd.Flags().BoolVarP(&runOpts.DryRun, "dry-run", "n", false, "Preview changes without modifying files")
		cmd.Flags().StringArrayVar(&skipJobs, "skip", nil, "Skip a job by name (repeatable; mutually exclusive with --only)")
		cmd.Flags().StringArrayVar(&onlyJobs, "only", nil, "Run only named jobs (repeatable; mutually exclusive with --skip)")
		cmd.Flags().StringArrayVar(&selectedRemotes, "remote", nil, "Target specific cloud remote by name (repeatable)")
		cmd.Flags().StringVar(&colorMode, "color", colorAuto, "Colorize terminal output: auto|always|never")
	}

	rootCmd.AddCommand(runCmd, versionCmd, validateCmd)

	// Context wired to OS signals. The goroutine sets signaled before canceling
	// so the exit-code check below can distinguish a signal from a normal error.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var signaled bool
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		signaled = true
		fmt.Fprintln(os.Stderr, "\nInterrupted. Shutting down...")
		cancel()
	}()

	err := rootCmd.ExecuteContext(ctx)

	if signaled {
		return exitSignal
	}
	if errors.Is(err, errPartialFailure) {
		return exitPartialFailure
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return exitUsageError
	}
	return exitSuccess
}

// Exit codes returned by Execute. These form part of the public CLI contract
// consumed by cron, launchd, and shell scripts wrapping shuttle.
const (
	exitSuccess        = 0
	exitPartialFailure = 1
	exitUsageError     = 2
	exitSignal         = 130 // Unix convention: 128 + SIGINT
)

// Valid values for the --color flag.
const (
	colorAuto   = "auto"
	colorAlways = "always"
	colorNever  = "never"
)

// validateColorMode returns an error when mode is not one of the supported
// --color values. Matching is case-sensitive so "AUTO" is rejected, matching
// the behavior of git, ripgrep, and ls.
func validateColorMode(mode string) error {
	switch mode {
	case colorAuto, colorAlways, colorNever:
		return nil
	default:
		return fmt.Errorf("invalid --color value %q; must be one of %q, %q, %q",
			mode, colorAuto, colorAlways, colorNever)
	}
}

// resolveColor decides whether ANSI color output should be enabled.
// The NO_COLOR environment variable (any non-empty value) always forces
// color off regardless of mode, per https://no-color.org. In "auto" mode
// color follows stdoutIsTTY so piped or redirected output stays plain.
func resolveColor(mode string, stdoutIsTTY bool) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	switch mode {
	case colorAlways:
		return true
	case colorNever:
		return false
	default: // colorAuto
		return stdoutIsTTY
	}
}

// errPartialFailure is the sentinel returned by executeRun when at least one
// sync item failed. The caller maps it to exitPartialFailure.
var errPartialFailure = fmt.Errorf("one or more tasks failed")

// executeRun loads config, sets up the logger, optionally prompts for the
// rclone config password, then runs the full sync pipeline.
func executeRun(ctx context.Context, skip, only, remotes []string, colorMode string, opts engine.RunOptions) error {
	opts.SkipJobs = skip
	opts.OnlyJobs = only
	opts.SelectedRemotes = remotes

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if err := engine.ValidateJobNames(opts.SkipJobs, opts.OnlyJobs, cfg.JobNames()); err != nil {
		return err
	}

	if err := validateRemoteNames(opts.SelectedRemotes, cfg.AllRemoteNames()); err != nil {
		return err
	}

	configPath, err := config.ConfigPath()
	if err != nil {
		return err
	}

	logDir := logDirectory()
	stdoutIsTTY := term.IsTerminal(int(os.Stdout.Fd()))
	useColor := resolveColor(colorMode, stdoutIsTTY)
	interactive := stdoutIsTTY

	// Prune stale logs before opening a new one so the new file doesn't
	// count against the retention window. Failures here are operational
	// metadata issues, never a reason to block a backup run.
	pruneDeleted, pruneWarnings, pruneErr := log.PruneOldLogs(logDir, cfg.ResolvedLogRetentionDays(), time.Now())

	logger, logPath, err := log.New(logDir, useColor)
	if err != nil {
		return fmt.Errorf("setting up logging: %w", err)
	}
	defer logger.Close()

	logger.Header("Shuttle Started")
	if opts.DryRun {
		logger.Warn("DRY RUN: no files will be modified.")
	}
	if pruneErr != nil {
		logger.Warn(fmt.Sprintf("log rotation skipped: %v", pruneErr))
	}
	for _, w := range pruneWarnings {
		logger.Warn("log rotation: " + w)
	}
	if pruneDeleted > 0 {
		logger.Info(fmt.Sprintf("pruned %d old log file(s)", pruneDeleted))
	}

	promptForPassword(logger)

	pw := engine.NewProgressWriter(os.Stdout, interactive, useColor)
	runner := engine.NewRunner(cfg, configPath, logger, pw, opts.DryRun, logPath)
	summary, err := runner.Run(ctx, opts)
	if err != nil {
		return err
	}

	engine.RenderSummary(os.Stdout, summary, useColor)
	fmt.Printf("\nLog: %s\n", logPath)

	if summary.HasErrors() {
		return errPartialFailure
	}
	return nil
}

// validateRemoteNames returns an error when any selected remote is not present
// in the union of all rclone jobs' remote names. A nil or empty selection is always valid.
func validateRemoteNames(selected, configured []string) error {
	if len(selected) == 0 {
		return nil
	}
	valid := make(map[string]bool, len(configured))
	for _, r := range configured {
		valid[r] = true
	}
	for _, r := range selected {
		if !valid[r] {
			return fmt.Errorf("unknown remote %q; configured: %v", r, configured)
		}
	}
	return nil
}

// promptForPassword checks for RCLONE_CONFIG_PASS and prompts interactively
// when it is absent and stdin is a TTY. The password is set in the environment
// for rclone to pick up automatically.
func promptForPassword(logger *log.Logger) {
	if os.Getenv("RCLONE_CONFIG_PASS") != "" {
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		logger.Warn("RCLONE_CONFIG_PASS not set and stdin is not a terminal.")
		return
	}
	fmt.Print("Enter rclone config password (or press Enter if none): ")
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil || len(pass) == 0 {
		return
	}
	os.Setenv("RCLONE_CONFIG_PASS", string(pass)) //nolint:errcheck // setting env cannot fail in practice
}

// logDirectory returns the path for log files, respecting XDG_STATE_HOME.
// Falls back to ~/.local/state when the env var is not set.
func logDirectory() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "shuttle", "logs")
}
