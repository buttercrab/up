// Package process executes commands without a shell and owns their lifecycle.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
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
// Output writers must return from Write; a caller-owned writer that blocks
// indefinitely cannot be interrupted by context cancellation.
type Runner struct {
	GracePeriod time.Duration
	// DiscardStderr keeps metadata probes stdout-only without creating another
	// pipe whose descendants could keep the task alive.
	DiscardStderr bool
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
	cmd.WaitDelay = grace + time.Second
	cleanup, err := configureCancellation(ctx, cmd, grace)
	if err != nil {
		return err
	}
	// Own the pipe drain: os/exec's WaitDelay hides a pipe timeout behind a
	// nonzero parent exit, so its return value cannot own descendant cleanup.
	pipe, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	reader := &drainReader{pipe: pipe}
	defer reader.Close()
	defer writer.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	if r.DiscardStderr {
		discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer discard.Close()
		cmd.Stderr = discard
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", command.Path, err)
	}
	_ = writer.Close()
	defer cleanup(false)
	stopCancellation := watchCancellation(ctx, cmd)
	defer stopCancellation()
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(output, reader)
		copyDone <- err
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var copyErr error
	select {
	case copyErr = <-copyDone:
		if copyErr != nil {
			cleanup(true)
		}
		err = <-waitDone
	case err = <-waitDone:
		cleanup(false)
		timer := time.NewTimer(grace + time.Second)
		defer timer.Stop()
		select {
		case copyErr = <-copyDone:
			if copyErr != nil {
				cleanup(true)
			}
		case <-timer.C:
			cleanup(true)
			_ = reader.Close()
			copyErr = <-copyDone
			err = errors.Join(err, exec.ErrWaitDelay)
		}
	}
	err = errors.Join(err, copyErr)
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", command.Path, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("%s: %w", command.Path, err)
	}
	return nil
}

// Only an intentional close of our read end is normal EOF. Errors returned by
// an output writer, including os.ErrClosed, remain visible to the caller.
type drainReader struct {
	pipe    *os.File
	closing atomic.Bool
}

func (r *drainReader) Read(p []byte) (int, error) {
	n, err := r.pipe.Read(p)
	if r.closing.Load() && errors.Is(err, os.ErrClosed) {
		err = io.EOF
	}
	return n, err
}

func (r *drainReader) Close() error {
	r.closing.Store(true)
	return r.pipe.Close()
}

// os/exec stops observing the context once the leader exits. Keep group
// cancellation active while descendants can still be holding output pipes.
func watchCancellation(ctx context.Context, cmd *exec.Cmd) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = cmd.Cancel()
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}
