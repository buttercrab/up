//go:build darwin || linux

package process_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/buttercrab/up/internal/process"
	"github.com/creack/pty"
	"golang.org/x/term"
)

type safeBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (w *safeBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if w.ready != nil && strings.Contains(w.buf.String(), "Interactive terminal") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *safeBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for terminal state")
}

func TestTerminalPromptAttachDetachAndPasswordPrivacy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	manager := &process.TerminalManager{}
	manager.Resize(100, 30)
	var log safeBuffer
	c := helper("password")
	c.TaskID = "fixture"
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx, c, &log) }()
	var session *process.TerminalSession
	waitFor(t, func() bool {
		select {
		case err := <-done:
			t.Fatalf("terminal exited before prompt: %v; log=%q", err, log.String())
		default:
		}
		session = manager.Session("fixture")
		return session != nil && session.NeedsInput()
	})
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	// First attach and detach while the same process remains at its prompt.
	transcript := &safeBuffer{ready: make(chan struct{})}
	attached := make(chan error, 1)
	go func() { attached <- manager.Attach(ctx, session, slave, transcript) }()
	select {
	case <-transcript.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := master.Write([]byte{0x1d}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-attached:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if session.NeedsInput() {
		t.Fatal("detached prompt should not auto-attach again")
	}
	select {
	case err := <-done:
		t.Fatalf("detaching killed command: %v", err)
	default:
	}
	// Reattach and answer a /dev/tty prompt with echo disabled, as sudo does.
	transcript = &safeBuffer{ready: make(chan struct{})}
	go func() { attached <- manager.Attach(ctx, session, slave, transcript) }()
	select {
	case <-transcript.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := master.Write([]byte("fixture-secret\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-attached:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal state not restored: %v", err)
	}
	if strings.Contains(log.String()+transcript.String(), "fixture-secret") {
		t.Fatal("password was echoed or logged")
	}
	if !strings.Contains(log.String(), "authenticated") {
		t.Fatalf("missing output: %q", log.String())
	}
	if manager.Session("fixture") != nil {
		t.Fatal("completed session retained")
	}
}

func TestTerminalCancellationWhileAttached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &process.TerminalManager{}
	c := helper("password")
	c.TaskID = "cancel"
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx, c, io.Discard) }()
	var session *process.TerminalSession
	waitFor(t, func() bool {
		select {
		case err := <-done:
			t.Fatalf("terminal exited before prompt: %v", err)
		default:
		}
		session = manager.Session("cancel")
		return session != nil && session.NeedsInput()
	})
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	attached := make(chan error, 1)
	transcript := &safeBuffer{ready: make(chan struct{})}
	go func() { attached <- manager.Attach(ctx, session, slave, transcript) }()
	select {
	case <-transcript.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("attach timeout")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command cancellation timeout")
	}
	select {
	case <-attached:
	case <-time.After(time.Second):
		t.Fatal("input read leaked after cancellation")
	}
}

// This helper only writes fixture output, then either exits or waits to be
// terminated. It never invokes installed package managers or privileged tools.
func TestTerminalOutputHelper(t *testing.T) {
	if os.Getenv("UP_TERMINAL_OUTPUT_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "fixture output")
	if os.Getenv("UP_TERMINAL_OUTPUT_MODE") == "wait" {
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func terminalOutputCommand(mode string) process.Command {
	return process.Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestTerminalOutputHelper$"},
		Env: []string{
			"UP_TERMINAL_OUTPUT_HELPER=1",
			"UP_TERMINAL_OUTPUT_MODE=" + mode,
			"GORACE=atexit_sleep_ms=0",
		},
		TaskID: "fixture-output",
	}
}

type failingTerminalWriter struct{ err error }

func (w failingTerminalWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTerminalPreservesWriterErrorsAndTerminatesCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"EIO", syscall.EIO},
		{"closed", os.ErrClosed},
		{"EOF", io.EOF},
	} {
		for _, mode := range []string{"exit", "wait"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				manager := &process.TerminalManager{}
				command := terminalOutputCommand(mode)
				err := manager.Run(ctx, command, failingTerminalWriter{tc.err})
				if !errors.Is(err, tc.err) || !strings.Contains(err.Error(), "terminal output") {
					t.Fatalf("writer error lost: got %v, want terminal output error wrapping %v", err, tc.err)
				}
				if ctx.Err() != nil {
					t.Fatal("writer failure did not terminate the command before its timeout")
				}
				if manager.Session(command.TaskID) != nil {
					t.Fatal("failed output left a terminal session behind")
				}
			})
		}
	}
}

func TestTerminalSuccessfulExitDrainsOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := (&process.TerminalManager{}).Run(ctx, terminalOutputCommand("exit"), &output); err != nil {
		t.Fatalf("ordinary PTY closure treated as failure: %v", err)
	}
	if !strings.Contains(output.String(), "fixture output") {
		t.Fatalf("terminal output was not drained: %q", output.String())
	}
}

// Model cancellation immediately after Run's initial context check, before the
// process has started and acquired a PID.
type cancelAfterFirstCheck struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelAfterFirstCheck) Err() error {
	err := c.Context.Err()
	c.once.Do(c.cancel)
	return err
}

func TestTerminalCancellationBeforeStartDoesNotPublishSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &process.TerminalManager{}
	command := terminalOutputCommand("exit")
	err := manager.Run(&cancelAfterFirstCheck{Context: ctx, cancel: cancel}, command, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before Start, got %v", err)
	}
	if manager.Session(command.TaskID) != nil {
		t.Fatal("failed Start published a terminal session")
	}
}
