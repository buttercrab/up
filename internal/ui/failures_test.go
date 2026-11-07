package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
	"github.com/charmbracelet/x/ansi"
)

func TestBackgroundFailureKeepsSelectedToolAndScrollback(t *testing.T) {
	m := fixture(t)
	m.width, m.height = 100, 28
	m.cfg.Plans = []update.Plan{{ID: "brew", Commands: []process.Command{{Path: "fake"}}}, {ID: "rustup", Commands: []process.Command{{Path: "fake"}}}}
	failed := make(chan struct{})
	written := make(chan struct{})
	m.cfg.Engine.Executor = fixtureExecutor(func(ctx context.Context, c process.Command, w io.Writer) error {
		if c.TaskID == "brew" {
			select {
			case <-failed:
				return errors.New("fixture failure")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		_, err := io.WriteString(w, strings.Repeat("rustup output\n", 40))
		if err != nil {
			return err
		}
		close(written)
		<-ctx.Done()
		return ctx.Err()
	})
	m.start()
	m.shell = newShellState(&Shell{})
	m.shell.screen = screenRun
	<-written
	m.selected = 1
	m.results = m.run.Snapshot()
	m.logs.SetHeight(4)
	m.refreshLogs()
	m.follow = false
	m.logs.SetYOffset(3)
	close(failed)
	deadline := time.Now().Add(time.Second)
	for m.run.Snapshot()[0].Status != update.Failed {
		if time.Now().After(deadline) {
			t.Fatal("fixture did not fail")
		}
		time.Sleep(time.Millisecond)
	}
	m.Update(pollMsg(time.Now()))
	if m.selected != 1 || m.follow || m.logs.YOffset() != 3 {
		t.Fatal("background failure moved selection or scrollback")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "1 failed · brew · f inspect") || !strings.Contains(view, "rustup / logs") {
		t.Fatalf("failure was not visible beside current work: %s", view)
	}
	press(m, 'f')
	view = ansi.Strip(m.View().Content)
	if m.selected != 0 || m.showProgress || !strings.Contains(view, "fixture failure") {
		t.Fatalf("explicit inspect did not open failure: %s", view)
	}
}

func TestFailureNavigationCyclesAndDoesNothingWithoutFailures(t *testing.T) {
	m := fixture(t)
	m.run = &update.Run{}
	m.results = []update.Result{{ID: "brew", Status: update.Failed}, {ID: "rustup", Status: update.Running}, {ID: "npm", Status: update.Failed}}
	m.selected = 1
	press(m, 'f')
	if m.selected != 2 {
		t.Fatal("did not find next failure")
	}
	press(m, 'f')
	if m.selected != 0 {
		t.Fatal("did not wrap failure navigation")
	}
	for _, size := range [][2]int{{1, 1}, {12, 4}, {30, 8}, {80, 24}, {116, 36}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		if len(strings.Split(view, "\n")) > m.height {
			t.Fatal("failure summary overflowed vertically")
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > m.width {
				t.Fatal("failure summary overflowed horizontally")
			}
		}
	}
	m.results[0].Status = update.Succeeded
	m.results[2].Status = update.Succeeded
	press(m, 'f')
	if m.selected != 0 || m.failureSummary(80) != "" {
		t.Fatal("advertised a nonexistent failure")
	}
}

func TestCompletedToolDoesNotClaimOutputIsStillPending(t *testing.T) {
	m := fixture(t)
	m.done = true
	m.logs.SetHeight(4)
	m.results[0].Status = update.Failed
	m.refreshLogs()
	if text := m.logs.View(); strings.Contains(text, "Waiting") || !strings.Contains(text, "No output was captured") {
		t.Fatalf("misleading final empty state: %s", text)
	}
}
