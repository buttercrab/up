package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
	"github.com/charmbracelet/x/ansi"
)

func fixture(t *testing.T) *model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newModel(ctx, Config{Plans: []update.Plan{{ID: "brew", Commands: []process.Command{{Path: "fake"}}}}, Engine: update.Engine{Jobs: 4}})
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

func TestLayoutDimensionAndStateMatrix(t *testing.T) {
	widths := []int{0, 1, 2, 5, 8, 12, 20, 30, 38, 40, 60, 80, 120}
	heights := []int{0, 1, 2, 3, 5, 8, 12, 16, 20, 24, 32}
	for _, state := range []string{"review", "live", "failure", "demo"} {
		for _, count := range []int{0, 1, 3, 9} {
			t.Run(fmt.Sprintf("%s/%d", state, count), func(t *testing.T) {
				m := fixture(t)
				m.cfg.Plans = nil
				m.results = nil
				m.cfg.Skipped = []string{"optional-long-tool-name", "another-unavailable-tool"}
				for i := range count {
					id := fmt.Sprintf("task-%02d", i)
					m.cfg.Plans = append(m.cfg.Plans, update.Plan{ID: id, Commands: []process.Command{{Path: "/Applications/A very long tool name/bin/updater", Args: []string{"upgrade", strings.Repeat("long-argument-", 15)}}}})
					m.results = append(m.results, update.Result{ID: id, Status: update.Queued, ExitCode: -1})
				}
				m.selected = max(0, count-1)
				m.cfg.Demo = state == "demo"
				if state == "live" || state == "failure" {
					m.run = &update.Run{}
					for i := range m.results {
						m.results[i].Status = update.Running
						m.results[i].LogTail = strings.Repeat("Long log line with a tab\tand wide characters 界界界\n", 30)
						m.results[i].LogPath = "/Users/some-user/Library/Caches/up/runs/a-very-long-run-directory-name/001.log"
					}
				}
				if state == "failure" {
					m.done = true
					m.note = "Returned to dashboard. " + strings.Repeat("long note ", 12)
					for i := range m.results {
						m.results[i].Status = update.Succeeded
					}
					if count > 0 {
						r := &m.results[m.selected]
						r.Status = update.Failed
						r.Err = errors.New("command failed")
						r.LogTail = "Error: font-geist: It seems the Font source 'GeistVF.ttf' is not there.\n"
					} else {
						m.err = errors.New("Could not start: " + strings.Repeat("long error ", 12))
					}
				}
				for _, width := range widths {
					for _, height := range heights {
						m.Update(tea.WindowSizeMsg{Width: width, Height: height})
						text := ansi.Strip(m.View().Content)
						var lines []string
						if text != "" {
							lines = strings.Split(text, "\n")
						}
						if len(lines) > height {
							t.Fatalf("%dx%d height=%d:\n%s", width, height, len(lines), text)
						}
						for _, line := range lines {
							if got := ansi.StringWidth(line); got > width {
								t.Fatalf("%dx%d line width=%d: %q", width, height, got, line)
							}
						}
						if width == 0 || height == 0 {
							if text != "" {
								t.Fatalf("nonempty zero-size view: %q", text)
							}
							continue
						}
						if !strings.Contains(text, "q") {
							t.Fatalf("%dx%d lost quit/stop control: %q", width, height, text)
						}
						if width >= 20 && height >= 2 && count > 0 && !strings.Contains(text, m.results[m.selected].ID) {
							t.Fatalf("%dx%d selected task hidden: %s", width, height, text)
						}
						if width >= 12 && height >= 2 && (state == "review" || state == "demo") && !strings.Contains(text, "Enter") {
							t.Fatalf("%dx%d confirmation control hidden: %s", width, height, text)
						}
					}
				}
			})
		}
	}
}

func TestMeasuredLayoutKeepsFailureDetailsAndControls(t *testing.T) {
	m := fixture(t)
	m.run = &update.Run{}
	m.done = true
	m.note = "Returned to dashboard."
	m.results[0] = update.Result{ID: "brew", Status: update.Failed, Err: errors.New("failure"), LogTail: "Error: font-geist: It seems the Font source 'GeistVF.ttf' is not there.\n", LogPath: "/tmp/up/full.log"}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 32})
	text := ansi.Strip(m.View().Content)
	for _, want := range []string{"expected font file is missing", "Homebrew references a missing font", "Returned to dashboard.", "Full log: /tmp/up/full.log", "Enter/q close"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
}

func TestResizePreservesLogSelectionScrollbackAndFollow(t *testing.T) {
	m := fixture(t)
	m.run = &update.Run{}
	m.results = []update.Result{{ID: "brew", Status: update.Running}, {ID: "npm", Status: update.Running}}
	for i := range m.results {
		for n := range 100 {
			m.results[i].LogTail += fmt.Sprintf("%s line %d\n", m.results[i].ID, n)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m.View()
	m.Update(tea.KeyPressMsg{Code: 'j'})
	m.View()
	if m.selected != 1 || !strings.Contains(ansi.Strip(m.logs.View()), "npm line 99") {
		t.Fatal("selected log is not following its task")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	offset := m.logs.YOffset()
	if m.follow || m.logs.AtBottom() {
		t.Fatal("PageUp did not pause follow mode")
	}
	m.results[1].LogTail += "npm appended\n"
	m.refreshLogs()
	m.View()
	if m.logs.YOffset() != offset {
		t.Fatal("new output moved paused scrollback")
	}
	m.Update(tea.WindowSizeMsg{Width: 38, Height: 16})
	view := ansi.Strip(m.View().Content)
	if m.selected != 1 || !strings.Contains(view, "npm") || m.follow {
		t.Fatal("resize lost selection or scroll mode")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m.View()
	if !m.follow || !strings.Contains(ansi.Strip(m.logs.View()), "npm appended") {
		t.Fatal("End did not resume following live output")
	}
}

func TestTerminalBackgroundChoosesReadablePalette(t *testing.T) {
	m := fixture(t)
	original := m.styles.accent.GetForeground()
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if reflect.DeepEqual(original, m.styles.accent.GetForeground()) {
		t.Fatal("light background did not change palette")
	}
	for name, style := range map[string]lipgloss.Style{"accent": m.styles.accent, "muted": m.styles.muted, "success": m.styles.good, "failure": m.styles.bad, "warning": m.styles.warn} {
		r, g, b, _ := style.GetForeground().RGBA()
		linear := func(v uint32) float64 {
			x := float64(v) / 65535
			if x <= 0.04045 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		luminance := 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
		if contrast := 1.05 / (luminance + 0.05); contrast < 4.5 {
			t.Errorf("%s contrast %.2f on white", name, contrast)
		}
	}
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if !reflect.DeepEqual(original, m.styles.accent.GetForeground()) {
		t.Fatal("dark background did not restore dark palette")
	}
}
