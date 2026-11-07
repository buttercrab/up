package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/buttercrab/up/internal/inventory"
	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/update"
)

// Shell loads features without executing updates. Load callbacks must honor
// cancellation. PrepareRun on a returned Config defers log creation until the
// user approves its review screen. Loaded AutoStart and Shell fields are ignored.
type Shell struct {
	LoadUpgrades   func(context.Context) (Config, error)
	LoadDemo       func(context.Context) (Config, error)
	LoadInventory  func(context.Context) (inventory.Report, error)
	StartInventory bool
}

type screen uint8

const (
	screenHome screen = iota
	screenLoading
	screenRun
	screenInventory
	screenDetails
	screenLoadError
)

type loadKind uint8

const (
	loadUpgrades loadKind = iota
	loadInventory
	loadDemo
)

var featureNames = []string{"Upgrades", "Inventory", "Demo"}

type loadedMsg struct {
	request   uint64
	kind      loadKind
	config    Config
	inventory inventory.Report
	err       error
}

type inventoryItem struct {
	installed *inventory.Installation
	missing   *inventory.MissingTool
}

func (i inventoryItem) id() string {
	if i.installed != nil {
		return i.installed.ID
	}
	return i.missing.ID
}
func (i inventoryItem) name() string {
	if i.installed != nil {
		return i.installed.Name
	}
	return i.missing.Name
}

type shellState struct {
	callbacks   *Shell
	screen      screen
	menu        int
	request     uint64
	cancelLoad  context.CancelFunc
	loading     loadKind
	returnTo    screen
	loadErr     error
	report      inventory.Report
	items       []inventoryItem
	selected    int
	details     viewport.Model
	loadMu      sync.Mutex
	loadClosed  bool
	activeLoads sync.WaitGroup
}

// Admission and shutdown share the same lock: a queued tea.Cmd can never add
// work after Wait begins, while a started callback is joined through cleanup.
func (s *shellState) startLoader() bool {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	if s.loadClosed {
		return false
	}
	s.activeLoads.Add(1)
	return true
}

func (m *model) stopLoaders() {
	if m.shell == nil {
		return
	}
	s := m.shell
	s.loadMu.Lock()
	s.loadClosed = true
	s.loadMu.Unlock()
	s.request++
	if s.cancelLoad != nil {
		s.cancelLoad()
	}
	s.activeLoads.Wait()
}

func newShellState(callbacks *Shell) *shellState {
	return &shellState{callbacks: callbacks, screen: screenHome, details: viewport.New()}
}

func (m *model) beginLoad(kind loadKind, returnTo screen) tea.Cmd {
	s := m.shell
	if s.cancelLoad != nil {
		s.cancelLoad()
	}
	s.request++
	request := s.request
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancelLoad = cancel
	s.loading = kind
	s.returnTo = returnTo
	s.loadErr = nil
	s.screen = screenLoading
	callbacks := s.callbacks
	return func() tea.Msg {
		msg := loadedMsg{request: request, kind: kind}
		if !s.startLoader() {
			msg.err = context.Canceled
			return msg
		}
		defer s.activeLoads.Done()
		if ctx.Err() != nil {
			msg.err = ctx.Err()
			return msg
		}
		switch kind {
		case loadUpgrades:
			if callbacks.LoadUpgrades == nil {
				msg.err = errors.New("Upgrades are unavailable")
			} else {
				msg.config, msg.err = callbacks.LoadUpgrades(ctx)
			}
		case loadDemo:
			if callbacks.LoadDemo == nil {
				msg.err = errors.New("Demo is unavailable")
			} else {
				msg.config, msg.err = callbacks.LoadDemo(ctx)
			}
		case loadInventory:
			if callbacks.LoadInventory == nil {
				msg.err = errors.New("Inventory is unavailable")
			} else {
				msg.inventory, msg.err = callbacks.LoadInventory(ctx)
			}
		}
		if ctx.Err() != nil && msg.err == nil {
			msg.err = ctx.Err()
		}
		return msg
	}
}

func (m *model) loaded(msg loadedMsg) {
	s := m.shell
	if s == nil || s.screen != screenLoading || msg.request != s.request {
		return
	}
	if s.cancelLoad != nil {
		s.cancelLoad()
		s.cancelLoad = nil
	}
	if msg.err != nil {
		s.loadErr = msg.err
		s.screen = screenLoadError
		return
	}
	if msg.kind == loadInventory {
		selectedID := ""
		if len(s.items) > 0 {
			selectedID = s.items[s.selected].id()
		}
		s.report = msg.inventory
		s.items = nil
		for i := range s.report.Installations {
			s.items = append(s.items, inventoryItem{installed: &s.report.Installations[i]})
		}
		for i := range s.report.Missing {
			s.items = append(s.items, inventoryItem{missing: &s.report.Missing[i]})
		}
		s.selected = 0
		for i, item := range s.items {
			if item.id() == selectedID {
				s.selected = i
				break
			}
		}
		if s.returnTo == screenDetails && len(s.items) > 0 {
			s.screen = screenDetails
		} else {
			s.screen = screenInventory
		}
		return
	}
	if len(msg.config.Plans) == 0 {
		s.loadErr = errors.New("No supported upgrades were detected. Open Inventory to inspect installations and adapter support.")
		s.screen = screenLoadError
		return
	}
	m.resetRun()
	m.cfg = msg.config
	m.cfg.Shell = nil
	m.cfg.AutoStart = false
	m.cfg.Demo = msg.kind == loadDemo
	if m.cfg.Terminal != nil {
		m.cfg.Terminal.Resize(m.width, m.height)
	}
	for _, p := range m.cfg.Plans {
		m.results = append(m.results, update.Result{ID: p.ID, Status: update.Queued, ExitCode: -1})
	}
	s.screen = screenRun
	m.resize()
}

func (m *model) resetRun() {
	if m.cancelRun != nil {
		m.cancelRun()
		m.cancelRun = nil
	}
	m.run = nil
	m.runCtx = nil
	m.results = nil
	m.err = nil
	m.selected = 0
	m.done = false
	m.stopping = false
	m.attaching = false
	m.note = ""
	m.follow = true
	m.quitAfterRun = false
	m.backAfterRun = false
	m.resultsSaved = false
	m.logs = viewport.New()
}

func (m *model) rememberRun() {
	if m.shell != nil && m.run != nil && !m.cfg.Demo && !m.resultsSaved {
		m.lastRealResults = append([]update.Result(nil), m.results...)
		m.lastRealErr = nil
		m.resultsSaved = true
	}
}

func (m *model) rememberStartupError() {
	if m.shell != nil && !m.cfg.Demo {
		m.lastRealErr = m.err
	}
}

func (m *model) home() {
	s := m.shell
	if s.cancelLoad != nil {
		s.cancelLoad()
		s.cancelLoad = nil
	}
	s.request++ // late replies must not replace a later screen
	s.loadErr = nil
	s.screen = screenHome
	m.resetRun()
	m.cfg = Config{}
}

func (m *model) shellKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	s := m.shell
	key := msg.String()
	if key == "q" || key == "ctrl+c" {
		if s.cancelLoad != nil {
			s.cancelLoad()
			s.cancelLoad = nil
			s.request++
		}
		if key == "ctrl+c" {
			m.exitErr = context.Canceled
		}
		if m.run != nil && !m.done {
			m.exitErr = context.Canceled
			m.quitAfterRun = true
			m.stopping = true
			if m.cancelRun != nil {
				m.cancelRun()
			}
			return true, nil
		}
		return true, tea.Quit
	}
	if s.screen == screenRun {
		if key == "esc" {
			if m.run == nil || m.done {
				m.home()
			} else {
				m.backAfterRun = true
				m.stopping = true
				if m.cancelRun != nil {
					m.cancelRun()
				}
			}
			return true, nil
		}
		if key == "enter" && m.done {
			m.home()
			return true, nil
		}
		return false, nil
	}
	if key == "esc" {
		switch s.screen {
		case screenDetails:
			s.screen = screenInventory
		case screenLoading, screenLoadError:
			previous := s.returnTo
			if s.cancelLoad != nil {
				s.cancelLoad()
				s.cancelLoad = nil
			}
			s.request++
			s.loadErr = nil
			if previous == screenInventory || previous == screenDetails {
				s.screen = previous
			} else {
				m.home()
			}
		case screenInventory:
			m.home()
		}
		return true, nil
	}
	switch s.screen {
	case screenHome:
		switch key {
		case "down", "j":
			s.menu = min(s.menu+1, len(featureNames)-1)
		case "up", "k":
			s.menu = max(0, s.menu-1)
		case "enter":
			return true, m.beginLoad(loadKind(s.menu), screenHome)
		}
	case screenLoading:
		// Loading is cancelable; repeated Enter does not launch duplicate requests.
	case screenLoadError:
		if key == "r" || key == "enter" {
			return true, m.beginLoad(s.loading, s.returnTo)
		}
	case screenInventory:
		switch key {
		case "down", "j":
			s.selected = min(s.selected+1, max(0, len(s.items)-1))
		case "up", "k":
			s.selected = max(0, s.selected-1)
		case "enter":
			if len(s.items) > 0 {
				s.details.GotoTop()
				s.screen = screenDetails
			}
		case "r":
			return true, m.beginLoad(loadInventory, screenInventory)
		}
	case screenDetails:
		if key == "r" {
			return true, m.beginLoad(loadInventory, screenDetails)
		}
		s.details, _ = s.details.Update(msg)
	}
	return true, nil
}

func safe(text string) string { return strings.ReplaceAll(report.Text(text), "\t", "    ") }

func shellBodyRoom(height int) int {
	if height >= 8 {
		return height - 4
	}
	return height - 2
}

// shellFrame reserves controls before content, including at tiny sizes. The
// selected row is the first body row supplied for compact/one-line fallbacks.
func (m *model) shellFrame(width, height int, heading string, body []string, help string) []string {
	if height == 1 {
		item := ""
		if len(body) > 0 {
			item = ansi.Strip(body[0])
		}
		if width > 2 {
			return []string{fitLine(item, width-2) + " q"}
		}
		return []string{"q"}
	}
	footer := fitLine(help, width)
	if width < 7 {
		footer = "q"
	} else if width < 20 {
		footer = "Esc/q"
		if strings.HasPrefix(help, "Enter") {
			footer = "Enter/q"
		}
	} else if ansi.StringWidth(help) > width {
		suffix := " · q quit"
		core := strings.TrimSuffix(help, suffix)
		footer = fitLine(core, width-ansi.StringWidth(suffix)) + suffix
	}
	if height == 2 {
		item := ""
		if len(body) > 0 {
			item = body[0]
		}
		return []string{fitLine(item, width), m.styles.muted.Render(footer)}
	}
	lines := []string{title.Render(fitLine(heading, width))}
	room := shellBodyRoom(height)
	if height >= 8 {
		lines = append(lines, "")
	}
	lines = append(lines, limitLines(body, room, width)...)
	if height >= 8 {
		lines = append(lines, "")
	}
	return append(lines, m.styles.muted.Render(footer))
}

func (m *model) shellView(width, height int) []string {
	s := m.shell
	switch s.screen {
	case screenHome:
		rows := make([]string, 0, 3)
		limit := min(3, max(1, height-2))
		first := max(0, min(s.menu-limit+1, 3-limit))
		for i := first; i < first+limit; i++ {
			cursor := "  "
			if i == s.menu {
				cursor = m.styles.accent.Render("› ")
			}
			line := cursor + featureNames[i]
			if width >= 64 {
				descriptions := []string{"Review and run updates", "Installed tools and owners", "Try a simulated update run"}
				line = cursor + padCell(featureNames[i], 14) + m.styles.muted.Render(descriptions[i])
			}
			rows = append(rows, line)
		}
		if height <= 2 {
			rows = []string{featureNames[s.menu]}
		}
		if height >= 9 && width < 64 {
			descriptions := []string{"Review and run updates", "Installed tools and owners (read-only)", "Try a simulated update run"}
			rows = append(rows, "", m.styles.muted.Render(fitLine(descriptions[s.menu], width)))
		}
		return m.shellFrame(width, height, "up", rows, "Enter open · ↑/↓ select · q quit")
	case screenLoading:
		line := m.spinner.View() + " Loading " + featureNames[int(s.loading)] + "…"
		return m.shellFrame(width, height, "up", []string{line}, "Esc back · q quit")
	case screenLoadError:
		body := wrapped(m.styles.bad.Render(safe(s.loadErr.Error())), width)
		return m.shellFrame(width, height, featureNames[int(s.loading)]+" unavailable", body, "r retry · Esc back · q quit")
	case screenInventory:
		room := shellBodyRoom(height)
		if height >= 6 {
			room--
		} // scope note
		rows := m.inventoryRows(width, max(1, room))
		if len(rows) == 0 {
			rows = []string{"No installations or missing tools were reported."}
		}
		if height >= 6 {
			rows = append(rows, m.styles.muted.Render(fitLine("Current PATH · read-only", width)))
		}
		return m.shellFrame(width, height, "up / inventory", rows, "Enter details · ↑/↓ select · r refresh · Esc back · q quit")
	case screenDetails:
		if len(s.items) == 0 {
			s.screen = screenInventory
			return m.shellView(width, height)
		}
		item := s.items[s.selected]
		help := "↑/↓ scroll · PgUp/PgDn · r refresh · Esc back · q quit"
		if height <= 3 {
			return m.shellFrame(width, height, "Inventory details", []string{safe(item.name())}, help)
		}
		room := shellBodyRoom(height)
		offset := s.details.YOffset()
		s.details.SetWidth(width)
		s.details.SetHeight(room)
		s.details.SetContent(strings.Join(wrapped(m.inventoryDetails(item), width), "\n"))
		s.details.SetYOffset(offset)
		body := strings.Split(s.details.View(), "\n")
		return m.shellFrame(width, height, "up / inventory / "+safe(item.name()), body, help)
	}
	return nil
}

func (m *model) inventoryRows(width, limit int) []string {
	s := m.shell
	limit = min(limit, len(s.items))
	if limit == 0 {
		return nil
	}
	first := max(0, min(s.selected-limit+1, len(s.items)-limit))
	rows := make([]string, 0, limit)
	for i := first; i < first+limit; i++ {
		item := s.items[i]
		cursor := "  "
		if i == s.selected {
			cursor = m.styles.accent.Render("› ")
		}
		status, color := "missing", m.styles.muted
		if item.installed != nil {
			status = supportLabel(item.installed.UpdateSupport)
			color = m.styles.muted
			if item.installed.UpdateSupport != inventory.Supported {
				color = m.styles.warn
			}
		}
		name := safe(item.name())
		if width >= 48 {
			rows = append(rows, fitLine(cursor+padCell(name, min(24, width/2))+"  "+color.Render(status), width))
		} else {
			rows = append(rows, fitLine(cursor+name+" · "+status, width))
		}
	}
	return rows
}

func supportLabel(support inventory.Support) string {
	switch support {
	case inventory.Supported:
		return "supported"
	case inventory.Unsupported:
		return "no updater"
	case inventory.Blocked:
		return "blocked"
	default:
		return "unknown"
	}
}

func (m *model) inventoryDetails(item inventoryItem) string {
	var lines []string
	add := func(label, value string) {
		if value == "" {
			value = "unknown"
		}
		lines = append(lines, label+": "+safe(value))
	}
	add("Name", item.name())
	add("ID", item.id())
	if item.missing != nil {
		add("Status", "missing from current PATH")
		add("Commands checked", strings.Join(item.missing.Commands, ", "))
		add("Owner", "unknown (tool missing)")
		add("Prefix", "unknown (tool missing)")
		add("Upgrade support", "not evaluated (tool missing)")
	} else {
		i := item.installed
		add("Status", "installed")
		add("Upgrade support", supportLabel(i.UpdateSupport))
		add("Owner", i.Owner)
		prefix := "unknown"
		if i.PrefixKnown {
			prefix = i.Prefix
		}
		add("Prefix", prefix)
		add("Executable", i.Executable)
		add("Resolved executable", i.ResolvedExecutable)
		add("Upgrade entrypoint", i.PlannedExecutable)
		if i.Reason != "" {
			add("Reason", i.Reason)
		}
		if len(i.Resources) > 0 {
			add("Shared resources", strings.Join(i.Resources, ", "))
		}
		if len(i.Runtimes) == 0 {
			add("Runtime", "not reported")
		}
		for _, runtime := range i.Runtimes {
			lines = append(lines, "", fmt.Sprintf("Runtime · %s", safe(runtime.ID)))
			add("Runtime executable", runtime.Executable)
			add("Runtime resolved executable", runtime.ResolvedExecutable)
			add("Runtime owner", runtime.Owner)
		}
	}
	lines = append(lines, "", "Scope: current PATH detection only. This is not a package list or a check for available upgrades.")
	return strings.Join(lines, "\n")
}
