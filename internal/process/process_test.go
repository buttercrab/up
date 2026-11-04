//go:build darwin || linux

package process_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/buttercrab/up/internal/process"
	"golang.org/x/term"
)

func helper(mode string, args ...string) process.Command {
	return process.Command{
		Path: os.Args[0],
		Args: append([]string{"-test.run=^TestHelperProcess$", "--", mode}, args...),
		Env:  []string{"UP_TEST_HELPER=1", "GORACE=atexit_sleep_ms=0"},
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("UP_TEST_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	switch args[0] {
	case "password":
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		fmt.Fprint(tty, "Password:")
		password, err := term.ReadPassword(int(tty.Fd()))
		if err != nil || string(password) != "fixture-secret" {
			os.Exit(5)
		}
		fmt.Fprintln(os.Stdout, "\nauthenticated")
	case "output":
		cwd, _ := os.Getwd()
		fmt.Fprintf(os.Stdout, "stdout:%s:%s\n", os.Getenv("UP_TEST_VALUE"), cwd)
		fmt.Fprintln(os.Stderr, "stderr")
	case "fail":
		os.Exit(7)
	case "wait-output":
		fmt.Fprint(os.Stdout, "fixture output")
		time.Sleep(30 * time.Second)
	case "grace-parent", "orphan-grace":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		childMode := "grace-child-closed"
		if args[0] == "orphan-grace" {
			childMode = "grace-child-open"
		}
		c := helper(childMode, args[1])
		cmd := exec.Command(c.Path, c.Args...)
		cmd.Env = append(os.Environ(), c.Env...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(args[1]+".parent", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		if args[0] == "grace-parent" {
			<-signals
		}
	case "grace-child-closed", "grace-child-open":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		if args[0] == "grace-child-closed" {
			_ = os.Stdout.Close()
			_ = os.Stderr.Close()
		}
		<-signals
		time.Sleep(250 * time.Millisecond)
		if err := os.WriteFile(args[1], []byte("graceful cleanup"), 0o600); err != nil {
			os.Exit(3)
		}
	case "tree":
		signal.Ignore(syscall.SIGTERM)
		c := helper("child", args[1])
		cmd := exec.Command(c.Path, c.Args...)
		cmd.Env = append(os.Environ(), c.Env...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(3)
		}
	case "orphan", "orphan-fail":
		c := helper("heartbeat", args[1])
		cmd := exec.Command(c.Path, c.Args...)
		cmd.Env = append(os.Environ(), c.Env...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
		if args[0] == "orphan-fail" {
			os.Exit(7)
		}
	case "heartbeat":
		signal.Ignore(syscall.SIGTERM)
		f, err := os.Create(args[1])
		if err != nil {
			os.Exit(3)
		}
		for range 6000 {
			_, _ = f.WriteString("x")
			time.Sleep(10 * time.Millisecond)
		}
	case "child":
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(600 * time.Millisecond)
		_ = os.WriteFile(args[1], []byte("child survived"), 0o600)
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func TestRunnerOutputEnvironmentAndDirectory(t *testing.T) {
	t.Setenv("UP_TEST_VALUE", "inherited")
	cmd := helper("output")
	cmd.Env = append(cmd.Env, "UP_TEST_VALUE=override")
	cmd.Dir = t.TempDir()
	var output bytes.Buffer
	if err := (process.Runner{}).Run(context.Background(), cmd, &output); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(cmd.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "stdout:override:"+dir) || !strings.Contains(output.String(), "stderr") {
		t.Fatalf("unexpected output: %q", output.String())
	}
}

func TestRunnerErrors(t *testing.T) {
	err := (process.Runner{}).Run(context.Background(), helper("fail"), io.Discard)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("expected exit code 7, got %v", err)
	}
	if err := (process.Runner{}).Run(context.Background(), process.Command{Path: filepath.Join(t.TempDir(), "missing")}, nil); err == nil {
		t.Fatal("missing executable succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (process.Runner{}).Run(ctx, helper("output"), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestRunnerMetadataProbeDiscardsStderr(t *testing.T) {
	var output bytes.Buffer
	if err := (process.Runner{DiscardStderr: true}).Run(context.Background(), helper("output"), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "stdout:") || strings.Contains(output.String(), "stderr") {
		t.Fatalf("stderr leaked into metadata: %q", output.String())
	}
}

type readyWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *readyWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "ready") {
		w.once.Do(func() { close(w.ready) })
	}
	return len(p), nil
}

func TestCancellationKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "survivor")
	w := &readyWriter{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- (process.Runner{GracePeriod: 50 * time.Millisecond}).Run(ctx, helper("tree", marker), w)
	}()
	select {
	case <-w.ready:
	case err := <-done:
		t.Fatalf("process exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("process did not become ready")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not terminate command")
	}
	// An ignored TERM must not leave a grandchild running after Wait returns.
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
}

func TestOutputPipeTimeoutCleansUpDescendants(t *testing.T) {
	for _, mode := range []string{"orphan", "orphan-fail"} {
		t.Run(mode, func(t *testing.T) { testPipeTimeout(t, mode) })
	}
}

func testPipeTimeout(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "heartbeat")
	err := (process.Runner{GracePeriod: 10 * time.Millisecond}).Run(ctx, helper(mode, marker), io.Discard)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected inherited output pipe timeout, got %v", err)
	}
	if mode == "orphan-fail" {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 7 {
			t.Fatalf("lost parent exit code 7: %v", err)
		}
	}
	before, err := os.ReadFile(marker)
	if err != nil || len(before) == 0 {
		t.Fatalf("descendant did not start: bytes=%d err=%v", len(before), err)
	}
	// Signal delivery is asynchronous: one write already in flight can finish
	// after KILL is sent. Require the heartbeat to stop within a bounded window,
	// then remain stopped; a surviving child writes every ten milliseconds.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		after, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if len(before) == len(after) {
			time.Sleep(100 * time.Millisecond)
			final, err := os.ReadFile(marker)
			if err == nil && len(final) == len(after) {
				return
			}
		}
		before = after
	}
	t.Fatal("descendant heartbeat continued after output timeout")
}

func TestCancellationPreservesDescendantGrace(t *testing.T) {
	for _, mode := range []string{"grace-parent", "orphan-grace"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			marker := filepath.Join(t.TempDir(), "cleanup")
			w := &readyWriter{ready: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- (process.Runner{GracePeriod: time.Second}).Run(ctx, helper(mode, marker), w) }()
			select {
			case <-w.ready:
			case err := <-done:
				t.Fatalf("exited before ready: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("child not ready")
			}
			if mode == "orphan-grace" {
				// Prove the leader was reaped, so only our group-lifetime watcher
				// can deliver TERM while the descendant retains its output pipe.
				deadline := time.Now().Add(time.Second)
				for {
					data, err := os.ReadFile(marker + ".parent")
					if err == nil {
						pid, err := strconv.Atoi(string(data))
						if err == nil && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
							break
						}
					}
					if time.Now().After(deadline) {
						t.Fatal("parent was not reaped before cancellation")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation timeout")
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "graceful cleanup" {
				t.Fatalf("descendant was killed before graceful cleanup: %q %v", data, err)
			}
		})
	}
}

type failingPipeWriter struct{ err error }

func (w failingPipeWriter) Write([]byte) (int, error) { return 0, w.err }

func TestPipeOutputFailureTerminatesCommand(t *testing.T) {
	for _, failure := range []error{syscall.EIO, os.ErrClosed, io.EOF} {
		t.Run(failure.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := (process.Runner{GracePeriod: 10 * time.Millisecond}).Run(ctx, helper("wait-output"), failingPipeWriter{failure})
			if !errors.Is(err, failure) {
				t.Fatalf("lost output error: got %v, want %v", err, failure)
			}
			if ctx.Err() != nil {
				t.Fatal("writer failure did not stop command before context timeout")
			}
		})
	}
}
