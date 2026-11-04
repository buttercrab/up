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
	styles                    palette
}

type palette struct{ accent, muted, good, bad, warn, border lipgloss.Style }

var title = lipgloss.NewStyle().Bold(true)

func (m *model) setPalette(dark bool) {
	colors := []string{"#8B7CF8", "#7C879A", "#29B998", "#F06A83", "#DDAE52", "#526078"}
	if !dark {
		colors = []string{"#5940B5", "#586174", "#087C62", "#B92E4B", "#8A5900", "#68748A"}
	}
	style := func(i int) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(colors[i])) }
	m.styles = palette{style(0), style(1), style(2), style(3), style(4), style(5)}
	m.spinner.Style = m.styles.accent
	m.bar = progress.New(progress.WithColors(lipgloss.Color(colors[2]), lipgloss.Color(colors[0])), progress.WithoutPercentage())
}

func newModel(ctx context.Context, cancel context.CancelFunc, cfg Config) *model {
	s := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	m := &model{ctx: ctx, cancel: cancel, cfg: cfg, width: 80, height: 28, follow: true,
		spinner: s,
		logs:    viewport.New(),
	}
	m.setPalette(true)
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
	return tea.Batch(poll(), m.spinner.Tick, tea.RequestBackgroundColor)
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
	case tea.BackgroundColorMsg:
		if msg.Color != nil {
			m.setPalette(msg.IsDark())
		}
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
	// View allocates the viewport from the measured chrome for the current
	// state. A resize can reduce that space, including all the way to zero.
	m.logs.SetWidth(max(1, min(m.width, 110)))
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
	text = ansi.Wrap(strings.ReplaceAll(text, "\t", "    "), max(1, m.logs.Width()), " ")
	offset := m.logs.YOffset()
	m.logs.SetContent(text)
	if m.follow {
		m.logs.GotoBottom()
	} else {
		m.logs.SetYOffset(offset)
	}
}

// frameGeometry keeps outer padding inside, rather than in addition to, the
// dimensions reported by the terminal. Unknown or zero-sized windows render
// nothing; keyboard actions still work while waiting for a usable size.
func (m *model) frameGeometry() (w, h, x, y int) {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	if m.width >= 24 {
		x = 1
	}
	if m.width >= 60 {
		x = 2
	}
	if m.height >= 24 {
		y = 1
	}
	return min(m.width-2*x, 110), m.height - 2*y, x, y
}

func fitLine(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(text, width, "…")
}

func wrapped(text string, width int) []string {
	return strings.Split(ansi.Wrap(strings.ReplaceAll(text, "\t", "    "), max(1, width), " "), "\n")
}

// limitLines makes truncation visible instead of letting a long command push
// the confirmation controls beyond the bottom of the terminal.
func limitLines(lines []string, n, width int) []string {
	if n <= 0 {
		return nil
	}
	if len(lines) <= n {
		return lines
	}
	result := append([]string(nil), lines[:n]...)
	result[n-1] = fitLine(result[n-1], max(0, width-1)) + "…"
	return result
}

func (m *model) header() string {
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
	header := m.styles.accent.Bold(true).Render("↑ up") + "  " + title.Render(stage)
	if m.cfg.Demo {
		header = m.styles.warn.Bold(true).Render("DEMO") + "  " + header
	}
	return header
}

type totals struct{ finished, succeeded, failed, cancelled, running int }

func (m *model) totals() totals {
	var result totals
	for _, r := range m.results {
		switch r.Status {
		case update.Running:
			result.running++
		case update.Succeeded:
			result.succeeded++
			result.finished++
		case update.Failed:
			result.failed++
			result.finished++
		case update.Cancelled:
			result.cancelled++
			result.finished++
		}
	}
	return result
}

func (m *model) selectedResult() *update.Result {
	if m.selected < 0 || m.selected >= len(m.results) {
		return nil
	}
	return &m.results[m.selected]
}

func (m *model) taskRow(index, width int) string {
	r := m.results[index]
	icon, status, color := "○", string(r.Status), m.styles.muted
	switch r.Status {
	case update.Running:
		icon, color = m.spinner.View(), m.styles.accent
		if m.cfg.Terminal != nil {
			if s := m.cfg.Terminal.Session(r.ID); s != nil && s.NeedsInput() {
				status, color = "needs input", m.styles.warn
			}
		}
	case update.Succeeded:
		icon, color = "✓", m.styles.good
	case update.Failed:
		icon, color = "✕", m.styles.bad
	case update.Cancelled:
		icon, color = "−", m.styles.warn
	}
	if r.Status == update.Queued && m.run != nil {
		if resource := m.waitingFor(index); resource != "" {
			status = "waiting for " + resource
		}
	}
	cursor := " "
	if index == m.selected {
		cursor = m.styles.accent.Render("›")
	}
	id := fitLine(report.Text(r.ID), max(1, width/2))
	if width < 48 {
		return fitLine(fmt.Sprintf("%s %s %s %s", cursor, icon, id, color.Render(status)), width)
	}
	elapsed := ""
	if !r.StartedAt.IsZero() {
		end := r.FinishedAt
		if end.IsZero() {
			end = time.Now()
		}
		elapsed = end.Sub(r.StartedAt).Round(time.Second).String()
	}
	return fitLine(fmt.Sprintf("%s %s  %-14s %s  %s", cursor, icon, id, color.Render(status), m.styles.muted.Render(elapsed)), width)
}

func (m *model) taskRows(limit, width int) []string {
	limit = min(limit, len(m.results))
	if limit <= 0 {
		return nil
	}
	start := max(0, min(m.selected-limit+1, len(m.results)-limit))
	rows := make([]string, 0, limit)
	for i := start; i < start+limit; i++ {
		rows = append(rows, m.taskRow(i, width))
	}
	return rows
}

func (m *model) help(compact bool) string {
	if m.done {
		if compact {
			return "Enter/q close"
		}
		return "Enter/q close · ↑/↓ select · PgUp/PgDn logs"
	}
	if m.run != nil {
		if compact {
			return "q stop · i terminal"
		}
		return "q stop · i terminal · ↑/↓ select · PgUp logs · End follow"
	}
	if compact {
		return "Enter run · q quit"
	}
	return "↑/↓ select · Enter start · q quit"
}

func (m *model) compactHelp(width int) string {
	if ansi.StringWidth(m.help(true)) <= width {
		return m.help(true)
	}
	if m.run != nil && !m.done && width >= len("q stop") {
		return "q stop"
	}
	if width >= len("Enter/q") {
		return "Enter/q"
	}
	return "q"
}

func (m *model) logBox(width int, label string, body []string) []string {
	inner := max(1, width-4)
	border := m.styles.border.Render
	lines := []string{border("╭" + strings.Repeat("─", width-2) + "╮")}
	for _, line := range append([]string{title.Render(label)}, body...) {
		line = fitLine(line, inner)
		lines = append(lines, border("│")+" "+line+strings.Repeat(" ", max(0, inner-ansi.StringWidth(line)))+" "+border("│"))
	}
	return append(lines, border("╰"+strings.Repeat("─", width-2)+"╯"))
}

// layout measures the actual header, statuses, messages and footer first. Only
// the remaining rows belong to command details or the log viewport. If that
// chrome cannot fit, the caller chooses a less detailed layout.
func (m *model) layout(width, height int, compact bool) ([]string, bool) {
	count := m.totals()
	head := []string{fitLine(m.header(), width)}
	if !compact {
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
		subtitle := fmt.Sprintf("%d %s · %d %s", len(m.results), taskWord, jobs, workerWord)
		if m.run != nil {
			subtitle += fmt.Sprintf(" · %d running", count.running)
		}
		head = append(head, m.styles.muted.Render(fitLine(subtitle, width)))
		if len(m.cfg.Skipped) > 0 {
			head = append(head, m.styles.muted.Render(fitLine("Not installed: "+strings.Join(m.cfg.Skipped, ", "), width)))
		}
		suffix := fmt.Sprintf("  %d/%d finished", count.finished, len(m.results))
		m.bar.SetWidth(max(1, width-ansi.StringWidth(suffix)))
		head = append(head, "", fitLine(m.bar.ViewAs(float64(count.finished)/float64(max(1, len(m.results))))+m.styles.muted.Render(suffix), width), "")
	}
	rowLimit := 6
	if compact {
		rowLimit = max(1, min(6, height/3))
	}
	head = append(head, m.taskRows(rowLimit, width)...)
	if !compact {
		head = append(head, "")
	}
	r := m.selectedResult()
	if r != nil && r.Err != nil {
		head = append(head, m.styles.bad.Render(fitLine(report.Failure(*r), width)))
		if hint := report.Hint(*r); hint != "" && !compact {
			head = append(head, m.styles.muted.Render(fitLine(hint, width)))
		}
	}
	var tail []string
	if r != nil && r.LogPath != "" && !compact {
		tail = append(tail, m.styles.muted.Render(fitLine("Full log: "+r.LogPath, width)))
	}
	if m.err != nil {
		tail = append(tail, m.styles.bad.Render(fitLine(report.Text(m.err.Error()), width)))
	}
	if m.done {
		summary := m.styles.good.Render(fmt.Sprintf("%d succeeded", count.succeeded)) + " · " + m.styles.bad.Render(fmt.Sprintf("%d failed", count.failed))
		if count.cancelled > 0 {
			summary += " · " + m.styles.warn.Render(fmt.Sprintf("%d cancelled", count.cancelled))
		}
		tail = append(tail, fitLine(summary, width))
	}
	if m.note != "" && !compact {
		tail = append(tail, m.styles.warn.Render(fitLine(m.note, width)))
	}
	if !compact {
		tail = append(tail, "")
	}
	footer := wrapped(m.help(compact), width)
	if compact {
		footer = []string{m.compactHelp(width)}
	}
	for _, line := range footer {
		tail = append(tail, m.styles.muted.Render(line))
	}
	if m.run == nil && !m.done {
		if !compact {
			notice := "Enter starts these upgrades. Nothing has run yet."
			if m.cfg.Demo {
				notice = "Enter starts a simulation. No tools will run."
			}
			noticeLines := wrapped(notice, width)
			for i := range noticeLines {
				noticeLines[i] = m.styles.muted.Render(noticeLines[i])
			}
			tail = append(append(noticeLines, ""), tail...)
		}
		room := height - len(head) - len(tail) - 1 // command heading
		if room < 1 {
			return nil, false
		}
		var commands []string
		if m.selected >= 0 && m.selected < len(m.cfg.Plans) {
			for _, c := range m.cfg.Plans[m.selected].Commands {
				commands = append(commands, report.Text(c.Path+" "+strings.Join(c.Args, " ")))
			}
		}
		commandLines := wrapped(strings.Join(commands, "\n"), width)
		head = append(head, title.Render(fitLine("Planned command", width)))
		head = append(head, limitLines(commandLines, room, width)...)
		return append(head, tail...), true
	}
	overhead := 3 // border, log label, border
	logWidth := width - 4
	if compact {
		overhead = 1
		logWidth = width
	}
	room := height - len(head) - len(tail) - overhead
	if room < 1 || logWidth < 1 {
		return nil, false
	}
	if m.logs.Width() != logWidth || m.logs.Height() != room {
		m.logs.SetWidth(logWidth)
		m.logs.SetHeight(room)
		m.refreshLogs()
	}
	label := "Live log"
	if r != nil {
		label += " · " + report.Text(r.ID)
	}
	if !m.follow {
		label += " · scrollback"
	}
	body := strings.Split(m.logs.View(), "\n")
	if compact {
		head = append(head, title.Render(fitLine(label, width)))
		head = append(head, body...)
	} else {
		head = append(head, m.logBox(width, label, body)...)
	}
	return append(head, tail...), true
}

func (m *model) minimal(width, height int) []string {
	// At one row, keep the selected task and an action hint together when space
	// permits. A one-column terminal cannot display both; q remains available.
	action := "Enter/q"
	if m.run != nil && !m.done {
		action = "q stop"
	}
	if width < ansi.StringWidth(action) {
		action = "q"
	}
	if height == 1 {
		if r := m.selectedResult(); r != nil && width > ansi.StringWidth(action)+1 {
			return []string{fitLine(report.Text(r.ID), width-ansi.StringWidth(action)-1) + " " + action}
		}
		return []string{fitLine(action, width)}
	}
	var lines []string
	if height >= 3 {
		lines = append(lines, fitLine(m.header(), width))
	}
	if r := m.selectedResult(); r != nil {
		// Drop decorative columns before truncating the task's actual name.
		lines = append(lines, fitLine(report.Text(r.ID)+" "+string(r.Status), width))
	}
	lines = append(lines, m.styles.muted.Render(m.compactHelp(width)))
	return lines
}

func (m *model) View() tea.View {
	width, height, x, y := m.frameGeometry()
	var content string
	if width > 0 && height > 0 {
		var lines []string
		ok := false
		if width >= 32 {
			lines, ok = m.layout(width, height, false)
		}
		if !ok {
			lines, ok = m.layout(width, height, true)
		}
		if !ok {
			lines = m.minimal(width, height)
		}
		framed := make([]string, 0, len(lines)+2*y)
		for range y {
			framed = append(framed, "")
		}
		for _, line := range lines {
			framed = append(framed, strings.Repeat(" ", x)+fitLine(line, width))
		}
		for range y {
			framed = append(framed, "")
		}
		content = strings.Join(framed, "\n")
	}
	view := tea.NewView(content)
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
