//go:build darwin || linux

package process

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

func configureCancellation(cmd *exec.Cmd, grace time.Duration) (func(bool), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var mu sync.Mutex
	var timer *time.Timer
	cancelled := false
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		cancelled = true
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer = time.AfterFunc(grace, func() {
			mu.Lock()
			defer mu.Unlock()
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		})
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return func(force bool) {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		// The parent can exit before its descendants. Reap the rest of the
		// process group even if no descendant holds an output pipe open.
		if cancelled || force {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}, nil
}
