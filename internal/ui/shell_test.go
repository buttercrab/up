package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/buttercrab/up/internal/inventory"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
	"github.com/charmbracelet/x/ansi"
)

func shellFixture(t *testing.T, callbacks *Shell) *model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newModel(ctx, Config{Shell: callbacks})
}
func press(m *model, code rune) tea.Cmd { _, cmd := m.Update(tea.KeyPressMsg{Code: code}); return cmd }
func deliver(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected asynchronous loader")
	}
	m.Update(cmd())
}
func testReport() inventory.Report {
	return inventory.Report{SchemaVersion: 1, Scope: inventory.CurrentPath,
		Installations: []inventory.Installation{{ID: "npm", Name: "npm", Executable: "/usr/local/bin/npm", ResolvedExecutable: "/prefix/node/npm-cli.js", Owner: inventory.OwnerHomebrew, Prefix: "/prefix", PrefixKnown: true, UpdateSupport: inventory.Blocked, Reason: "Runtime owner must be checked", Runtimes: []inventory.Runtime{{ID: "node", Executable: "/usr/local/bin/node", ResolvedExecutable: "/prefix/node", Owner: inventory.OwnerHomebrew}}}},
		Missing:       []inventory.MissingTool{{ID: "uv", Name: "uv", Commands: []string{"uv"}}}}
}
func planConfig(id string, executor process.Executor) Config {
	return Config{Plans: []update.Plan{{ID: id, Commands: []process.Command{{Path: "fake"}}}}, Engine: update.Engine{Jobs: 1, Executor: executor}}
}

func TestShellNavigationIsReadOnlyAndRejectsStaleResponses(t *testing.T) {
	loads, prepares, executes := 0, 0, 0
	var loadedContext context.Context
	callback := func(context.Context) (Config, error) {
		cfg := planConfig("real", fixtureExecutor(func(context.Context, process.Command, io.Writer) error { executes++; return nil }))
		cfg.PrepareRun = func(context.Context) (update.Engine, error) { prepares++; return cfg.Engine, nil }
		return cfg, nil
	}
	m := shellFixture(t, &Shell{LoadUpgrades: callback, LoadDemo: callback, LoadInventory: func(ctx context.Context) (inventory.Report, error) {
		loads++
		loadedContext = ctx
		return testReport(), nil
	}})
	m.Init()
	if m.shell.screen != screenHome || prepares != 0 || executes != 0 {
		t.Fatal("opening home started execution")
	}
	press(m, tea.KeyDown)
	cmd := press(m, tea.KeyEnter)
	if m.shell.screen != screenLoading || loads != 0 {
		t.Fatal("inventory callback ran synchronously")
	}
	stale := cmd()
	press(m, tea.KeyEscape)
	if loadedContext.Err() != context.Canceled {
		t.Fatal("back did not cancel discovery")
	}
	m.Update(stale)
	if m.shell.screen != screenHome || len(m.shell.items) != 0 {
		t.Fatal("stale inventory response replaced home")
	}
	deliver(t, m, press(m, tea.KeyEnter))
	if m.shell.screen != screenInventory {
		t.Fatal("inventory was not loaded")
	}
	press(m, tea.KeyEnter)
	if m.shell.screen != screenDetails {
		t.Fatal("Enter did not open details")
	}
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 40})
	text := ansi.Strip(m.View().Content)
	for _, want := range []string{"Owner: homebrew", "Prefix: /prefix", "Runtime owner: homebrew", "Upgrade support: blocked", "Runtime owner must be checked"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	deliver(t, m, press(m, 'r'))
	if m.shell.screen != screenDetails || m.shell.items[m.shell.selected].id() != "npm" {
		t.Fatal("refresh lost details selection")
	}
	press(m, tea.KeyEscape)
	press(m, tea.KeyDown)
	press(m, tea.KeyEnter)
	if text := m.inventoryDetails(m.shell.items[m.shell.selected]); !strings.Contains(text, "missing from current PATH") || !strings.Contains(text, "not evaluated (tool missing)") {
		t.Fatalf("missing tool represented incorrectly: %s", text)
	}
	press(m, tea.KeyEscape)
	press(m, tea.KeyEscape)
	if m.shell.screen != screenHome || prepares != 0 || executes != 0 || m.run != nil {
		t.Fatal("navigation caused an upgrade or log preparation")
	}
	if cmd := press(m, 'q'); cmd == nil {
		t.Fatal("q did not quit home")
	}
	if len(m.lastRealResults) != 0 {
		t.Fatal("inventory-only session has upgrade results")
	}
}

func TestShellPerRunCancellationAndLatestRealResults(t *testing.T) {
	loads, prepares := 0, 0
	started := make(chan struct{}, 1)
	real := func(context.Context) (Config, error) {
		loads++
		first := loads == 1
		cfg := planConfig("real", fixtureExecutor(func(ctx context.Context, _ process.Command, _ io.Writer) error {
			if first {
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			}
			return errors.New("fixture upgrade failure")
		}))
		cfg.PrepareRun = func(ctx context.Context) (update.Engine, error) {
			prepares++
			if ctx.Err() != nil {
				t.Error("new run inherited a cancelled context")
			}
			return cfg.Engine, nil
		}
		return cfg, nil
	}
	demo := func(context.Context) (Config, error) {
		return planConfig("demo", fixtureExecutor(func(context.Context, process.Command, io.Writer) error { return nil })), nil
	}
	m := shellFixture(t, &Shell{LoadUpgrades: real, LoadDemo: demo})
	deliver(t, m, press(m, tea.KeyEnter))
	if prepares != 0 || m.run != nil {
		t.Fatal("review created run resources")
	}
	press(m, tea.KeyEnter)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("run did not start")
	}
	press(m, tea.KeyEscape)
	m.run.Wait()
	m.Update(pollMsg(time.Now()))
	if m.shell.screen != screenHome || m.ctx.Err() != nil || len(m.lastRealResults) != 1 || m.lastRealResults[0].Status != update.Cancelled {
		t.Fatal("cancel/back poisoned the session or lost results")
	}
	deliver(t, m, press(m, tea.KeyEnter))
	press(m, tea.KeyEnter)
	m.run.Wait()
	m.Update(pollMsg(time.Now()))
	if !m.done || prepares != 2 || m.lastRealResults[0].Status != update.Failed {
		t.Fatal("second real run did not replace earlier results")
	}
	press(m, tea.KeyEscape)
	press(m, tea.KeyDown)
	press(m, tea.KeyDown)
	deliver(t, m, press(m, tea.KeyEnter))
	if !m.cfg.Demo || m.run != nil {
		t.Fatal("demo skipped review or was not labeled")
	}
	press(m, tea.KeyEnter)
	m.run.Wait()
	m.Update(pollMsg(time.Now()))
	if m.lastRealResults[0].ID != "real" || m.lastRealResults[0].Status != update.Failed {
		t.Fatal("demo overwrote real upgrade exit results")
	}
	press(m, tea.KeyEnter)
	if m.shell.screen != screenHome || m.ctx.Err() != nil {
		t.Fatal("completed demo did not return home")
	}
}

func TestShellLoadingErrorsRetryAndInitialInventory(t *testing.T) {
	calls := 0
	m := shellFixture(t, &Shell{StartInventory: true, LoadInventory: func(context.Context) (inventory.Report, error) {
		calls++
		if calls == 1 {
			return inventory.Report{}, errors.New("fixture discovery failed")
		}
		return testReport(), nil
	}})
	// Init changes screen immediately; the callback is still a command handled
	// asynchronously by the one Bubble Tea program.
	commands := m.Init()().(tea.BatchMsg)
	if m.shell.screen != screenLoading || calls != 0 {
		t.Fatal("initial inventory did not load asynchronously")
	}
	for _, cmd := range commands {
		if cmd == nil {
			continue
		}
		msg := cmd()
		if _, ok := msg.(loadedMsg); ok {
			m.Update(msg)
		}
	}
	if m.shell.screen != screenLoadError || m.err != nil {
		t.Fatal("discovery failure was fatal to the session")
	}
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "fixture discovery failed") {
		t.Fatalf("discovery error hidden: %s", text)
	}
	deliver(t, m, press(m, 'r'))
	if m.shell.screen != screenInventory || calls != 2 {
		t.Fatal("retry failed")
	}
	press(m, tea.KeyEscape)
	if m.shell.screen != screenHome {
		t.Fatal("initial inventory cannot return home")
	}
}

func TestShellQuitCancelsRunWithoutCancellingSessionContext(t *testing.T) {
	started := make(chan struct{}, 1)
	m := shellFixture(t, &Shell{LoadDemo: func(context.Context) (Config, error) {
		return planConfig("demo", fixtureExecutor(func(ctx context.Context, _ process.Command, _ io.Writer) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})), nil
	}})
	press(m, tea.KeyDown)
	press(m, tea.KeyDown)
	deliver(t, m, press(m, tea.KeyEnter))
	press(m, tea.KeyEnter)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("demo did not start")
	}
	press(m, 'q')
	m.run.Wait()
	_, cmd := m.Update(pollMsg(time.Now()))
	if cmd == nil || !errors.Is(m.exitErr, context.Canceled) || m.ctx.Err() != nil {
		t.Fatal("quit did not finish run cleanup and record explicit cancellation")
	}
	if len(m.lastRealResults) != 0 {
		t.Fatal("cancelled demo replaced real results")
	}
}

func TestShellDimensionMatrixAndInventoryScroll(t *testing.T) {
	for _, state := range []screen{screenHome, screenLoading, screenLoadError, screenInventory, screenDetails} {
		m := shellFixture(t, &Shell{})
		m.shell.screen = state
		m.shell.menu = 2
		m.shell.loading = loadInventory
		m.shell.loadErr = errors.New(strings.Repeat("long discovery error ", 20))
		m.shell.report = testReport()
		for i := range m.shell.report.Installations {
			m.shell.items = append(m.shell.items, inventoryItem{installed: &m.shell.report.Installations[i]})
		}
		m.shell.items = append(m.shell.items, inventoryItem{missing: &m.shell.report.Missing[0]})
		m.shell.selected = 1
		for _, width := range []int{0, 1, 2, 8, 12, 20, 30, 38, 60, 80, 120} {
			for _, height := range []int{0, 1, 2, 3, 5, 8, 12, 16, 24, 32} {
				m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				text := ansi.Strip(m.View().Content)
				if width == 0 || height == 0 {
					if text != "" {
						t.Fatal("zero-size shell rendered content")
					}
					continue
				}
				lines := strings.Split(text, "\n")
				if len(lines) > height {
					t.Fatalf("screen=%d %dx%d height=%d: %s", state, width, height, len(lines), text)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("screen=%d %dx%d overflow: %q", state, width, height, line)
					}
				}
				if !strings.Contains(text, "q") {
					t.Fatalf("screen=%d %dx%d quit hidden: %s", state, width, height, text)
				}
				if width >= 20 && height >= 2 && (state == screenInventory || state == screenDetails) && !strings.Contains(text, "uv") {
					t.Fatalf("selected inventory tool hidden: %s", text)
				}
			}
		}
	}
	m := shellFixture(t, &Shell{})
	m.shell.screen = screenDetails
	entry := testReport().Installations[0]
	entry.Reason = strings.Repeat("Long inspection reason ", 60)
	m.shell.items = []inventoryItem{{installed: &entry}}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	m.View()
	press(m, tea.KeyPgDown)
	if m.shell.details.YOffset() == 0 {
		t.Fatal("inventory details did not scroll")
	}
	press(m, tea.KeyEscape)
	if m.shell.screen != screenInventory {
		t.Fatal("details Esc did not return to inventory list")
	}
}

func TestShellShutdownWaitsOnlyForStartedLoaderCleanup(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	m := shellFixture(t, &Shell{LoadInventory: func(ctx context.Context) (inventory.Report, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return inventory.Report{}, ctx.Err()
	}})
	press(m, tea.KeyDown)
	cmd := press(m, tea.KeyEnter)
	replied := make(chan tea.Msg, 1)
	go func() { replied <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("loader did not start")
	}
	press(m, 'q')
	go func() { m.stopLoaders(); close(stopped) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("quit did not cancel loader")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown returned before loader cleanup")
	default:
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join completed loader")
	}
	m.Update(<-replied)
	if m.shell.screen != screenLoading {
		t.Fatal("cancelled response changed the screen after quit")
	}
	calls := 0
	queued := shellFixture(t, &Shell{LoadInventory: func(context.Context) (inventory.Report, error) { calls++; return testReport(), nil }})
	press(queued, tea.KeyDown)
	late := press(queued, tea.KeyEnter)
	done := make(chan struct{})
	go func() { queued.stopLoaders(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unstarted command blocked shutdown")
	}
	if msg := late().(loadedMsg); !errors.Is(msg.err, context.Canceled) || calls != 0 {
		t.Fatal("queued command began a callback after shutdown")
	}
}

func TestInventorySupportUsesFriendlyVisibleLabels(t *testing.T) {
	m := shellFixture(t, &Shell{})
	for _, tc := range []struct {
		support inventory.Support
		label   string
	}{{inventory.Supported, "supported"}, {inventory.Unsupported, "no updater"}, {inventory.Blocked, "blocked"}} {
		entry := testReport().Installations[0]
		entry.UpdateSupport = tc.support
		item := inventoryItem{installed: &entry}
		m.shell.items = []inventoryItem{item}
		list := ansi.Strip(strings.Join(m.inventoryRows(80, 1), "\n"))
		details := m.inventoryDetails(item)
		if !strings.Contains(list, tc.label) || !strings.Contains(details, "Upgrade support: "+tc.label) {
			t.Fatalf("support %q: list=%q details=%q", tc.support, list, details)
		}
		if strings.Contains(list+details, "not_supported") || !strings.Contains(details, entry.Reason) {
			t.Fatal("raw enum leaked into UI or explanatory reason disappeared")
		}
		if entry.UpdateSupport != tc.support {
			t.Fatal("presentation changed the inventory enum")
		}
	}
}

func TestShellRunHelpAdvertisesBackAndPreservesDirectHelp(t *testing.T) {
	for _, state := range []string{"review", "running", "done"} {
		m := fixture(t)
		if state != "review" {
			m.run = &update.Run{}
			m.results[0].Status = update.Running
		}
		if state == "done" {
			m.done = true
			m.results[0].Status = update.Succeeded
		}
		direct := m.help(false)
		if strings.Contains(direct, "Esc") {
			t.Fatal("legacy direct-mode help changed")
		}
		m.shell = newShellState(&Shell{})
		m.shell.screen = screenRun
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 32})
		text := ansi.Strip(m.View().Content)
		expected := "Esc back"
		if state == "running" {
			expected = "Esc cancel/back"
		}
		if state == "done" {
			expected = "Enter/Esc menu"
		}
		if !strings.Contains(text, expected) || !strings.Contains(text, "q quit") {
			t.Fatalf("%s help missing navigation: %s", state, text)
		}
		for _, width := range []int{1, 2, 8, 12, 20, 38, 60, 80} {
			for _, height := range []int{1, 2, 8, 16, 32} {
				m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				text := ansi.Strip(m.View().Content)
				lines := strings.Split(text, "\n")
				if len(lines) > height {
					t.Fatalf("%s %dx%d vertical overflow", state, width, height)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("%s %dx%d horizontal overflow: %q", state, width, height, line)
					}
				}
				if !strings.Contains(text, "q") {
					t.Fatalf("%s %dx%d quit hidden", state, width, height)
				}
				if width >= 20 && height >= 2 && !strings.Contains(text, "Esc") {
					t.Fatalf("%s %dx%d Back hidden: %s", state, width, height, text)
				}
				if width >= 20 && !strings.Contains(text, "brew") {
					t.Fatalf("%s %dx%d selected task hidden: %s", state, width, height, text)
				}
			}
		}
	}
}

func TestShellRealStartupFailureSurvivesBackBrowsingAndDemo(t *testing.T) {
	for _, failure := range []string{"prepare", "engine"} {
		t.Run(failure, func(t *testing.T) {
			previousErr := errors.New("older attempt")
			prepareErr := errors.New("fixture log preparation failed")
			demoErr := errors.New("fixture demo preparation failed")
			attempts := 0
			success := fixtureExecutor(func(context.Context, process.Command, io.Writer) error { return nil })
			callbacks := &Shell{
				LoadUpgrades: func(context.Context) (Config, error) {
					cfg := planConfig("real", success)
					cfg.PrepareRun = func(context.Context) (update.Engine, error) {
						attempts++
						if attempts == 1 {
							if failure == "prepare" {
								return update.Engine{}, prepareErr
							}
							return update.Engine{Jobs: -1}, nil
						}
						return cfg.Engine, nil
					}
					return cfg, nil
				},
				LoadInventory: func(context.Context) (inventory.Report, error) { return testReport(), nil },
				LoadDemo: func(context.Context) (Config, error) {
					cfg := planConfig("demo", success)
					cfg.PrepareRun = func(context.Context) (update.Engine, error) { return update.Engine{}, demoErr }
					return cfg, nil
				},
			}
			m := shellFixture(t, callbacks)
			m.lastRealErr = previousErr
			m.lastRealResults = []update.Result{{ID: "older", Status: update.Failed}}
			deliver(t, m, press(m, tea.KeyEnter))
			if !errors.Is(m.lastRealErr, previousErr) {
				t.Fatal("review cleared previous outcome before approval")
			}
			press(m, tea.KeyEnter)
			startupErr := m.err
			if startupErr == nil || !m.done || m.run != nil {
				t.Fatal("startup fixture did not fail before creating a run")
			}
			if results, err := m.outcome(nil); !errors.Is(err, startupErr) || len(results) != 0 {
				t.Fatalf("startup outcome: results=%v err=%v", results, err)
			}
			press(m, tea.KeyEscape)
			if m.err != nil {
				t.Fatal("transient screen error was not reset")
			}
			press(m, tea.KeyDown)
			deliver(t, m, press(m, tea.KeyEnter))
			press(m, tea.KeyEnter)
			press(m, tea.KeyEscape)
			press(m, tea.KeyEscape)
			press(m, tea.KeyDown)
			deliver(t, m, press(m, tea.KeyEnter))
			press(m, tea.KeyEnter)
			if !errors.Is(m.err, demoErr) {
				t.Fatal("demo fixture did not exercise transient preparation failure")
			}
			if _, err := m.outcome(nil); !errors.Is(err, startupErr) {
				t.Fatalf("demo changed saved real startup error: %v", err)
			}
			press(m, tea.KeyEscape)
			if _, err := m.outcome(nil); !errors.Is(err, startupErr) {
				t.Fatalf("Back/browsing changed real startup error: %v", err)
			}
			press(m, tea.KeyUp)
			press(m, tea.KeyUp)
			deliver(t, m, press(m, tea.KeyEnter))
			if _, err := m.outcome(nil); !errors.Is(err, startupErr) {
				t.Fatal("reviewing a later attempt cleared prior error")
			}
			press(m, tea.KeyEnter)
			if results, err := m.outcome(nil); err != nil || len(results) != 0 {
				t.Fatal("new real approval did not clear the older outcome")
			}
			m.run.Wait()
			m.Update(pollMsg(time.Now()))
			press(m, tea.KeyEscape)
			results, err := m.outcome(nil)
			if err != nil || len(results) != 1 || results[0].Status != update.Succeeded {
				t.Fatalf("later real success did not replace startup failure: results=%v err=%v", results, err)
			}
		})
	}
}

func TestShellOutcomePreservesFatalAndExplicitCancelPriority(t *testing.T) {
	m := shellFixture(t, &Shell{})
	saved := errors.New("saved real startup error")
	transient := errors.New("demo error")
	fatal := errors.New("terminal program failure")
	m.lastRealErr = saved
	m.err = transient
	if _, err := m.outcome(nil); !errors.Is(err, saved) {
		t.Fatal("shell reported a transient demo error")
	}
	if _, err := m.outcome(fatal); !errors.Is(err, fatal) {
		t.Fatal("saved startup error hid fatal program error")
	}
	m.exitErr = context.Canceled
	if _, err := m.outcome(fatal); !errors.Is(err, context.Canceled) {
		t.Fatal("explicit quit cancellation was lost")
	}
	direct := fixture(t)
	direct.err = transient
	direct.lastRealErr = saved
	if _, err := direct.outcome(nil); !errors.Is(err, transient) {
		t.Fatal("direct-mode error behavior changed")
	}
}
