package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
	"github.com/charmbracelet/x/ansi"
)

func fixture(t *testing.T) *model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newModel(ctx, cancel, Config{Plans: []update.Plan{{ID: "brew", Commands: []process.Command{{Path: "fake"}}}}, Engine: update.Engine{Jobs: 4}})
}

type fixtureExecutor func(context.Context, process.Command, io.Writer) error

func (f fixtureExecutor) Run(ctx context.Context, c process.Command, w io.Writer) error {
	return f(ctx, c, w)
}

func TestLiveSnapshotsAndCompletion(t *testing.T) {
	m := fixture(t)
	written := make(chan struct{})
	release := make(chan struct{})
	m.cfg.Engine.Executor = fixtureExecutor(func(ctx context.Context, _ process.Command, w io.Writer) error {
		_, err := io.WriteString(w, "Downloading fixture package\n")
		close(written)
		if err != nil {
			return err
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.run == nil {
		t.Fatal("Enter did not start run")
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	m.Update(pollMsg(time.Now()))
	text := ansi.Strip(m.View().Content)
	for _, want := range []string{"Upgrading your tools", "running", "Downloading fixture package", "0/1 finished"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing live state %q: %s", want, text)
		}
	}
	close(release)
	m.run.Wait()
	m.Update(pollMsg(time.Now()))
	text = ansi.Strip(m.View().Content)
	if !strings.Contains(text, "Run complete") || !strings.Contains(text, "1/1 finished") || !strings.Contains(text, "1 succeeded") {
		t.Fatalf("missing completion: %s", text)
	}
}

func TestReviewNeverExecutesBeforeEnter(t *testing.T) {
	m := fixture(t)
	m.Init()
	if m.run != nil {
		t.Fatal("review started an upgrade")
	}
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "Nothing has run yet") || !strings.Contains(got, "fake") {
		t.Fatalf("review missing plan: %s", got)
	}
	m.Update(tea.KeyPressMsg{Code: 'q'})
	if m.run != nil {
		t.Fatal("quitting review executed commands")
	}
}

func TestLiveAndFailureViews(t *testing.T) {
	m := fixture(t)
	m.run = &update.Run{}
	m.results = []update.Result{{ID: "brew", Status: update.Failed, ExitCode: 1, Err: errors.New("failed"), LogTail: "Error: Missing font file\n", LogPath: "/tmp/log"}}
	m.done = true
	for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 32}, {Width: 60, Height: 24}, {Width: 30, Height: 12}} {
		m.Update(size)
		text := ansi.Strip(m.View().Content)
		if len(strings.Split(text, "\n")) > size.Height {
			t.Fatalf("vertical overflow at %dx%d: %s", size.Width, size.Height, text)
		}
		if !strings.Contains(text, "brew") {
			t.Fatal("task disappeared")
		}
		if size.Width >= 38 && (!strings.Contains(text, "Missing font file") || !strings.Contains(text, "1 failed") || !strings.Contains(text, "1/1 finished")) {
			t.Fatalf("missing final results: %s", text)
		}
		for _, line := range strings.Split(text, "\n") {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("overflow at %dx%d: width=%d %q", size.Width, size.Height, ansi.StringWidth(line), line)
			}
		}
	}
}

func TestParallelCountAndSharedResourceWait(t *testing.T) {
	m := fixture(t)
	m.run = &update.Run{}
	m.cfg.Plans = []update.Plan{{ID: "brew", Resources: []string{"homebrew"}}, {ID: "rustup", Resources: []string{"rust-toolchains"}}, {ID: "npm", Resources: []string{"homebrew", "npm-global"}}}
	m.results = []update.Result{{ID: "brew", Status: update.Running}, {ID: "rustup", Status: update.Running}, {ID: "npm", Status: update.Queued}}
	text := ansi.Strip(m.View().Content)
	if !strings.Contains(text, "2 running") || !strings.Contains(text, "waiting for homebrew") {
		t.Fatalf("missing parallel status: %s", text)
	}
	m.results[2].Status = update.Running
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "3 running") {
		t.Fatalf("missing active count: %s", text)
	}
}
