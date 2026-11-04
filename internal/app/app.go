// Package app translates CLI options into plans and reports execution results.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/buttercrab/up/internal/demo"
	"github.com/buttercrab/up/internal/inventory"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/ui"
	"github.com/buttercrab/up/internal/update"
	"github.com/buttercrab/up/internal/updaters"
)

// Run returns a shell exit code: 0 success/preview, 1 execution failure,
// 2 invalid options, 130 cancellation. It does not exit the caller's process.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && args[0] == "inventory" {
		return runInventory(ctx, args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jobs := flags.Int("jobs", 4, "maximum concurrent updaters")
	dryRun := flags.Bool("dry-run", false, "preview commands without executing them")
	yes := flags.Bool("yes", false, "execute the displayed plan without prompting")
	plain := flags.Bool("plain", false, "use plain output instead of the interactive dashboard")
	supportedIDs := strings.Join(inventory.SupportedIDs(), ",")
	only := flags.String("only", "", "comma-separated updaters to run: "+supportedIDs)
	exclude := flags.String("exclude", "", "comma-separated updaters to omit")
	demoMode := flags.Bool("demo", false, "simulate three concurrent tasks without changing installed tools")
	showVersion := flags.Bool("version", false, "print version")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: up [options]\n\nOpen one arrow-key application: Upgrades, Inventory, and Demo.\nUse ↑/↓ and Enter to open features; Esc goes back; q quits.\nSupported upgrade adapters: %s.\n\nAutomation: --plain --yes runs upgrades, --dry-run previews commands,\nand up inventory --json exports read-only metadata.\n", supportedIDs)
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
	interactive := !*plain && isTerminal(stdout) && term.IsTerminal(int(os.Stdin.Fd()))
	if interactive && !*dryRun && !*yes && !*demoMode && *only == "" && *exclude == "" {
		return runShell(ctx, stdout, stderr, shellOptions{jobs: *jobs, only: *only, exclude: *exclude}, false)
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
	cfg := newRunConfig(selection, *jobs, *demoMode, interactive)
	cfg.AutoStart = *yes
	var results []update.Result
	if interactive {
		results, err = ui.Show(ctx, os.Stdin, stdout, cfg)
	} else {
		var engine update.Engine
		engine, err = cfg.PrepareRun(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			if ctx.Err() != nil {
				return 130
			}
			return 1
		}
		if engine.Executor == nil {
			engine.Executor = process.Runner{}
		}
		engine.Executor = plainExecutor{executor: engine.Executor, output: &lockedWriter{writer: stdout}}
		var run *update.Run
		fmt.Fprintf(stdout, "Running; full logs: %s\n", engine.LogDir)
		run, err = engine.Start(ctx, selection.Plans)
		if err == nil {
			results = run.Wait()
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 130
		}
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
