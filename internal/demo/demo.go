// Package demo simulates concurrent updates without invoking any tools.
package demo

import (
	"context"
	"fmt"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
	"io"
	"time"
)

func Plans() []update.Plan {
	var plans []update.Plan
	for _, id := range []string{"brew", "rustup", "npm"} {
		plans = append(plans, update.Plan{ID: id, Commands: []process.Command{{Path: "simulate", Args: []string{id}}}})
	}
	return plans
}

type Executor struct{}

func (Executor) Run(ctx context.Context, c process.Command, w io.Writer) error {
	delay := map[string]time.Duration{"brew": 500 * time.Millisecond, "rustup": 650 * time.Millisecond, "npm": 350 * time.Millisecond}[c.TaskID]
	for step, stage := range []string{"Checking installed versions", "Fetching package metadata", "Downloading updates", "Verifying downloads", "Installing updates", "Finished"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "[demo · %s] %s\n", c.TaskID, stage); err != nil {
			return err
		}
		if err := progressFrame(w, c.TaskID, step); err != nil {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return nil
}

// Representative upstream formats exercise the same parsers as real runs.
func progressFrame(w io.Writer, id string, step int) error {
	var text string
	switch id {
	case "brew":
		if step < 4 {
			text = fmt.Sprintf("⠋ Bottle demo-llvm (1.0) Downloading %.1fMB/100.0MB\n⠋ Cask demo-editor (1.0) Downloading %.1fMB/80.0MB\n", float64(step+1)*25, float64(step+1)*20)
		} else {
			text = "==> Installing demo-llvm\n==> Installing demo-editor\n"
		}
	case "rustup":
		if step < 4 {
			text = fmt.Sprintf("demo-rustc downloading [### ] %.1f MiB / 40.0 MiB\ndemo-cargo downloading [##  ] %.1f MiB (2 MiB/s, ETA: 1s)\n", float64(step+1)*10, float64(step+1)*3)
		} else {
			text = "demo-rustc installing\ndemo-cargo installing\n"
		}
	case "npm":
		if step == 4 {
			text = "npm info run @demo/tool@1.0 postinstall node_modules/@demo/tool node install.js\n"
		}
		if step == 5 {
			text = "npm info run @demo/tool@1.0 postinstall { code: 0, signal: null }\n"
		}
	}
	_, err := io.WriteString(w, text)
	return err
}
