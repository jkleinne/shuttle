package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jkleinne/shuttle/internal/config"
	"github.com/jkleinne/shuttle/internal/engine"
	"github.com/jkleinne/shuttle/internal/log"
)

// version is set at build time via -ldflags; defaults to "dev" for local builds.
var version = "dev"

func main() {
	os.Exit(run())
}

// run is the real entry point, returning an exit code so main stays testable.
// Exit codes: 0 success, 1 partial task failure, 2 config/usage error, 130 signal.
func run() int {
	var runOpts engine.RunOptions
	var skipJobs, onlyJobs, selectedRemotes []string

	rootCmd := &cobra.Command{
		Use:   "shuttle",
		Short: "Automated backup and synchronization tool",
		// No subcommand: delegate to executeRun so `shuttle --dry-run` works.
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRun(cmd.Context(), skipJobs, onlyJobs, selectedRemotes, runOpts, args)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Execute sync tasks (default when no subcommand given)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRun(cmd.Context(), skipJobs, onlyJobs, selectedRemotes, runOpts, args)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("shuttle %s\n", version)
		},
	}

	// Register the same flags on both root and run so both invocation styles
	// (`shuttle --dry-run` and `shuttle run --dry-run`) accept them.
	for _, cmd := range []*cobra.Command{rootCmd, runCmd} {
		cmd.Flags().BoolVarP(&runOpts.DryRun, "dry-run", "n", false, "Preview changes without modifying files")
		cmd.Flags().StringArrayVar(&skipJobs, "skip", nil, "Skip a job by name (repeatable; mutually exclusive with --only)")
		cmd.Flags().StringArrayVar(&onlyJobs, "only", nil, "Run only named jobs (repeatable; mutually exclusive with --skip)")
		cmd.Flags().StringArrayVar(&selectedRemotes, "remote", nil, "Target specific cloud remote by name (repeatable)")
	}

	rootCmd.AddCommand(runCmd, versionCmd)

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
		return 130
	}
	if errors.Is(err, errPartialFailure) {
		return 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 2
	}
	return 0
}

// errPartialFailure is the sentinel returned by executeRun when at least one
// sync item failed. The caller maps it to exit code 1.
var errPartialFailure = fmt.Errorf("one or more tasks failed")

// executeRun loads config, sets up the logger, optionally prompts for the
// rclone config password, then runs the full sync pipeline.
func executeRun(ctx context.Context, skip, only, remotes []string, opts engine.RunOptions, trailingArgs []string) error {
	opts.SkipJobs = skip
	opts.OnlyJobs = only
	opts.SelectedRemotes = remotes
	opts.RcloneOverrides = trailingArgs

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if err := engine.ValidateJobNames(opts.SkipJobs, opts.OnlyJobs, cfg.JobNames()); err != nil {
		return err
	}

	if err := validateRemoteNames(opts.SelectedRemotes, cfg.RemoteNames()); err != nil {
		return err
	}

	logDir := logDirectory()
	useColor := term.IsTerminal(int(os.Stdout.Fd()))
	logger, logPath, err := log.New(logDir, useColor)
	if err != nil {
		return fmt.Errorf("setting up logging: %w", err)
	}
	defer logger.Close()

	logger.Header("Shuttle Started")
	if opts.DryRun {
		logger.Warn("DRY RUN: no files will be modified.")
	}

	promptForPassword(logger)

	runner := engine.NewRunner(cfg, logger, opts.DryRun, logPath)
	summary, err := runner.Run(ctx, opts)
	if err != nil {
		return err
	}

	engine.RenderSummary(os.Stdout, summary)
	fmt.Printf("\nLog: %s\n", logPath)

	if summary.HasErrors() {
		return errPartialFailure
	}
	return nil
}

// validateRemoteNames returns an error when any selected remote is not present
// in the configured remote list. A nil or empty selection is always valid.
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
