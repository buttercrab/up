// Package report presents execution failures without duplicating process labels.
package report

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/buttercrab/up/internal/update"
	"github.com/charmbracelet/x/ansi"
)

// Text removes terminal control sequences before embedding command output in UI.
func Text(s string) string {
	s = strings.ReplaceAll(ansi.Strip(s), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}

func Failure(r update.Result) string {
	if r.Status == update.Cancelled {
		return "Cancelled by user."
	}
	lines := strings.Split(Text(r.LogTail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(strings.ToLower(line), "error:") {
			cause := strings.TrimSpace(line[len("error:"):])
			if strings.Contains(strings.ToLower(cause), "font source") && strings.Contains(strings.ToLower(cause), "not there") {
				name, _, _ := strings.Cut(cause, ":")
				return name + ": expected font file is missing."
			}
			return cause
		}
	}
	if r.ExitCode > 0 {
		return fmt.Sprintf("Command exited with code %d. Inspect the live log for details.", r.ExitCode)
	}
	if r.Err != nil {
		return strings.TrimPrefix(Text(r.Err.Error()), r.ID+": ")
	}
	return ""
}

func Hint(r update.Result) string {
	text := strings.ToLower(Text(r.LogTail))
	switch {
	case strings.Contains(text, "font source") && strings.Contains(text, "not there"):
		return "Homebrew references a missing font file. Repair that cask separately before retrying."
	case strings.Contains(text, "sudo") && (strings.Contains(text, "password") || strings.Contains(text, "terminal")):
		return "Use the interactive TUI to answer installer prompts in a terminal."
	}
	return ""
}
