package report_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/update"
)

func TestFailurePrefersCauseAndStripsControls(t *testing.T) {
	r := update.Result{ID: "brew", Status: update.Failed, ExitCode: 1, Err: errors.New("brew: exit status 1"), LogTail: "\x1b[31mError: font-geist: It seems the Font source 'GeistVF.ttf' is not there.\x1b[0m\r\n==> Purging files\n"}
	got := report.Failure(r)
	if !strings.HasPrefix(got, "font-geist:") || strings.Contains(got, "\x1b") || strings.Contains(got, "exit status") {
		t.Fatalf("unhelpful failure: %q", got)
	}
	if !strings.Contains(report.Hint(r), "missing font") {
		t.Fatal("missing font repair hint")
	}
	r.LogTail = ""
	if strings.Contains(report.Failure(r), "brew: brew:") {
		t.Fatal("duplicated label")
	}
}

func TestTextRemovesTerminalCommands(t *testing.T) {
	got := report.Text("\x1b]52;c;secret\ahello\x1b[2J\x00\r\nworld")
	if got != "hello\nworld" {
		t.Fatalf("unsafe output: %q", got)
	}
}
