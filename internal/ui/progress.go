package ui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/buttercrab/up/internal/output"
	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/update"
)

// Keep active transfers visible while leaving space for logs and controls.
// These are upstream observations: a downloaded archive is not an installed
// package, and command success/failure remains in the task row above.
func (m *model) packageRows(r update.Result, width, limit int) []string {
	items := slices.Clone(r.Progress)
	active := func(p output.Item) bool {
		return p.Phase != "downloaded" && p.Phase != "installed" && p.Phase != "script finished" && p.Phase != "script failed"
	}
	slices.SortStableFunc(items, func(a, b output.Item) int {
		if active(a) == active(b) {
			return 0
		}
		if active(a) {
			return -1
		}
		return 1
	})
	label := "Package progress · " + r.ID
	if r.Status != update.Running {
		label += " · last reported"
	}
	rows := []string{title.Render(fitLine(label, width))}
	for _, item := range items[:min(limit, len(items))] {
		name := item.Name
		if channel, component, ok := strings.Cut(name, " / "); ok {
			name = component + " · " + channel
		}
		name = fitLine(report.Text(name), max(12, width/3))
		status := item.Phase
		if fraction, known := item.Fraction(); known && item.Phase == "downloading" {
			filled := int(fraction * 10)
			status = fmt.Sprintf("%s%s %3.0f%% · %s", strings.Repeat("━", filled), strings.Repeat("─", 10-filled), fraction*100, item.Detail)
		} else if item.Detail != "" && (item.Phase == "downloading" || item.Phase == "downloaded") {
			status += " · " + item.Detail
		}
		color := m.styles.accent
		if item.Phase == "script failed" {
			color = m.styles.bad
		}
		rows = append(rows, fitLine("  "+name+"  "+color.Render(report.Text(status)), width))
	}
	if len(items) > limit {
		rows = append(rows, m.styles.muted.Render(fitLine(fmt.Sprintf("  +%d more · p shows all progress", len(items)-limit), width)))
	}
	return append(rows, "")
}
