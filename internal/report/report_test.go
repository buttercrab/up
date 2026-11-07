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

func TestNPMFailureNamesRegistryPackageWithoutExposingURL(t *testing.T) {
	for _, name := range []string{"prime-agent", "@scope/private-tool"} {
		for _, prefix := range []string{"npm error ", "npm ERR! "} {
			r := update.Result{ID: "npm", Status: update.Failed, ExitCode: 1, LogTail: prefix + "code E404\r\n" + prefix + "404 Not Found - GET https://registry.example/private?token=secret\r\n" + prefix + "404 The requested resource '" + name + "@*' could not be found or you do not have permission to access it.\r\n"}
			got := report.Failure(r)
			if !strings.Contains(got, name) || !strings.Contains(got, "E404") || strings.Contains(got, "secret") || strings.Contains(got, "exited with code") {
				t.Fatalf("bad diagnosis: %q", got)
			}
			if !strings.Contains(report.Hint(r), "original installer") {
				t.Fatal("missing source repair hint")
			}
		}
	}
}

func TestNPMFailuresKeepAccessAmbiguityAndCancellation(t *testing.T) {
	for _, code := range []string{"E404", "E401", "E403", "EACCES", "EPERM"} {
		r := update.Result{ID: "npm", Status: update.Failed, ExitCode: 1, LogTail: "npm error code " + code + "\n"}
		if !strings.Contains(report.Failure(r), code) || report.Hint(r) == "" {
			t.Fatalf("missing diagnosis for %s", code)
		}
		r.Status = update.Cancelled
		if report.Failure(r) != "Cancelled by user." {
			t.Fatal("npm error hid cancellation")
		}
	}
}

func TestNPMResourceURLIsNotTreatedAsPackageIdentity(t *testing.T) {
	r := update.Result{ID: "npm", Status: update.Failed, ExitCode: 1, LogTail: "npm error code E404\nnpm error 404 The requested resource 'https://secret@registry.example/pkg' could not be found\n"}
	if got := report.Failure(r); strings.Contains(got, "secret") || !strings.Contains(got, "E404") {
		t.Fatalf("unsafe diagnosis: %q", got)
	}
}

func TestNPMNestedCommandFailureExplainsSelfUpdateOrdering(t *testing.T) {
	r := update.Result{ID: "npm", Status: update.Failed, ExitCode: 127, LogTail: "npm error code 127\nnpm error sh: npm: command not found\n"}
	if !strings.Contains(report.Failure(r), "install script could not find npm") || !strings.Contains(report.Hint(r), "separately") {
		t.Fatal("missing self-update explanation")
	}
}
