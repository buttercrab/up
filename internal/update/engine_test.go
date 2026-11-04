package update_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
)

type executorFunc func(context.Context, process.Command, io.Writer) error

func (f executorFunc) Run(ctx context.Context, cmd process.Command, w io.Writer) error {
	return f(ctx, cmd, w)
}

func plan(id string, resources ...string) update.Plan {
	return update.Plan{ID: id, Resources: resources, Commands: []process.Command{{Path: id}}}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for execution")
		var zero T
		return zero
	}
}

func TestParallelismAndResourceSerialization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 4)
	release := map[string]chan struct{}{}
	for _, id := range []string{"a", "b", "c", "d"} {
		release[id] = make(chan struct{})
	}
	var mu sync.Mutex
	active, maxActive := 0, 0
	resourceActive := false
	engine := update.Engine{Jobs: 2, Executor: executorFunc(func(ctx context.Context, c process.Command, _ io.Writer) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		shared := c.Path == "a" || c.Path == "b"
		if shared && resourceActive {
			t.Error("tasks sharing a resource overlapped")
		}
		if shared {
			resourceActive = true
		}
		mu.Unlock()
		started <- c.Path
		select {
		case <-release[c.Path]:
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		if shared {
			resourceActive = false
		}
		mu.Unlock()
		return ctx.Err()
	})}
	run, err := engine.Start(ctx, []update.Plan{plan("a", "brew"), plan("b", "brew"), plan("c"), plan("d")})
	if err != nil {
		t.Fatal(err)
	}
	first, second := receive(t, started), receive(t, started)
	if !(first == "a" && second == "c" || first == "c" && second == "a") {
		t.Fatalf("expected independent a/c to start, got %s/%s", first, second)
	}
	// The second resource contender stays queued while c's freed slot lets d run.
	close(release["c"])
	if got := receive(t, started); got != "d" {
		t.Fatalf("expected d to bypass blocked b, got %s", got)
	}
	close(release["a"])
	if got := receive(t, started); got != "b" {
		t.Fatalf("expected b after a releases brew, got %s", got)
	}
	close(release["b"])
	close(release["d"])
	results := run.Wait()
	for i, result := range results {
		if result.ID != []string{"a", "b", "c", "d"}[i] || result.Status != update.Succeeded {
			t.Fatalf("unexpected ordered result: %+v", result)
		}
	}
	if maxActive != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maxActive)
	}
}

func TestFailureStopsOnlyItsPlan(t *testing.T) {
	failure := errors.New("upgrade failed")
	var called []string
	engine := update.Engine{Jobs: 1, Executor: executorFunc(func(_ context.Context, c process.Command, _ io.Writer) error {
		called = append(called, c.Path)
		if c.Path == "fail" {
			return failure
		}
		return nil
	})}
	broken := plan("broken")
	broken.Commands = []process.Command{{Path: "fail"}, {Path: "must-not-run"}}
	run, err := engine.Start(context.Background(), []update.Plan{broken, plan("healthy")})
	if err != nil {
		t.Fatal(err)
	}
	results := run.Wait()
	if !errors.Is(results[0].Err, failure) || results[0].Status != update.Failed || results[1].Status != update.Succeeded {
		t.Fatalf("unexpected results: %+v", results)
	}
	if strings.Join(called, ",") != "fail,healthy" {
		t.Fatalf("commands = %v", called)
	}
}

func TestCancellationIncludesQueuedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	engine := update.Engine{Jobs: 1, Executor: executorFunc(func(ctx context.Context, _ process.Command, _ io.Writer) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})}
	run, err := engine.Start(ctx, []update.Plan{plan("running"), plan("queued")})
	if err != nil {
		t.Fatal(err)
	}
	receive(t, started)
	cancel()
	receive(t, run.Done())
	results := run.Wait()
	for _, result := range results {
		if result.Status != update.Cancelled || !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("unexpected cancelled result: %+v", result)
		}
	}
	if !results[1].StartedAt.IsZero() {
		t.Fatal("queued task was started after cancellation")
	}
}

func TestSnapshotsAndFullLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	written := make(chan struct{})
	release := make(chan struct{})
	content := strings.Repeat("0123456789", 100)
	dir := t.TempDir()
	engine := update.Engine{LogDir: dir, TailBytes: 16, Executor: executorFunc(func(ctx context.Context, _ process.Command, w io.Writer) error {
		if _, err := io.WriteString(w, content); err != nil {
			return err
		}
		close(written)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})}
	run, err := engine.Start(ctx, []update.Plan{plan("../../unsafe-id")})
	if err != nil {
		t.Fatal(err)
	}
	receive(t, written)
	snapshot := run.Snapshot()
	if snapshot[0].Status != update.Running || snapshot[0].LogTail != content[len(content)-16:] {
		t.Fatalf("unexpected running snapshot: %+v", snapshot)
	}
	snapshot[0].Status = update.Failed
	if run.Snapshot()[0].Status != update.Running {
		t.Fatal("caller mutated live state")
	}
	close(release)
	result := run.Wait()[0]
	data, err := os.ReadFile(result.LogPath)
	if err != nil || string(data) != content || filepath.Dir(result.LogPath) != dir {
		t.Fatalf("full log mismatch: path=%s bytes=%d err=%v", result.LogPath, len(data), err)
	}
	info, err := os.Stat(result.LogPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions: info=%v err=%v", info, err)
	}
}

func TestInvalidPlansNeverExecute(t *testing.T) {
	cases := [][]update.Plan{
		{plan("")},
		{plan("same"), plan("same")},
		{{ID: "empty"}},
		{plan("resource", "")},
		{{ID: "command", Commands: []process.Command{{}}}},
	}
	for i, plans := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			e := update.Engine{Executor: executorFunc(func(context.Context, process.Command, io.Writer) error {
				t.Error("invalid plans executed")
				return nil
			})}
			if _, err := e.Start(context.Background(), plans); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestEmptyAndPreCancelledRuns(t *testing.T) {
	run, err := (update.Engine{}).Start(context.Background(), nil)
	if err != nil || len(run.Wait()) != 0 {
		t.Fatalf("empty run: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run, err = (update.Engine{}).Start(ctx, []update.Plan{plan("cancelled")})
	if err != nil || run.Wait()[0].Status != update.Cancelled {
		t.Fatalf("pre-cancelled run: %v", err)
	}
}

func TestLogCollisionFailsTaskWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "001.log")
	if err := os.WriteFile(name, []byte("existing log"), 0o600); err != nil {
		t.Fatal(err)
	}
	var executed []string
	e := update.Engine{Jobs: 1, LogDir: dir, Executor: executorFunc(func(_ context.Context, c process.Command, _ io.Writer) error {
		executed = append(executed, c.Path)
		return nil
	})}
	run, err := e.Start(context.Background(), []update.Plan{plan("collision"), plan("independent")})
	if err != nil {
		t.Fatal(err)
	}
	results := run.Wait()
	if results[0].Status != update.Failed || results[1].Status != update.Succeeded || strings.Join(executed, ",") != "independent" {
		t.Fatalf("results=%+v executed=%v", results, executed)
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "existing log" {
		t.Fatalf("existing log overwritten: %q %v", data, err)
	}
}

func TestExitHelper(t *testing.T) {
	if os.Getenv("UP_ENGINE_EXIT_HELPER") == "1" {
		os.Exit(9)
	}
}

func TestProcessExitCodeInResult(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process runner supports macOS and Linux")
	}
	p := update.Plan{ID: "exit", Commands: []process.Command{{
		Path: os.Args[0], Args: []string{"-test.run=^TestExitHelper$"},
		Env: []string{"UP_ENGINE_EXIT_HELPER=1", "GORACE=atexit_sleep_ms=0"},
	}}}
	run, err := (update.Engine{}).Start(context.Background(), []update.Plan{p})
	if err != nil {
		t.Fatal(err)
	}
	result := run.Wait()[0]
	if result.ExitCode != 9 || result.Status != update.Failed {
		t.Fatalf("unexpected process result: %+v", result)
	}
}
