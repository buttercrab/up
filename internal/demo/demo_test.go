package demo

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestDemoCancellationDoesNotExecuteTools(t *testing.T) {
	plans := Plans()
	if len(plans) != 3 {
		t.Fatal("expected three demo tasks")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, p := range plans {
		c := p.Commands[0]
		if c.Path != "simulate" {
			t.Fatal("demo contains a real command")
		}
		c.TaskID = p.ID
		if err := (Executor{}).Run(ctx, c, io.Discard); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	}
}
