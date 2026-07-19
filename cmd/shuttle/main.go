package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

// cliFlags holds the resolved CLI inputs for a single run. Collecting them in
// one struct keeps executeRun's signature below the project's 3-parameter
// threshold and makes it obvious which fields belong to the CLI boundary vs
// the engine's RunOptions.
type cliFlags struct {
	RunOpts    engine.RunOptions
	ColorMode  string
	Quiet      bool
	Verbose    bool
	ConfigPath string
}

type colorInputs struct {
	mode               string
	stdoutIsTerminal   bool
	isNoColorRequested bool
}

type runPreparation struct {
	plan             engine.RunPlan
	configPath       string
	logRetentionDays int
	verbosity        log.Verbosity
	colorMode        engine.TerminalColorMode
	interactive      bool
	dryRun           bool
}

type runSession struct {
	logger    *log.Logger
	logPath   string
	runner    *engine.Runner
	verbosity log.Verbosity
	colorMode engine.TerminalColorMode
}

type passwordPrompt struct {
	writer       io.Writer
	terminal     bool
	readPassword func() ([]byte, error)
}

// run is the real entry point, returning an exit code so main stays testable.
// Exit codes: 0 success, 1 partial task failure, 2 config/usage error, 130 signal.
func run() int {
	var cli cliFlags
	rootCmd := newRootCommand(&cli)

	// Context canceled on SIGINT/SIGTERM. The parent is Background and stop
	// runs only after the exit-code check below, so once ExecuteContext
	// returns, ctx.Err() != nil can mean exactly one thing: a signal arrived.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Companion channel for UX only: print the interrupt notice at receipt
	// time and restore the default disposition so a second signal terminates
	// the process immediately instead of being swallowed. Registration order
	// is load-bearing: NotifyContext above must register first so a signal
	// landing between the two calls still cancels (at worst the notice line
	// is skipped).
	interruptCh := make(chan os.Signal, 1)
	signal.Notify(interruptCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-interruptCh
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
		fmt.Fprintln(os.Stderr, "\nInterrupted. Shutting down...")
	}()

	return commandExitCode(ctx, rootCmd.ExecuteContext(ctx))
}

func newRootCommand(cli *cliFlags) *cobra.Command {
	rootCommand := &cobra.Command{
		Use:   "shuttle",
		Short: "Automated backup and synchronization tool",
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if err := validateColorMode(cli.ColorMode); err != nil {
				return err
			}
			if cli.Quiet && cli.Verbose {
				return errors.New("--quiet and --verbose are mutually exclusive")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			return executeRun(command.Context(), *cli)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	runCommand := newRunCommand(cli)
	registerRunFlags(rootCommand, cli)
	registerRunFlags(runCommand, cli)
	rootCommand.PersistentFlags().StringVarP(
		&cli.ConfigPath,
		"config",
		"c",
		"",
		"Path to config file (overrides $SHUTTLE_CONFIG and the default XDG location)",
	)
	rootCommand.AddCommand(
		runCommand,
		newVersionCommand(),
		newValidateCommand(cli),
		newDoctorCommand(cli),
	)
	return rootCommand
}

func newRunCommand(cli *cliFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Execute sync tasks (default when no subcommand given)",
		RunE: func(command *cobra.Command, _ []string) error {
			return executeRun(command.Context(), *cli)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(command *cobra.Command, _ []string) {
			output := command.OutOrStdout()
			_, _ = fmt.Fprintf(output, "shuttle %s\n", engine.SanitizeTerminalText(version))
			_, _ = fmt.Fprintf(output, "commit: %s\n", engine.SanitizeTerminalText(commit))
			_, _ = fmt.Fprintf(output, "built:  %s\n", engine.SanitizeTerminalText(date))
		},
	}
}

func newValidateCommand(cli *cliFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check configuration file for errors",
		RunE: func(command *cobra.Command, _ []string) error {
			path, _, err := resolveConfigPath(cli.ConfigPath)
			if err != nil {
				return err
			}
			if _, err := config.LoadFile(path); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(
				command.OutOrStdout(),
				"config ok: %s\n",
				engine.SanitizeTerminalText(path),
			)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}

func newDoctorCommand(cli *cliFlags) *cobra.Command {
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check environment and configuration readiness",
		RunE: func(command *cobra.Command, _ []string) error {
			path, explicit, err := resolveConfigPath(cli.ConfigPath)
			if err != nil {
				return err
			}
			cfg, loadErr := config.LoadFile(path)
			report := engine.Diagnose(command.Context(), engine.ConfigStatus{
				Path:     path,
				Cfg:      cfg,
				LoadErr:  loadErr,
				Explicit: explicit,
			})
			colorMode := resolveColor(colorInputs{
				mode:               cli.ColorMode,
				stdoutIsTerminal:   term.IsTerminal(int(os.Stdout.Fd())),
				isNoColorRequested: os.Getenv("NO_COLOR") != "",
			})
			engine.RenderReport(command.OutOrStdout(), report, colorMode)
			if report.HasFailures() {
				return errDoctorFailed
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.Flags().StringVar(
		&cli.ColorMode,
		"color",
		colorAuto,
		"Colorize terminal output: auto|always|never",
	)
	return command
}

func registerRunFlags(command *cobra.Command, cli *cliFlags) {
	flags := command.Flags()
	flags.BoolVarP(
		&cli.RunOpts.DryRun,
		"dry-run",
		"n",
		false,
		"Preview changes without modifying files",
	)
	flags.StringArrayVar(
		&cli.RunOpts.SkipJobs,
		"skip",
		nil,
		"Skip a job by name (repeatable; mutually exclusive with --only)",
	)
	flags.StringArrayVar(
		&cli.RunOpts.OnlyJobs,
		"only",
		nil,
		"Run only named jobs (repeatable; mutually exclusive with --skip)",
	)
	flags.StringArrayVar(
		&cli.RunOpts.SelectedRemotes,
		"remote",
		nil,
		"Target specific cloud remote by name (repeatable)",
	)
	flags.StringVar(
		&cli.ColorMode,
		"color",
		colorAuto,
		"Colorize terminal output: auto|always|never",
	)
	flags.BoolVarP(
		&cli.Quiet,
		"quiet",
		"q",
		false,
		"Suppress terminal output on success (mutually exclusive with --verbose)",
	)
	flags.BoolVarP(
		&cli.Verbose,
		"verbose",
		"v",
		false,
		"Show executed commands and extra diagnostics (mutually exclusive with --quiet)",
	)
}

func commandExitCode(ctx context.Context, err error) int {
	switch {
	case ctx.Err() != nil:
		return exitSignal
	case errors.Is(err, errPartialFailure):
		return exitPartialFailure
	case errors.Is(err, errDoctorFailed):
		return exitUsageError
	case err != nil:
		fmt.Fprintf(os.Stderr, "Error: %s\n", engine.SanitizeTerminalText(err.Error()))
		return exitUsageError
	default:
		return exitSuccess
	}
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

const (
	// envConfigPath names the environment variable used to supply an alternate config path.
	envConfigPath = "SHUTTLE_CONFIG"
	// envRcloneConfigPass names rclone's native direct password environment.
	envRcloneConfigPass = "RCLONE_CONFIG_PASS"
	// envRclonePasswordCommand names rclone's native external password provider.
	envRclonePasswordCommand = "RCLONE_PASSWORD_COMMAND"
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

// resolveVerbosity collapses the two boolean CLI flags into the Logger's
// Verbosity enum. The caller has already ensured quiet and verbose are not
// both set (via PersistentPreRunE).
func resolveVerbosity(quiet, verbose bool) log.Verbosity {
	switch {
	case quiet:
		return log.VerbosityQuiet
	case verbose:
		return log.VerbosityVerbose
	default:
		return log.VerbosityNormal
	}
}

// resolveConfigPath picks the config path to use and reports whether the
// caller supplied one explicitly (via --config or $SHUTTLE_CONFIG). An
// explicit path is asserted-to-exist by the caller; the default XDG path is
// tolerated as absent. Tildes are expanded and paths are made absolute so the
// lock-file hash is stable regardless of the caller's working directory.
func resolveConfigPath(flagValue string) (path string, explicit bool, err error) {
	raw := flagValue
	if raw == "" {
		raw = os.Getenv(envConfigPath)
	}
	if raw == "" {
		p, pathErr := config.ConfigPath()
		if pathErr != nil {
			return "", false, fmt.Errorf("resolving default config path: %w", pathErr)
		}
		absolutePath, absoluteError := filepath.Abs(p)
		if absoluteError != nil {
			return "", false, fmt.Errorf("resolving default config path %q: %w", p, absoluteError)
		}
		return absolutePath, false, nil
	}
	expanded, expErr := expandHome(raw)
	if expErr != nil {
		return "", true, expErr
	}
	abs, absErr := filepath.Abs(expanded)
	if absErr != nil {
		return "", true, fmt.Errorf("resolving config path %q: %w", raw, absErr)
	}
	return abs, true, nil
}

// expandHome replaces a leading "~" with the user's home directory. Paths
// that do not start with "~" are returned unchanged. Kept local to main.go
// because tilde expansion of CLI inputs is a shell/boundary concern; the
// equivalent helper inside internal/config handles tildes in *config values*,
// which is a separate use case.
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory for path %q: %w", path, err)
	}
	return filepath.Join(home, path[1:]), nil
}

// resolveColor decides whether ANSI color output should be enabled based on
// the --color mode, whether stdout is a TTY, and whether the NO_COLOR
// environment variable is set. NO_COLOR forces color off regardless of mode
// per https://no-color.org. Kept pure (no env access) so it is trivially
// testable; the caller reads NO_COLOR at the boundary.
func resolveColor(inputs colorInputs) engine.TerminalColorMode {
	if inputs.isNoColorRequested {
		return engine.TerminalColorDisabled
	}
	switch inputs.mode {
	case colorAlways:
		return engine.TerminalColorEnabled
	case colorNever:
		return engine.TerminalColorDisabled
	default: // colorAuto
		if inputs.stdoutIsTerminal {
			return engine.TerminalColorEnabled
		}
		return engine.TerminalColorDisabled
	}
}

// errPartialFailure is the sentinel returned by executeRun when at least one
// sync item failed. The caller maps it to exitPartialFailure.
var errPartialFailure = errors.New("one or more tasks failed")

// errDoctorFailed is returned by doctorCmd when at least one diagnostic check
// failed. run() maps it to exit 2 silently (the report already lists failures).
var errDoctorFailed = errors.New("doctor found problems")

// executeRun advances validated selection through focused CLI-owned boundary stages.
func executeRun(ctx context.Context, cli cliFlags) error {
	preparation, err := prepareRun(cli)
	if err != nil {
		return err
	}
	session, err := openRunSession(preparation)
	if err != nil {
		return err
	}
	defer session.logger.Close()
	summary, err := executePlan(ctx, session)
	if err != nil {
		return err
	}
	return renderRunResult(session, summary)
}

func prepareRun(cli cliFlags) (runPreparation, error) {
	configPath, explicit, err := resolveConfigPath(cli.ConfigPath)
	if err != nil {
		return runPreparation{}, err
	}

	var cfg *config.Config
	if explicit {
		cfg, err = config.LoadFile(configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return runPreparation{}, fmt.Errorf("loading config: %w", err)
	}

	if err := engine.ValidateJobNames(
		cli.RunOpts.SkipJobs,
		cli.RunOpts.OnlyJobs,
		cfg.JobNames(),
	); err != nil {
		return runPreparation{}, err
	}
	if err := validateRemoteNames(cli.RunOpts.SelectedRemotes, cfg.AllRemoteNames()); err != nil {
		return runPreparation{}, err
	}
	plan, err := engine.BuildRunPlan(cfg, cli.RunOpts)
	if err != nil {
		return runPreparation{}, err
	}
	stdoutIsTerminal := term.IsTerminal(int(os.Stdout.Fd()))
	return runPreparation{
		plan:             plan,
		configPath:       configPath,
		logRetentionDays: cfg.ResolvedLogRetentionDays(),
		verbosity:        resolveVerbosity(cli.Quiet, cli.Verbose),
		colorMode: resolveColor(colorInputs{
			mode:               cli.ColorMode,
			stdoutIsTerminal:   stdoutIsTerminal,
			isNoColorRequested: os.Getenv("NO_COLOR") != "",
		}),
		interactive: stdoutIsTerminal,
		dryRun:      cli.RunOpts.DryRun,
	}, nil
}

func openRunSession(preparation runPreparation) (runSession, error) {
	logger, logPath, err := openRunLogger(preparation)
	if err != nil {
		return runSession{}, err
	}

	rclonePassword := ""
	if preparation.plan.RequiresRclone() {
		rclonePassword, err = resolveRclonePassword(logger, passwordPrompt{
			writer:   os.Stdout,
			terminal: term.IsTerminal(int(os.Stdin.Fd())),
			readPassword: func() ([]byte, error) {
				return term.ReadPassword(int(os.Stdin.Fd()))
			},
		})
		if err != nil {
			logger.Close()
			return runSession{}, err
		}
	}
	progressOut := io.Writer(os.Stdout)
	progressInteractive := preparation.interactive
	if preparation.verbosity == log.VerbosityQuiet {
		progressOut = io.Discard
		progressInteractive = false
	}
	progressMode := engine.ProgressNonInteractive
	if progressInteractive {
		progressMode = engine.ProgressInteractive
	}

	runner, err := engine.NewRunner(engine.RunnerConfig{
		Plan:       preparation.plan,
		ConfigPath: preparation.configPath,
		Logger:     logger,
		Progress: engine.NewProgressWriter(
			progressOut,
			engine.ProgressOptions{
				Mode:  progressMode,
				Color: preparation.colorMode,
			},
		),
		Prerequisites: engine.NewSystemPrerequisiteChecker(),
		Locker:        engine.NewFileRunLocker(),
		Rsync:         engine.NewRsyncExecutor(logger),
		Rclone:        engine.NewRcloneExecutor(logger, rclonePassword),
	})
	if err != nil {
		logger.Close()
		return runSession{}, fmt.Errorf("creating runner: %w", err)
	}
	return runSession{
		logger:    logger,
		logPath:   logPath,
		runner:    runner,
		verbosity: preparation.verbosity,
		colorMode: preparation.colorMode,
	}, nil
}

func openRunLogger(preparation runPreparation) (*log.Logger, string, error) {
	logDir, err := logDirectory()
	if err != nil {
		return nil, "", err
	}
	pruneDeleted, pruneWarnings, pruneErr := log.PruneOldLogs(
		logDir,
		preparation.logRetentionDays,
		time.Now(),
	)
	logger, logPath, err := log.New(
		logDir,
		log.Options{
			UseColor:  preparation.colorMode == engine.TerminalColorEnabled,
			Verbosity: preparation.verbosity,
		},
	)
	if err != nil {
		return nil, "", fmt.Errorf("setting up logging: %w", err)
	}

	logger.Header("Shuttle Started")
	if preparation.dryRun {
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
	return logger, logPath, nil
}

func executePlan(ctx context.Context, session runSession) (engine.Summary, error) {
	return session.runner.Run(ctx)
}

func renderRunResult(session runSession, summary engine.Summary) error {
	var output io.Writer
	if session.verbosity == log.VerbosityQuiet {
		if summary.HasErrors() {
			output = os.Stderr
		}
	} else {
		output = os.Stdout
	}
	if output != nil {
		engine.RenderSummary(output, summary, session.colorMode)
		_, _ = fmt.Fprintf(
			output,
			"\nLog: %s\n",
			engine.SanitizeTerminalText(session.logPath),
		)
	}
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

// resolveRclonePassword returns only prompted credentials for child-only injection.
func resolveRclonePassword(logger *log.Logger, prompt passwordPrompt) (string, error) {
	if os.Getenv(envRcloneConfigPass) != "" ||
		os.Getenv(envRclonePasswordCommand) != "" {
		return "", nil
	}
	if !prompt.terminal {
		logger.Warn(
			envRcloneConfigPass + " and " + envRclonePasswordCommand +
				" are not set and stdin is not a terminal.",
		)
		return "", nil
	}
	if _, err := fmt.Fprint(
		prompt.writer,
		"Enter rclone config password (or press Enter if none): ",
	); err != nil {
		return "", fmt.Errorf("writing rclone config password prompt: %w", err)
	}
	password, readErr := prompt.readPassword()
	_, writeErr := fmt.Fprintln(prompt.writer)
	if readErr != nil {
		return "", fmt.Errorf("reading rclone config password: %w", readErr)
	}
	if writeErr != nil {
		return "", fmt.Errorf("writing rclone config password prompt newline: %w", writeErr)
	}
	return string(password), nil
}

// logDirectory returns an absolute log path while preserving home lookup failures.
func logDirectory() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory for logs: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	logDir := filepath.Join(dir, "shuttle", "logs")
	absolutePath, err := filepath.Abs(logDir)
	if err != nil {
		return "", fmt.Errorf("resolving log directory %q: %w", logDir, err)
	}
	return absolutePath, nil
}
