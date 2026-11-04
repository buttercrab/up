//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCancellationCleanupBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "unused-fixture")
	cleanup, err := configureCancellation(ctx, cmd, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	cleanup(false)
	cleanup(true)
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("cancel before start: %v", err)
	}
}
