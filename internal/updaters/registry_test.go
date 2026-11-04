package updaters

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"testing"
)

func tools(paths map[string]string) func(string) (string, error) {
	return func(id string) (string, error) {
		if path, ok := paths[id]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestNativeCommandsAndIndependentResources(t *testing.T) {
	s, err := discover(context.Background(), "", "", tools(map[string]string{"brew": "/tools/brew", "rustup": "/tools/rustup", "npm": "/tools/npm", "node": "/tools/node"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Plans) != 3 {
		t.Fatalf("plans=%+v", s)
	}
	want := [][]string{{"upgrade", "--no-ask"}, {"update"}, {"update", "--global"}}
	resources := map[string]bool{}
	for i, p := range s.Plans {
		if !reflect.DeepEqual(p.Commands[0].Args, want[i]) {
			t.Fatalf("%s args=%v", p.ID, p.Commands[0].Args)
		}
		for _, r := range p.Resources {
			if resources[r] {
				t.Fatalf("independent tools share %s", r)
			}
			resources[r] = true
		}
	}
}

func TestDiscoverySelectionAndMissingTools(t *testing.T) {
	lookup := tools(map[string]string{"brew": "/tools/brew", "npm": "/tools/npm"})
	s, err := discover(context.Background(), "", "", lookup)
	if err != nil || len(s.Plans) != 2 || !slices.Equal(s.Skipped, []string{"rustup"}) {
		t.Fatalf("selection=%+v err=%v", s, err)
	}
	s, err = discover(context.Background(), "npm", "", lookup)
	if err != nil || len(s.Plans) != 1 || s.Plans[0].ID != "npm" {
		t.Fatalf("selection=%+v err=%v", s, err)
	}
	s, err = discover(context.Background(), "", "brew", lookup)
	if err != nil || len(s.Plans) != 1 || s.Plans[0].ID != "npm" {
		t.Fatalf("excluded selection=%+v err=%v", s, err)
	}
	if _, err := discover(context.Background(), "rustup", "", lookup); err == nil || errors.Is(err, ErrSelection) {
		t.Fatalf("missing requested tool: %v", err)
	}
	for _, args := range [][2]string{{"unknown", ""}, {"brew", "brew"}, {"", "unknown"}, {"brew,", ""}} {
		if _, err := discover(context.Background(), args[0], args[1], lookup); !errors.Is(err, ErrSelection) {
			t.Fatalf("invalid selection %v: %v", args, err)
		}
	}
	s, err = discover(context.Background(), "", "", tools(nil))
	if err != nil || len(s.Plans) != 0 || len(s.Skipped) != 3 {
		t.Fatalf("empty discovery=%+v err=%v", s, err)
	}
}

func TestHomebrewRuntimeConflictsAreSerialized(t *testing.T) {
	s, err := discover(context.Background(), "", "", tools(map[string]string{
		"brew":   "/opt/homebrew/bin/brew",
		"rustup": "/opt/homebrew/Cellar/rustup/1/bin/rustup",
		"npm":    "/home/user/npm/bin/npm",
		"node":   "/opt/homebrew/Cellar/node/1/bin/node",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Plans {
		if !slices.Contains(p.Resources, "homebrew") {
			t.Fatalf("%s could race Homebrew", p.ID)
		}
	}
	if !slices.Contains(s.Plans[1].Commands[0].Args, "--no-self-update") {
		t.Fatal("Homebrew-managed rustup can self-update")
	}
}
