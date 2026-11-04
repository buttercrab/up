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
	for _, stage := range []string{"Checking installed versions", "Fetching package metadata", "Downloading updates", "Verifying downloads", "Installing updates", "Finished"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "[demo · %s] %s\n", c.TaskID, stage); err != nil {
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
