package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buttercrab/up/internal/app"
	"github.com/buttercrab/up/internal/inventory"
)

func inventoryFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Unsupported tools must be detected without executing them. Supported
	// tools may receive only the narrowly scoped metadata query.
	brew := "#!/bin/sh\nif [ \"$1\" = --prefix ]; then printf '%s\\n' \"$HOME/brew-prefix\"; exit 0; fi\nprintf invoked > \"$HOME/unexpected-execution\"\nexit 91\n"
	uv := "#!/bin/sh\nprintf invoked > \"$HOME/unexpected-execution\"\nexit 91\n"
	for name, body := range map[string]string{"brew": brew, "uv": uv} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	return dir
}

func TestInventoryJSONIsReadOnlyAndVersioned(t *testing.T) {
	dir := inventoryFixture(t)
	var stdout, stderr bytes.Buffer
	if code := app.Run(context.Background(), []string{"inventory", "--json"}, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code=%d err=%s", code, &stderr)
	}
	var result inventory.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, &stdout)
	}
	if result.SchemaVersion != 1 || result.Scope != "current_path" {
		t.Fatalf("unexpected contract: %+v", result)
	}
	byID := map[string]inventory.Installation{}
	for _, item := range result.Installations {
		byID[item.ID] = item
	}
	if byID["brew"].UpdateSupport != inventory.Supported || byID["uv"].UpdateSupport != inventory.Unsupported {
		t.Fatalf("wrong support status: %+v", byID)
	}
	if len(result.Installations) != 2 || len(result.Missing) == 0 {
		t.Fatalf("wrong detection: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "unexpected-execution")); !os.IsNotExist(err) {
		t.Fatalf("inventory executed an upgrade or unsupported tool: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Library")); !os.IsNotExist(err) {
		t.Fatal("inventory created execution logs")
	}
}

func TestInventoryPlainAndInvalidOptions(t *testing.T) {
	inventoryFixture(t)
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"inventory"}, 0, "not supported"},
		{[]string{"inventory", "--help"}, 0, "Usage: up inventory"},
		{[]string{"inventory", "--yes"}, 2, "flag provided but not defined"},
		{[]string{"inventory", "extra"}, 2, "no positional arguments"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := app.Run(context.Background(), tc.args, &stdout, &stderr, "test")
			if code != tc.code || !strings.Contains(stdout.String()+stderr.String(), tc.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
			}
		})
	}
}

func TestInventoryCancellationDoesNotEmitPartialJSON(t *testing.T) {
	inventoryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := app.Run(ctx, []string{"inventory", "--json"}, &stdout, &stderr, "test"); code != 130 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
	}
}
