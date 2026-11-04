//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

func configureCancellation(ctx context.Context, cmd *exec.Cmd, grace time.Duration) (func(bool), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var mu sync.Mutex
	var timer *time.Timer
	cancelled := false
	closed := false
	var deadline time.Time
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if closed {
			return os.ErrProcessDone
		}
		if cancelled {
			return nil
		}
		cancelled = true
		deadline = time.Now().Add(grace)
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer = time.AfterFunc(grace, func() {
			mu.Lock()
			defer mu.Unlock()
			if !closed {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				closed = true
			}
		})
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return func(force bool) {
		if cmd.Process == nil {
			return
		}
		if ctx.Err() != nil {
			_ = cmd.Cancel()
		}
		mu.Lock()
		if closed || (!cancelled && !force) {
			mu.Unlock()
			return
		}
		// A parent exiting is not evidence that its descendants have finished
		// handling TERM. Hold the task's resources until their grace expires.
		remaining := time.Until(deadline)
		wait := cancelled && remaining > 0
		mu.Unlock()
		if wait {
			time.Sleep(remaining)
		}
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		if timer != nil {
			timer.Stop()
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		closed = true
	}, nil
}
