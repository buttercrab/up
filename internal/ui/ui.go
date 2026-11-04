// Package ui renders execution state and owns terminal interaction, while the
// update engine remains responsible for command execution and cancellation.
package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/update"
)

type Config struct {
	Plans     []update.Plan
	Engine    update.Engine
	Terminal  *process.TerminalManager
	AutoStart bool
	Skipped   []string
	Demo      bool
}

func Show(ctx context.Context, input io.Reader, output io.Writer, cfg Config) ([]update.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, cancel, cfg)
	_, err := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler()).Run()
	cancel()
	if m.run != nil {
		m.results = m.run.Wait()
	}
	if err == nil {
		err = m.err
	}
	return m.results, err
}

type pollMsg time.Time
type handoffMsg struct{ err error }

type model struct {
	ctx                       context.Context
	cancel                    context.CancelFunc
	cfg                       Config
	run                       *update.Run
	results                   []update.Result
	err                       error
	selected                  int
	width, height             int
	done, stopping, attaching bool
	note                      string
	follow                    bool
	spinner                   spinner.Model
	bar                       progress.Model
	logs                      viewport.Model
}

var (
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B7CF8"))
	muted  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C879A"))
	good   = lipgloss.NewStyle().Foreground(lipgloss.Color("#29B998"))
	bad    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F06A83"))
	warn   = lipgloss.NewStyle().Foreground(lipgloss.Color("#DDAE52"))
	title  = lipgloss.NewStyle().Bold(true)
)

func newModel(ctx context.Context, cancel context.CancelFunc, cfg Config) *model {
	s := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(accent))
	m := &model{ctx: ctx, cancel: cancel, cfg: cfg, width: 80, height: 28, follow: true,
		spinner: s, bar: progress.New(progress.WithColors(lipgloss.Color("#29B998"), lipgloss.Color("#8B7CF8")), progress.WithoutPercentage()),
		logs: viewport.New(),
	}
	for _, p := range cfg.Plans {
		m.results = append(m.results, update.Result{ID: p.ID, Status: update.Queued, ExitCode: -1})
	}
	m.resize()
	return m
}

func poll() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return pollMsg(t) })
}

func (m *model) Init() tea.Cmd {
	if m.cfg.AutoStart {
		m.start()
	}
	return tea.Batch(poll(), m.spinner.Tick)
}

func (m *model) start() {
	if m.run != nil || m.done {
		return
	}
	m.run, m.err = m.cfg.Engine.Start(m.ctx, m.cfg.Plans)
	if m.err != nil {
		m.done = true
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		if m.cfg.Terminal != nil {
			m.cfg.Terminal.Resize(msg.Width, msg.Height)
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			if m.run == nil || m.done {
				return m, tea.Quit
			}
			m.stopping = true
			m.cancel()
		case "enter":
			if m.done {
				return m, tea.Quit
			}
			m.start()
		case "j", "down":
			m.selected = min(m.selected+1, max(0, len(m.results)-1))
			m.follow = true
			m.refreshLogs()
		case "k", "up":
			m.selected = max(0, m.selected-1)
			m.follow = true
			m.refreshLogs()
		case "i":
			if cmd := m.handoff(m.selected); cmd != nil {
				return m, cmd
			}
			m.note = "No running terminal for the selected task."
		case "pgup", "pgdown", "home":
			m.follow = false
			m.logs, _ = m.logs.Update(msg)
		case "end":
			m.follow = true
			m.logs.GotoBottom()
		}
	case handoffMsg:
		m.attaching = false
		if msg.err != nil {
			m.note = "Terminal handoff: " + report.Text(msg.err.Error())
		} else {
			m.note = "Returned to dashboard."
		}
	case pollMsg:
		if m.run != nil {
			m.results = m.run.Snapshot()
			select {
			case <-m.run.Done():
				m.done = true
				m.results = m.run.Wait()
			default:
			}
			m.refreshLogs()
		}
		if m.ctx.Err() != nil {
			m.stopping = true
			if m.run == nil || m.done {
				return m, tea.Quit
			}
		}
		if !m.attaching && m.cfg.Terminal != nil {
			for i, r := range m.results {
				if session := m.cfg.Terminal.Session(r.ID); session != nil && session.NeedsInput() {
					m.selected = i
					return m, tea.Batch(poll(), m.handoff(i))
				}
			}
		}
		return m, poll()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) handoff(index int) tea.Cmd {
	if m.cfg.Terminal == nil || index >= len(m.results) || m.attaching {
		return nil
	}
	session := m.cfg.Terminal.Session(m.results[index].ID)
	if session == nil {
		return nil
	}
	// Avoid an automatic retry loop if releasing the user's terminal fails.
	session.DismissPrompt()
	m.attaching = true
	return tea.Exec(&terminalExec{ctx: m.ctx, manager: m.cfg.Terminal, session: session}, func(err error) tea.Msg { return handoffMsg{err} })
}

func (m *model) resize() {
	w := max(10, min(m.width-6, 110))
	m.bar.SetWidth(max(8, w-22))
	m.logs.SetWidth(max(8, w-4))
	m.logs.SetHeight(max(2, m.height-18-min(len(m.results), 6)))
	if len(m.cfg.Skipped) > 0 {
		m.logs.SetHeight(max(2, m.logs.Height()-1))
	}
	m.refreshLogs()
}

func (m *model) refreshLogs() {
	if len(m.results) == 0 {
		return
	}
	r := m.results[m.selected]
	text := strings.TrimSpace(report.Text(r.LogTail))
	if text == "" {
		text = "Waiting for command output…"
	}
	text = ansi.Wrap(text, max(8, m.logs.Width()), " ")
	offset := m.logs.YOffset()
	m.logs.SetContent(text)
	if m.follow {
		m.logs.GotoBottom()
	} else {
		m.logs.SetYOffset(offset)
	}
}

func (m *model) View() tea.View {
	w := max(10, min(m.width-6, 110))
	stage := "Review your upgrades"
	if m.run != nil {
		stage = "Upgrading your tools"
	}
	if m.done {
		stage = "Run complete"
	}
	if m.stopping && !m.done {
		stage = "Stopping commands…"
	}
	header := accent.Bold(true).Render("↑ up") + "  " + title.Render(stage)
	if m.cfg.Demo {
		header = warn.Bold(true).Render("DEMO") + "  " + header
	}
	header = ansi.Truncate(header, w, "…")
	var rows []string
	finished, succeeded, failed, cancelled, running := 0, 0, 0, 0, 0
	start := max(0, m.selected-5)
	for i, r := range m.results {
		icon, status, color := "○", string(r.Status), muted
		switch r.Status {
		case update.Running:
			running++
			icon, color = m.spinner.View(), accent
			if m.cfg.Terminal != nil {
				if session := m.cfg.Terminal.Session(r.ID); session != nil && session.NeedsInput() {
					status, color = "needs input", warn
				}
			}
		case update.Succeeded:
			icon, color = "✓", good
			succeeded++
			finished++
		case update.Failed:
			icon, color = "✕", bad
			failed++
			finished++
		case update.Cancelled:
			icon, color = "−", warn
			finished++
			cancelled++
		}
		if r.Status == update.Queued && m.run != nil {
			if resource := m.waitingFor(i); resource != "" {
				status = "waiting for " + resource
			}
		}
		if i < start || i >= start+6 {
			continue
		}
		cursor := " "
		if i == m.selected {
			cursor = accent.Render("›")
		}
		elapsed := ""
		if !r.StartedAt.IsZero() {
			end := r.FinishedAt
			if end.IsZero() {
				end = time.Now()
			}
			elapsed = end.Sub(r.StartedAt).Round(time.Second).String()
		}
		row := fmt.Sprintf("%s %s  %-14s %s  %s", cursor, icon, report.Text(r.ID), color.Render(status), muted.Render(elapsed))
		rows = append(rows, ansi.Truncate(row, w, "…"))
	}
	percent := float64(finished) / float64(max(1, len(m.results)))
	jobs := m.cfg.Engine.Jobs
	if jobs == 0 {
		jobs = 4
	}
	jobs = min(jobs, max(1, len(m.results)))
	taskWord, workerWord := "tasks", "workers"
	if len(m.results) == 1 {
		taskWord = "task"
	}
	if jobs == 1 {
		workerWord = "worker"
	}
	subtitle := muted.Render(fmt.Sprintf("%d %s · %d %s", len(m.results), taskWord, jobs, workerWord))
	if m.run != nil {
		subtitle += muted.Render(fmt.Sprintf(" · %d running", running))
	}
	content := header + "\n" + subtitle + "\n\n"
	if len(m.cfg.Skipped) > 0 {
		content += muted.Render(ansi.Truncate("Not installed: "+strings.Join(m.cfg.Skipped, ", "), w, "…")) + "\n"
	}
	content += m.bar.ViewAs(percent) + muted.Render(fmt.Sprintf("  %d/%d finished", finished, len(m.results))) + "\n\n"
	content += strings.Join(rows, "\n") + "\n\n"
	if m.run == nil && !m.done {
		var commands []string
		if len(m.cfg.Plans) > 0 {
			for _, c := range m.cfg.Plans[m.selected].Commands {
				commands = append(commands, report.Text(c.Path+" "+strings.Join(c.Args, " ")))
			}
		}
		content += title.Render("Planned command") + "\n" + ansi.Wrap(strings.Join(commands, "\n"), w, " ") + "\n\n"
		if m.cfg.Demo {
			content += warn.Render("Enter starts a simulation. No tools will run.")
		} else {
			content += muted.Render("Enter starts these upgrades. Nothing has run yet.")
		}
	} else if len(m.results) > 0 {
		r := m.results[m.selected]
		if r.Err != nil {
			content += bad.Render(ansi.Truncate(report.Failure(r), w, "…")) + "\n"
			if hint := report.Hint(r); hint != "" {
				content += muted.Render(ansi.Truncate(hint, w, "…")) + "\n"
			}
		}
		label := "Live log · " + report.Text(r.ID)
		if !m.follow {
			label += " · scrollback"
		}
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#526078")).Width(w-2).Padding(0, 1)
		content += box.Render(title.Render(label) + "\n" + m.logs.View())
		if r.LogPath != "" {
			content += "\n" + muted.Render(ansi.Truncate("Full log: "+r.LogPath, w, "…"))
		}
	}
	if m.err != nil {
		content += "\n" + bad.Render(ansi.Truncate(report.Text(m.err.Error()), w, "…"))
	}
	if m.done {
		content += "\n" + good.Render(fmt.Sprintf("%d succeeded", succeeded)) + " · " + bad.Render(fmt.Sprintf("%d failed", failed))
		if cancelled > 0 {
			content += " · " + warn.Render(fmt.Sprintf("%d cancelled", cancelled))
		}
	}
	if m.note != "" {
		content += "\n" + warn.Render(ansi.Truncate(m.note, w, "…"))
	}
	help := "↑/↓ select  ·  Enter start  ·  q quit"
	if m.run != nil && !m.done {
		help = "↑/↓ select  ·  i terminal  ·  PgUp logs  ·  End follow  ·  q stop"
	}
	if m.done {
		help = "↑/↓ select  ·  PgUp/PgDn logs  ·  Enter/q close"
	}
	content += "\n\n" + muted.Render(ansi.Wrap(help, w, " "))
	if m.width < 38 || m.height < 16 {
		content = header + "\n" + strings.Join(rows, "\n") + "\n" + ansi.Wrap("Resize for logs · Enter start/close · q stop/quit", max(8, m.width-2), " ")
	}
	view := tea.NewView(lipgloss.NewStyle().Padding(1, 2).Render(content))
	view.AltScreen = true
	return view
}

func (m *model) waitingFor(index int) string {
	if index >= len(m.cfg.Plans) {
		return ""
	}
	for i, r := range m.results {
		if r.Status != update.Running || i >= len(m.cfg.Plans) {
			continue
		}
		for _, resource := range m.cfg.Plans[index].Resources {
			for _, active := range m.cfg.Plans[i].Resources {
				if resource == active {
					return resource
				}
			}
		}
	}
	return ""
}

type terminalExec struct {
	ctx     context.Context
	manager *process.TerminalManager
	session *process.TerminalSession
	input   io.Reader
	output  io.Writer
}

func (e *terminalExec) SetStdin(r io.Reader)  { e.input = r }
func (e *terminalExec) SetStdout(w io.Writer) { e.output = w }
func (*terminalExec) SetStderr(io.Writer)     {}
func (e *terminalExec) Run() error            { return e.manager.Attach(e.ctx, e.session, e.input, e.output) }
