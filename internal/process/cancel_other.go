//go:build !darwin && !linux

package process

import (
	"errors"
	"os/exec"
	"time"
)

func configureCancellation(_ *exec.Cmd, _ time.Duration) (func(bool), error) {
	return nil, errors.New("process execution is supported on macOS and Linux")
}
