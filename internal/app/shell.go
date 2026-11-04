package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/buttercrab/up/internal/demo"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/ui"
	"github.com/buttercrab/up/internal/update"
	"github.com/buttercrab/up/internal/updaters"
)

type shellOptions struct {
	jobs          int
	only, exclude string
}

func runShell(ctx context.Context, stdout, stderr io.Writer, options shellOptions, startInventory bool) int {
	shell := &ui.Shell{
		StartInventory: startInventory,
		LoadUpgrades: func(ctx context.Context) (ui.Config, error) {
			selection, err := updaters.Discover(ctx, options.only, options.exclude)
			if err != nil {
				return ui.Config{}, err
			}
			return newRunConfig(selection, options.jobs, false, true), nil
		},
		LoadDemo: func(ctx context.Context) (ui.Config, error) {
			if err := ctx.Err(); err != nil {
				return ui.Config{}, err
			}
			return newRunConfig(updaters.Selection{Plans: demo.Plans()}, options.jobs, true, true), nil
		},
		LoadInventory: updaters.Inventory,
	}
	results, err := ui.Show(ctx, os.Stdin, stdout, ui.Config{Shell: shell})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 130
		}
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	if ctx.Err() != nil {
		return 130
	}
	code := 0
	for _, result := range results {
		if result.Status == update.Failed && code == 0 {
			code = 1
		}
		if result.Status == update.Cancelled {
			code = 130
		}
	}
	return code
}

// Every screen uses the same run setup. Browsing and reviewing create neither
// log directories nor update runs; PrepareRun is invoked only after approval.
func newRunConfig(selection updaters.Selection, jobs int, isDemo, interactive bool) ui.Config {
	engine := update.Engine{Jobs: jobs}
	var terminal *process.TerminalManager
	if isDemo {
		engine.Executor = demo.Executor{}
	} else if interactive {
		terminal = &process.TerminalManager{}
		engine.Executor = terminal
	}
	return ui.Config{
		Plans: selection.Plans, Skipped: selection.Skipped, Demo: isDemo, Engine: engine, Terminal: terminal,
		PrepareRun: func(ctx context.Context) (update.Engine, error) {
			if err := ctx.Err(); err != nil {
				return update.Engine{}, err
			}
			cache, err := os.UserCacheDir()
			if err != nil {
				return update.Engine{}, fmt.Errorf("find logs directory: %w", err)
			}
			root := filepath.Join(cache, "up", "runs")
			if err := os.MkdirAll(root, 0o700); err != nil {
				return update.Engine{}, fmt.Errorf("create logs directory: %w", err)
			}
			logs, err := os.MkdirTemp(root, "run-")
			if err != nil {
				return update.Engine{}, fmt.Errorf("create run logs: %w", err)
			}
			prepared := engine
			prepared.LogDir = logs
			return prepared, nil
		},
	}
}
