package ui

import (
	"fmt"
	"strings"

	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/update"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) failureSummary(width int) string {
	var names []string
	for _, result := range m.results {
		if result.Status == update.Failed {
			names = append(names, report.Text(result.ID))
		}
	}
	if len(names) == 0 {
		return ""
	}
	if width < 24 {
		return m.styles.bad.Render(fitLine(fmt.Sprintf("! %d failed", len(names)), width))
	}
	label := fmt.Sprintf("! %d failed", len(names))
	if width >= 48 {
		label += " · " + strings.Join(names, ", ")
	}
	// Reserve the action before shortening names: the warning is navigable.
	action := " · f inspect"
	return m.styles.bad.Render(fitLine(label, max(0, width-ansi.StringWidth(action)))) + action
}

// Failures are announced without moving the current view. Only an explicit
// keypress changes selection, so a background failure cannot steal scrollback.
func (m *model) nextFailure() {
	for step := 1; step <= len(m.results); step++ {
		index := (m.selected + step) % len(m.results)
		if m.results[index].Status != update.Failed {
			continue
		}
		m.selected = index
		m.showProgress = false
		m.follow = true
		m.refreshLogs()
		return
	}
}
