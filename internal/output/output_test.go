package output_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/buttercrab/up/internal/output"
)

func TestRedrawsAndChunkBoundaries(t *testing.T) {
	// Homebrew uses column zero, erase-line and cursor-previous-line to redraw
	// a group of downloads. Rustup uses cursor-up and carriage return.
	stream := "Checking…\r\n⠋ Bottle llvm (23.1.5) Downloading 66.0MB/457.3MB\x1b[K\r\n" +
		"⠋ Cask visual-studio-code (1.145.0) Downloading 15.7MB/356.2MB\x1b[K\x1b[1F" +
		"\x1b[?2026h⠙ Bottle llvm (23.1.5) Downloading 100.0MB/457.3MB\x1b[K\r\n" +
		"✔︎ Cask visual-studio-code (1.145.0) Downloaded 356.2MB/356.2MB\x1b[K\x1b[?2026l"
	for _, chunk := range []int{1, 2, 7, len(stream)} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			tracker := output.NewTracker("brew")
			screen := output.NewScreen(tracker.Observe)
			for start := 0; start < len(stream); start += chunk {
				screen.Feed([]byte(stream[start:min(start+chunk, len(stream))]))
			}
			text := screen.Text()
			if strings.Count(text, "Bottle llvm") != 1 || strings.Contains(text, "66.0MB") || strings.Contains(text, "15.7MB") || strings.ContainsAny(text, "\x1b\x00") {
				t.Fatalf("bad redraw: %q", text)
			}
			items := tracker.Items()
			if len(items) != 2 || items[0].Name != "llvm" || items[0].Current != 100e6 || items[1].Phase != "downloaded" {
				t.Fatalf("bad observations: %+v", items)
			}
			if f, known := items[0].Fraction(); !known || f < 0.21 || f > 0.22 {
				t.Fatalf("fraction: %f %v", f, known)
			}
			items[0].Name = "mutated"
			if tracker.Items()[0].Name != "llvm" {
				t.Fatal("snapshot aliases tracker")
			}
		})
	}
}

func TestImplicitWrapsDoNotCombineConcurrentRustupRows(t *testing.T) {
	for _, chunk := range []int{1, 17, 10000} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			tracker := output.NewTracker("rustup")
			s := output.NewScreen(tracker.Observe)
			s.SetWidth(100)
			row := func(name, phase string) string {
				return fmt.Sprintf("%-100s", name+" "+phase+" [# ] 25.61 MiB (80 MiB/s, ETA: 0s)")
			}
			stream := "\r" + row("rustc", "downloading") + "\r" + row("cargo", "downloading") + row("rustc", "downloading") + "\x1b[1A\r" + row("cargo", "installed") + row("rustc", "installed") + "\r\n"
			for pos := 0; pos < len(stream); pos += chunk {
				s.Feed([]byte(stream[pos:min(pos+chunk, len(stream))]))
			}
			s.Flush()
			items := tracker.Items()
			if len(items) != 2 {
				t.Fatalf("partial redraw invented items: %+v", items)
			}
			for _, item := range items {
				if item.Phase != "installed" || strings.Count(item.Detail, "ETA:") != 1 || strings.Contains(item.Detail, "downloading") {
					t.Fatalf("combined row: %+v", item)
				}
			}
			if strings.Count(s.Text(), "rustc") != 1 || strings.Count(s.Text(), "cargo") != 1 {
				t.Fatalf("repeated rows: %q", s.Text())
			}
		})
	}
}

func TestWrapInsideIdentityDoesNotInventPackages(t *testing.T) {
	for _, tc := range []struct {
		tool, text string
		width      int
	}{
		{"brew", "==> Installing visual-studio-code\r\n", 20},
		{"rustup", "info: downloading component 'rust-std-wasm32-wasip2'\r\n", 32},
		{"rustup", "rust-std-wasm32-unknown-unknown downloading [# ] 1 MiB\r\n", 20},
	} {
		t.Run(tc.tool+fmt.Sprint(tc.width), func(t *testing.T) {
			tr := output.NewTracker(tc.tool)
			s := output.NewScreen(tr.Observe)
			s.SetWidth(tc.width)
			for _, b := range []byte(tc.text) {
				s.Feed([]byte{b})
			}
			s.Flush()
			if len(tr.Items()) != 0 {
				t.Fatalf("invented identity from fragment: %+v", tr.Items())
			}
			if s.Text() == "" {
				t.Fatal("discarded unrecognized output")
			}
		})
	}
}

func TestCarriageReturnsControlsAndBounds(t *testing.T) {
	s := output.NewScreen(nil)
	s.Feed([]byte("very long status\r\x1b[2Kdone\x1b]52;c;secret\a\x00"))
	if s.Text() != "done" {
		t.Fatalf("unsafe/duplicated text: %q", s.Text())
	}
	s.Feed([]byte("\r\x1b[0G界a\rX"))
	if strings.ContainsAny(s.Text(), "\x00\x1b") {
		t.Fatal("wide cell placeholder leaked")
	}
	s.Feed([]byte(strings.Repeat("row\n", 500)))
	if strings.Count(s.Text(), "\n") > 255 {
		t.Fatal("unbounded rows")
	}
	s.Feed([]byte(strings.Repeat("a", 10000)))
	last := strings.Split(s.Text(), "\n")
	if len(last[len(last)-1]) > 512 {
		t.Fatal("unbounded columns")
	}
}

func TestResizeAndHostileCursorParametersRemainBounded(t *testing.T) {
	s := output.NewScreen(nil)
	s.Feed([]byte("old projection"))
	s.SetWidth(20)
	s.Feed([]byte("new\r\x1b[999999999Gx\x1b[999999999Bz\x1b[999999999Fok"))
	text := s.Text()
	if strings.Contains(text, "old projection") || strings.Count(text, "\n") > 255 {
		t.Fatalf("resize/bounds: %q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if len(line) > 20 {
			t.Fatalf("cursor escaped width: %q", line)
		}
	}
}

func TestRustupConcurrentAndOlderFormats(t *testing.T) {
	tracker := output.NewTracker("rustup")
	s := output.NewScreen(tracker.Observe)
	s.Feed([]byte("info: syncing channel updates for nightly-aarch64-apple-darwin\r\n" +
		"rustc downloading [#              ] 25.61 MiB (80.58 MiB/s, ETA: 0s)\r\n" +
		"rust-std-wasm32-wasip2 downloading [##             ] 24.42 MiB (98.28 MiB/s, ETA: 0s)\r"))
	items := tracker.Items()
	if len(items) != 2 || !strings.HasSuffix(items[1].Name, " / rust-std-wasm32-wasip2") || items[0].Current <= 0 {
		t.Fatalf("lost concurrent components: %+v", items)
	}
	if _, known := items[0].Fraction(); known {
		t.Fatal("invented a byte total from hash marks")
	}
	s.Feed([]byte("\ninfo: downloading component 'cargo'\n3.4 MiB / 6.8 MiB (50 %) 100 KiB/s\r"))
	items = tracker.Items()
	if f, known := items[2].Fraction(); !known || f != 0.5 {
		t.Fatalf("old rustup progress: %+v", items)
	}
	s.Feed([]byte("\ninfo: installing component 'cargo'\n"))
	if tracker.Items()[2].Phase != "installing" || tracker.Items()[2].Total != 0 {
		t.Fatal("download percentage leaked into install phase")
	}
}

func TestUnknownOutputAndMetadataAreNotPackages(t *testing.T) {
	for _, tool := range []string{"brew", "rustup", "npm", "other"} {
		tr := output.NewTracker(tool)
		for _, line := range []string{"Downloading updates", "50%", "npm WARN deprecated abc", "added 10 packages in 3s", "✔︎ Bottle Manifest llvm (23.1.5) Downloaded 1KB/1KB", "==> Upgrading 3 outdated packages:", "==> Installing dependencies for llvm: libfoo"} {
			tr.Observe(line)
		}
		if len(tr.Items()) != 0 {
			t.Fatalf("%s invented package progress: %+v", tool, tr.Items())
		}
	}
	tr := output.NewTracker("npm")
	tr.Observe("npm info run @scope/pkg@1.2.3 postinstall node_modules/@scope/pkg node setup.js")
	items := tr.Items()
	if len(items) != 1 || items[0].Name != "@scope/pkg" || items[0].Version != "1.2.3" {
		t.Fatalf("npm package: %+v", items)
	}
	if _, known := items[0].Fraction(); known {
		t.Fatal("invented npm percentage")
	}
}

func TestBrewUnknownTotalKeepsTransferredBytes(t *testing.T) {
	tr := output.NewTracker("brew")
	tr.Observe("⠋ Bottle llvm (23.1.5) Downloading 12.8MB/-------")
	items := tr.Items()
	if len(items) != 1 || items[0].Current != 12.8e6 || items[0].Detail != "12.8 MB" {
		t.Fatalf("missing unknown-total counter: %+v", items)
	}
	if _, known := items[0].Fraction(); known {
		t.Fatal("invented unknown byte total")
	}
}

func TestNPMLifecycleCompletionIncludingFailures(t *testing.T) {
	for _, tc := range []struct{ record, phase string }{
		{"{ code: 0, signal: null }", "script finished"},
		{"{ code: 1, signal: null }", "script failed"},
		{"{ code: null, signal: 'SIGTERM' }", "script failed"},
	} {
		tr := output.NewTracker("npm")
		tr.Observe("npm info run @scope/pkg@1.0 install node_modules/@scope/pkg node install.js")
		tr.Observe("npm info run @scope/pkg@1.0 install " + tc.record)
		if items := tr.Items(); len(items) != 1 || items[0].Phase != tc.phase {
			t.Fatalf("%s: %+v", tc.record, items)
		}
	}
}
