package app_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buttercrab/up/internal/app"
)

func TestCLIReadOnlyModes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "brew"), []byte("#!/bin/sh\nexit 91\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 0, "Preview only"},
		{[]string{"--plain"}, 0, "Preview only"},
		{[]string{"--dry-run", "--yes"}, 0, `"upgrade" "--no-ask"`},
		{[]string{"--only=unknown"}, 2, "unknown updater"},
		{[]string{"--only=npm"}, 1, "not installed"},
		{[]string{"--only=brew", "--exclude=brew"}, 2, "both selected and excluded"},
		{[]string{"--demo", "--dry-run"}, 0, "no installed tools will be changed"},
		{[]string{"--demo", "--only=brew"}, 2, "cannot be combined"},
		{[]string{"--version"}, 0, "test-version"},
		{[]string{"--help"}, 0, "Usage: up"},
		{[]string{"--jobs=0"}, 2, "--jobs >= 1"},
		{[]string{"unexpected"}, 2, "no positional arguments"},
		{[]string{"--unknown"}, 2, "flag provided but not defined"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := app.Run(context.Background(), tc.args, &stdout, &stderr, "test-version")
			if code != tc.code || !strings.Contains(stdout.String()+stderr.String(), tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}
