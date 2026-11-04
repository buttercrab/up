// Package process executes commands without a shell and owns their lifecycle.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Command is a single process. Env entries override inherited environment
// variables. Runner is non-interactive; TerminalManager supports terminal input.
type Command struct {
	Path   string
	Args   []string
	Dir    string
	Env    []string
	TaskID string // assigned by the scheduler, used to identify terminal sessions
}

// Executor permits testing the scheduler without executing installed tools.
// Implementations must support concurrent calls and honor cancellation.
type Executor interface {
	Run(context.Context, Command, io.Writer) error
}

// Runner sends TERM on cancellation, then KILL after GracePeriod. The zero
// value uses a two-second grace period. Supported hosts are macOS and Linux.
type Runner struct {
	GracePeriod time.Duration
}

func (r Runner) Run(ctx context.Context, command Command, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if command.Path == "" {
		return errors.New("command path is empty")
	}
	grace := r.GracePeriod
	if grace <= 0 {
		grace = 2 * time.Second
	}
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = append(os.Environ(), command.Env...)
	if output == nil {
		output = io.Discard
	}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = grace + time.Second
	cleanup, err := configureCancellation(cmd, grace)
	if err != nil {
		return err
	}
	// WaitDelay can expire after a successful parent leaves descendants holding
	// output pipes. Clean that group up as well as explicitly cancelled groups.
	defer func() { cleanup(errors.Is(err, exec.ErrWaitDelay)) }()
	err = cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", command.Path, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("%s: %w", command.Path, err)
	}
	return nil
}
