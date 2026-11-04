// Package app translates CLI options into plans and reports execution results.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/buttercrab/up/internal/demo"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/ui"
	"github.com/buttercrab/up/internal/update"
	"github.com/buttercrab/up/internal/updaters"
)

// Run returns a shell exit code: 0 success/preview, 1 execution failure,
// 2 invalid options, 130 cancellation. It does not exit the caller's process.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jobs := flags.Int("jobs", 4, "maximum concurrent updaters")
	dryRun := flags.Bool("dry-run", false, "preview commands without executing them")
	yes := flags.Bool("yes", false, "execute the displayed plan without prompting")
	plain := flags.Bool("plain", false, "use plain output instead of the interactive dashboard")
	only := flags.String("only", "", "comma-separated updaters to run: brew,rustup,npm")
	exclude := flags.String("exclude", "", "comma-separated updaters to omit")
	demoMode := flags.Bool("demo", false, "simulate three concurrent tasks without changing installed tools")
	showVersion := flags.Bool("version", false, "print version")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: up [--dry-run | --yes] [--plain] [--jobs N] [--only NAMES | --exclude NAMES]\n\nParallel upgrades for installed Homebrew, rustup, and npm. Use --demo for a harmless simulation.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *jobs < 1 {
		fmt.Fprintln(stderr, "up: expected no positional arguments and --jobs >= 1")
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	var selection updaters.Selection
	var err error
	if *demoMode {
		if *only != "" || *exclude != "" {
			fmt.Fprintln(stderr, "up: --demo cannot be combined with --only or --exclude")
			return 2
		}
		selection.Plans = demo.Plans()
	} else {
		selection, err = updaters.Discover(ctx, *only, *exclude)
	}
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		if ctx.Err() != nil {
			return 130
		}
		if errors.Is(err, updaters.ErrSelection) {
			return 2
		}
		return 1
	}
	if len(selection.Plans) == 0 {
		printPlan(stdout, selection)
		fmt.Fprintln(stdout, "No supported updaters are installed or selected.")
		return 0
	}
	interactive := !*plain && isTerminal(stdout) && term.IsTerminal(int(os.Stdin.Fd()))
	if *dryRun || !interactive {
		if *demoMode {
			fmt.Fprintln(stdout, "Demo simulation: no installed tools will be changed.")
		}
		printPlan(stdout, selection)
		if *dryRun || !*yes {
			fmt.Fprintln(stdout, "Preview only. Use --yes to execute.")
			return 0
		}
	}
	cache, err := os.UserCacheDir()
	if err == nil {
		cache = filepath.Join(cache, "up", "runs")
		err = os.MkdirAll(cache, 0o700)
	}
	var logs string
	if err == nil {
		logs, err = os.MkdirTemp(cache, "run-")
	}
	if err != nil {
		fmt.Fprintf(stderr, "up: create logs: %v\n", err)
		return 1
	}
	engine := update.Engine{Jobs: *jobs, LogDir: logs}
	if *demoMode {
		engine.Executor = demo.Executor{}
	}
	var results []update.Result
	if interactive {
		manager := &process.TerminalManager{}
		if *demoMode {
			manager = nil
		} else {
			engine.Executor = manager
		}
		results, err = ui.Show(ctx, os.Stdin, stdout, ui.Config{
			Plans: selection.Plans, Skipped: selection.Skipped, Demo: *demoMode, Engine: engine, Terminal: manager, AutoStart: *yes,
		})
	} else {
		if engine.Executor == nil {
			engine.Executor = process.Runner{}
		}
		engine.Executor = plainExecutor{executor: engine.Executor, output: &lockedWriter{writer: stdout}}
		var run *update.Run
		fmt.Fprintf(stdout, "Running; full logs: %s\n", logs)
		run, err = engine.Start(ctx, selection.Plans)
		if err == nil {
			results = run.Wait()
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	code := 0
	for _, result := range results {
		if result.Status == update.Queued {
			continue
		}
		fmt.Fprintf(stdout, "%s: %s\n", result.ID, result.Status)
		if result.Err != nil {
			fmt.Fprintf(stderr, "%s: %s\n", result.ID, report.Failure(result))
			if hint := report.Hint(result); hint != "" {
				fmt.Fprintln(stderr, hint)
			}
			fmt.Fprintf(stderr, "Full log: %s\n", result.LogPath)
			if code == 0 {
				code = 1
			}
		}
		if result.Status == update.Cancelled {
			code = 130
		}
	}
	if ctx.Err() != nil {
		return 130
	}
	return code
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func printPlan(w io.Writer, selection updaters.Selection) {
	fmt.Fprintln(w, "Plan:")
	for _, plan := range selection.Plans {
		for _, cmd := range plan.Commands {
			parts := []string{strconv.Quote(cmd.Path)}
			for _, arg := range cmd.Args {
				parts = append(parts, strconv.Quote(arg))
			}
			fmt.Fprintf(w, "  %s: %s\n", plan.ID, strings.Join(parts, " "))
		}
	}
	if len(selection.Skipped) > 0 {
		fmt.Fprintf(w, "Skipped (not installed): %s\n", strings.Join(selection.Skipped, ", "))
	}
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

type plainExecutor struct {
	executor process.Executor
	output   io.Writer
}

func (e plainExecutor) Run(ctx context.Context, c process.Command, log io.Writer) error {
	return e.executor.Run(ctx, c, io.MultiWriter(log, e.output))
}
