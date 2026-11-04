//go:build darwin || linux

package app_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// CSI-u makes Escape unambiguous when several key events share one PTY read.
const escapeKey = "\x1b[27u"

type shellFixture struct {
	t        *testing.T
	dir      string
	master   *os.File
	cmd      *exec.Cmd
	done     chan error
	readDone chan struct{}
	capture  terminalCapture
	finished bool
}

func startShell(t *testing.T, mode, args string) *shellFixture {
	t.Helper()
	f := &shellFixture{t: t, dir: t.TempDir(), done: make(chan error, 1)}
	brew := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestFakeBrew$ -- \"$@\"\n"
	uv := "#!/bin/sh\nprintf executed > \"$HOME/unsupported-executed\"\nexit 91\n"
	for name, body := range map[string]string{"brew": brew, "uv": uv} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.cmd = exec.Command(os.Args[0], "-test.run=^TestAppHelper$")
	f.cmd.Env = append(os.Environ(), "UP_APP_HELPER=1", "UP_FAKE_BREW=1", "UP_FAKE_MODE="+mode, "UP_APP_ARGS="+args, "PATH="+f.dir, "HOME="+f.dir, "XDG_CACHE_HOME="+f.dir, "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=0", "GORACE=atexit_sleep_ms=0")
	var err error
	f.master, err = pty.StartWithSize(f.cmd, &pty.Winsize{Cols: 100, Rows: 32})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	f.readDone = readDone
	go func() { _, _ = io.Copy(&f.capture, f.master); close(readDone) }()
	go func() { f.done <- f.cmd.Wait() }()
	t.Cleanup(func() {
		if !f.finished {
			_ = f.cmd.Process.Signal(os.Interrupt)
			select {
			case <-f.done:
			case <-time.After(5 * time.Second):
				_ = f.cmd.Process.Kill()
			}
		}
		_ = f.master.Close()
		<-readDone
	})
	return f
}

func (f *shellFixture) send(keys string) {
	f.t.Helper()
	if _, err := f.master.Write([]byte(keys)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *shellFixture) wait(text string) {
	f.waitSince(text, 0)
}

func (f *shellFixture) waitSince(text string, offset int) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(ansi.Strip(f.capture.String()[offset:]), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("missing %q in terminal output: %q", text, f.capture.String())
}

func (f *shellFixture) noExecution() {
	f.t.Helper()
	for _, name := range []string{"invoked", "unsupported-executed", "Library", "up"} {
		if _, err := os.Stat(filepath.Join(f.dir, name)); !os.IsNotExist(err) {
			f.t.Fatalf("browsing created %s: %v", name, err)
		}
	}
}

func (f *shellFixture) finish(want int) {
	f.t.Helper()
	f.send("q")
	f.exited(want, true)
}

func (f *shellFixture) exited(want int, terminal bool) {
	f.t.Helper()
	var err error
	select {
	case err = <-f.done:
		f.finished = true
	case <-time.After(5 * time.Second):
		f.t.Fatal("session did not exit")
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			f.t.Fatal(err)
		}
	}
	if code != want {
		f.t.Fatalf("exit=%d want=%d, output=%q", code, want, f.capture.String())
	}
	select {
	case <-f.readDone:
	case <-time.After(time.Second):
		f.t.Fatal("terminal output did not close")
	}
	if terminal && !strings.Contains(f.capture.String(), "\x1b[?1049l") {
		f.t.Fatal("terminal not restored")
	}
}

func TestShellBrowseInventoryAndDemo(t *testing.T) {
	f := startShell(t, "shell", "")
	f.wait("Choose a feature")
	f.noExecution()
	f.send("\x1b[B\r") // Inventory, using the arrow key requested by the user.
	f.wait("Inventory · read-only")
	f.noExecution()
	f.send("\x1b[B\r") // uv details; unsupported detector must remain read-only.
	f.wait("Update adapter is not implemented.")
	f.noExecution()
	f.send(escapeKey + escapeKey) // Details -> Inventory -> Home.
	f.send("\x1b[B\r")            // Home remembers Inventory, so Down opens Demo.
	f.wait("Review your upgrades")
	f.noExecution()
	f.send("\r")
	f.wait("Run complete")
	if _, err := os.Stat(filepath.Join(f.dir, "invoked")); !os.IsNotExist(err) {
		t.Fatal("demo invoked installed updater")
	}
	f.send(escapeKey)
	f.finish(0)
}

func TestShellBackAfterCancellationKeepsOtherFeaturesUsable(t *testing.T) {
	f := startShell(t, "cancel", "")
	f.wait("Choose a feature")
	f.send("\r")
	f.wait("Review your upgrades")
	f.noExecution()
	f.send("\r")
	f.wait("Sleeping fixture")
	beforeBack := len(f.capture.String())
	f.send(escapeKey)
	f.waitSince("Choose a feature", beforeBack)
	// Opening Inventory after cleanup proves the application's parent context
	// is still live; navigating during the stopping screen is intentionally ignored.
	f.send("\x1b[B\r")
	f.wait("Inventory · read-only")
	f.finish(130)
}

func TestInventoryShortcutUsesSameTUI(t *testing.T) {
	f := startShell(t, "shell", "inventory")
	f.wait("Inventory · read-only")
	f.noExecution()
	f.send("\r")
	f.wait("Executable:")
	f.noExecution()
	f.finish(0)
}

func TestShellStartupFailureSurvivesInventoryBrowsing(t *testing.T) {
	f := startShell(t, "shell", "")
	f.wait("Choose a feature")
	// Block both native cache layouts without relying on Unix permission bits.
	for _, name := range []string{"Library", "up"} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.send("\r")
	f.wait("Review your upgrades")
	f.send("\r")
	f.wait("create logs directory")
	beforeBack := len(f.capture.String())
	f.send(escapeKey)
	f.waitSince("Choose a feature", beforeBack)
	f.send("\x1b[B\r")
	f.wait("Inventory · read-only")
	f.finish(1)
	if _, err := os.Stat(filepath.Join(f.dir, "invoked")); !os.IsNotExist(err) {
		t.Fatal("failed startup executed an updater")
	}
}

func TestInteractiveInvalidSelectorsKeepCLIExitCode(t *testing.T) {
	for _, args := range []string{"--only=unknown", "--only=brew --exclude=brew", "--only=npm"} {
		t.Run(args, func(t *testing.T) {
			f := startShell(t, "shell", args)
			want := 2
			if args == "--only=npm" {
				want = 1
			}
			f.exited(want, false)
			f.noExecution()
			if strings.Contains(f.capture.String(), "Choose a feature") {
				t.Fatal("invalid explicit selector opened menu")
			}
		})
	}
}
