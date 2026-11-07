package ui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/buttercrab/up/internal/output"
	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/update"
	"github.com/charmbracelet/x/ansi"
)

// Keep active transfers visible while leaving space for logs and controls.
// These are upstream observations: a downloaded archive is not an installed
// package, and command success/failure remains in the task row above.
func (m *model) packageRows(r update.Result, width int) []string {
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
	rows := []string{title.Render(fitLine(r.ID+" / packages", width))}
	nameWidth := min(36, max(16, width/2-4))
	if width >= 72 {
		rows = append(rows, m.styles.muted.Render(padCell("Package", nameWidth)+"  Progress"))
	}
	for _, item := range items {
		name := item.Name
		if channel, component, ok := strings.Cut(name, " / "); ok {
			name = component + " · " + channel
		}
		if item.Version != "" {
			name += " " + item.Version
		}
		name = report.Text(name)
		status := item.Phase
		if fraction, known := item.Fraction(); known && item.Phase == "downloading" {
			filled := int(fraction * 10)
			bar := m.styles.accent.Render(strings.Repeat("━", filled)) + m.styles.muted.Render(strings.Repeat("─", 10-filled))
			status = fmt.Sprintf("%s %3.0f%%  %s", bar, fraction*100, report.Text(item.Detail))
		} else if item.Detail != "" && (item.Phase == "downloading" || item.Phase == "downloaded") {
			status += "  " + report.Text(item.Detail)
		}
		if item.Phase == "script failed" {
			status = m.styles.bad.Render(status)
		}
		if width >= 72 {
			names := wrapped(name, nameWidth)
			rows = append(rows, fitLine(padCell(names[0], nameWidth)+"  "+status, width))
			for _, extra := range names[1:] {
				rows = append(rows, m.styles.muted.Render(fitLine(extra, width)))
			}
		} else {
			rows = append(rows, wrapped(name, width)...)
			rows = append(rows, fitLine("  "+status, width))
		}
	}
	return append(rows, "")
}

func padCell(text string, width int) string {
	text = fitLine(text, width)
	return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}

func progressActivity(r update.Result) string {
	downloading, installing := 0, 0
	for _, item := range r.Progress {
		switch item.Phase {
		case "downloading":
			downloading++
		case "installing", "unpacking", "extracting", "upgrading":
			installing++
		}
	}
	var parts []string
	if downloading > 0 {
		parts = append(parts, fmt.Sprintf("%d downloading", downloading))
	}
	if installing > 0 {
		parts = append(parts, fmt.Sprintf("%d installing", installing))
	}
	return strings.Join(parts, ", ")
}
