package process

import (
	"io"
	"testing"
)

func TestPromptsAcrossWritesAndTrailingNewline(t *testing.T) {
	for _, parts := range [][]string{
		{"==> Do you want to proceed with the upgrade? [y/n]\r\n"},
		{"[sudo] Pass", "word:", " \r\n"},
		{"Continue? \x1b[33m[Y/n]\x1b[0m\n"},
	} {
		s := &TerminalSession{output: io.Discard}
		for _, p := range parts {
			if _, err := s.Write([]byte(p)); err != nil {
				t.Fatal(err)
			}
		}
		if !s.NeedsInput() {
			t.Fatalf("prompt not detected: %q", parts)
		}
		s.DismissPrompt()
		if s.NeedsInput() {
			t.Fatal("dismissed prompt reattached")
		}
	}
	s := &TerminalSession{output: io.Discard}
	_, _ = s.Write([]byte("Downloading package 1 of 2\n"))
	if s.NeedsInput() {
		t.Fatal("ordinary output treated as prompt")
	}
}
