//go:build darwin || linux

package app_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buttercrab/up/internal/app"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestAppHelper(t *testing.T) {
	if os.Getenv("UP_APP_HELPER") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(app.Run(ctx, strings.Fields(os.Getenv("UP_APP_ARGS")), os.Stdout, os.Stderr, "test"))
}

func TestFakeBrew(t *testing.T) {
	if os.Getenv("UP_FAKE_BREW") != "1" {
		return
	}
	id := os.Getenv("UP_FIXTURE_TOOL")
	if id == "" {
		id = "brew"
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if id == "brew" && len(args) == 1 && args[0] == "--prefix" {
		fmt.Println(filepath.Join(os.Getenv("HOME"), "brew-prefix"))
		os.Exit(0)
	}
	if id == "npm" && len(args) == 2 && args[0] == "prefix" && args[1] == "--global" {
		fmt.Println(filepath.Join(os.Getenv("HOME"), "npm-prefix"))
		os.Exit(0)
	}
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "invoked"), []byte("yes"), 0o600)
	fmt.Println("Fetching fixture packages…")
	switch os.Getenv("UP_FAKE_MODE") {
	case "parallel":
		id := os.Getenv("UP_FIXTURE_TOOL")
		_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "started-"+id), []byte("yes"), 0o600)
		fmt.Printf("%s fixture live\n", id)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "release")); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(6)
	case "password":
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		fmt.Fprint(tty, "Password:")
		password, err := term.ReadPassword(int(tty.Fd()))
		if err != nil || string(password) != "fixture-secret" {
			os.Exit(5)
		}
		fmt.Println("\nauthenticated")
	case "failure":
		fmt.Fprintln(os.Stderr, "Error: font-geist: It seems the Font source 'GeistVF.ttf' is not there.")
		os.Exit(1)
	case "cancel":
		fmt.Println("Sleeping fixture")
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

type terminalCapture struct {
	mu   sync.Mutex
	text strings.Builder
}

func (c *terminalCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.Write(p)
}
func (c *terminalCapture) String() string { c.mu.Lock(); defer c.mu.Unlock(); return c.text.String() }

func TestInteractiveCLI(t *testing.T) {
	for _, mode := range []string{"review", "password", "failure", "cancel", "autostart", "parallel", "demo"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			// This executable only dispatches another test helper. It cannot run
			// the host's package manager or perform actual privileged operations.
			script := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestFakeBrew$ -- \"$@\"\n"
			if err := os.WriteFile(filepath.Join(dir, "brew"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if mode == "parallel" {
				for _, id := range []string{"brew", "rustup", "npm"} {
					fixture := "#!/bin/sh\nexport UP_FIXTURE_TOOL=" + id + "\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestFakeBrew$ -- \"$@\"\n"
					if err := os.WriteFile(filepath.Join(dir, id), []byte(fixture), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestAppHelper$")
			cmd.Env = append(os.Environ(), "UP_APP_HELPER=1", "UP_FAKE_BREW=1", "UP_FAKE_MODE="+mode, "PATH="+dir, "HOME="+dir, "XDG_CACHE_HOME="+dir, "TERM=xterm-256color", "COLORTERM=truecolor", "GORACE=atexit_sleep_ms=0")
			cmd.Env = append(cmd.Env, "NO_COLOR=0", "UP_APP_ARGS=")
			if mode == "autostart" {
				cmd.Env = append(cmd.Env, "UP_APP_ARGS=--yes")
			}
			if mode == "demo" {
				cmd.Env = append(cmd.Env, "UP_APP_ARGS=--demo")
			}
			master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 32})
			if err != nil {
				t.Fatal(err)
			}
			var capture terminalCapture
			readDone := make(chan struct{})
			go func() { _, _ = io.Copy(&capture, master); close(readDone) }()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			finished := false
			t.Cleanup(func() {
				if !finished {
					_ = cmd.Process.Signal(os.Interrupt)
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						_ = cmd.Process.Kill()
					}
				}
				_ = master.Close()
				<-readDone
			})
			waitText := func(text string) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if strings.Contains(ansi.Strip(capture.String()), text) {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("missing %q in terminal output:\n%q", text, capture.String())
			}
			if mode != "autostart" {
				if mode != "demo" {
					waitText("Review and run updates")
					if _, err := os.Stat(filepath.Join(dir, "Library")); !os.IsNotExist(err) {
						t.Fatal("home menu created execution logs")
					}
					_, _ = master.Write([]byte("\r"))
				}
				waitText("Review upgrades")
				if _, err := os.Stat(filepath.Join(dir, "invoked")); !os.IsNotExist(err) {
					t.Fatal("review executed brew")
				}
			}
			if mode == "review" {
				_, _ = master.Write([]byte("q"))
			} else if mode == "autostart" {
				waitText("Finished")
				_, _ = master.Write([]byte("q"))
			} else {
				_, _ = master.Write([]byte("\r"))
				if mode == "parallel" {
					// None of these fixtures can finish until all three are observed
					// running; a serial implementation will time out here.
					for _, id := range []string{"brew", "rustup", "npm"} {
						deadline := time.Now().Add(5 * time.Second)
						for {
							if _, err := os.Stat(filepath.Join(dir, "started-"+id)); err == nil {
								break
							}
							if time.Now().After(deadline) {
								t.Fatalf("%s did not start concurrently", id)
							}
							time.Sleep(10 * time.Millisecond)
						}
					}
					// The renderer updates the count's digit in place, so raw
					// output will not contain a fresh "3 running" string. Check
					// each live task row as well as the start barrier above.
					for _, id := range []string{"brew", "rustup", "npm"} {
						waitText(fmt.Sprintf("%-14s running", id))
					}
					waitText("brew fixture live")
					_, _ = master.Write([]byte("j"))
					waitText("rustup fixture live")
					_, _ = master.Write([]byte("j"))
					// The unboxed log can reuse the previous row's suffix using
					// delete-character controls. A resize asks the real renderer
					// for a full frame before asserting captured log text.
					if err := pty.Setsize(master, &pty.Winsize{Cols: 99, Rows: 32}); err != nil {
						t.Fatal(err)
					}
					waitText("npm fixture live")
					if err := os.WriteFile(filepath.Join(dir, "release"), []byte("yes"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "password" {
					waitText("Interactive terminal")
					_, _ = master.Write([]byte("fixture-secret\r"))
					waitText("authenticated")
				}
				if mode == "cancel" {
					waitText("Sleeping fixture")
					_, _ = master.Write([]byte("q"))
				} else {
					waitText("Finished")
					_, _ = master.Write([]byte("q"))
				}
			}
			var exitErr error
			select {
			case exitErr = <-done:
				finished = true
			case <-time.After(5 * time.Second):
				t.Fatal("CLI did not exit")
			}
			want := 0
			if mode == "failure" {
				want = 1
			}
			if mode == "cancel" {
				want = 130
			}
			got := 0
			if exitErr != nil {
				if e, ok := exitErr.(*exec.ExitError); ok {
					got = e.ExitCode()
				} else {
					t.Fatal(exitErr)
				}
			}
			if got != want {
				t.Fatalf("exit=%d want=%d output=%q", got, want, capture.String())
			}
			if strings.Contains(capture.String(), "fixture-secret") {
				t.Fatal("password was echoed")
			}
			if !strings.Contains(capture.String(), "\x1b[?1049l") {
				t.Fatal("alternate screen was not restored")
			}
			if !strings.Contains(capture.String(), "38;2;") {
				t.Fatal("true-color theme was not rendered")
			}
			if mode == "failure" && !strings.Contains(capture.String(), "expected font file is missing") {
				t.Fatal("missing actionable cause")
			}
			if mode == "demo" {
				if !strings.Contains(capture.String(), "demo") {
					t.Fatal("simulation not labeled")
				}
				if _, err := os.Stat(filepath.Join(dir, "invoked")); !os.IsNotExist(err) {
					t.Fatal("demo invoked a real adapter")
				}
			}
			if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err == nil && strings.HasSuffix(path, ".log") {
					data, e := os.ReadFile(path)
					if e != nil {
						return e
					}
					if strings.Contains(string(data), "fixture-secret") {
						t.Error("password in full log")
					}
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
