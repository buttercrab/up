//go:build darwin || linux

package app_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buttercrab/up/internal/app"
)

func TestExecutionWithFakeBrew(t *testing.T) {
	for _, exit := range []int{0, 7} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			dir := t.TempDir()
			// The CLI finds only this harmless fixture, never the host's brew.
			script := fmt.Sprintf("#!/bin/sh\nprintf 'fake upgrade output\\n'\nexit %d\n", exit)
			if err := os.WriteFile(filepath.Join(dir, "brew"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("HOME", dir)
			t.Setenv("XDG_CACHE_HOME", dir)
			var stdout, stderr bytes.Buffer
			code := app.Run(context.Background(), []string{"--yes"}, &stdout, &stderr, "test")
			wantCode, wantStatus := 0, "succeeded"
			if exit != 0 {
				wantCode, wantStatus = 1, "failed"
			}
			if code != wantCode || !strings.Contains(stdout.String(), "brew: "+wantStatus) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "fake upgrade output") {
				t.Fatalf("live command output missing: %q", stdout.String())
			}
			var logCount int
			err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if filepath.Ext(path) == ".log" {
					logCount++
					data, err := os.ReadFile(path)
					if err != nil || string(data) != "fake upgrade output\n" {
						t.Errorf("log=%q err=%v", data, err)
					}
				}
				return nil
			})
			if err != nil || logCount != 1 {
				t.Fatalf("expected one full log: count=%d err=%v", logCount, err)
			}
		})
	}
}
