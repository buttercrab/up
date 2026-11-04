// Package updaters discovers supported tools without executing package managers.
package updaters

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/buttercrab/up/internal/update"
	"github.com/buttercrab/up/internal/updaters/brew"
	"github.com/buttercrab/up/internal/updaters/npm"
	"github.com/buttercrab/up/internal/updaters/rustup"
)

var ErrSelection = errors.New("invalid updater selection")

type Selection struct {
	Plans   []update.Plan
	Skipped []string
}

type entry struct {
	id      string
	updater update.Updater
}

func Discover(ctx context.Context, only, exclude string) (Selection, error) {
	return discover(ctx, only, exclude, exec.LookPath)
}

func discover(ctx context.Context, only, exclude string, lookup func(string) (string, error)) (Selection, error) {
	entries := []entry{{"brew", brew.Updater{}}, {"rustup", rustup.Updater{}}, {"npm", npm.Updater{}}}
	parse := func(value string) (map[string]bool, error) {
		names := make(map[string]bool)
		if value == "" {
			return names, nil
		}
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			known := false
			for _, e := range entries {
				if e.id == name {
					known = true
				}
			}
			if !known {
				return nil, fmt.Errorf("%w: unknown updater %q (choose brew,rustup,npm)", ErrSelection, name)
			}
			names[name] = true
		}
		return names, nil
	}
	requested, err := parse(only)
	if err != nil {
		return Selection{}, err
	}
	excluded, err := parse(exclude)
	if err != nil {
		return Selection{}, err
	}
	for name := range requested {
		if excluded[name] {
			return Selection{}, fmt.Errorf("%w: %s is both selected and excluded", ErrSelection, name)
		}
	}
	var result Selection
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return Selection{}, err
		}
		if excluded[e.id] || len(requested) > 0 && !requested[e.id] {
			continue
		}
		path, err := lookup(e.id)
		if err != nil {
			if requested[e.id] {
				return Selection{}, fmt.Errorf("requested updater %s is not installed or executable", e.id)
			}
			result.Skipped = append(result.Skipped, e.id)
			continue
		}
		plan, err := e.updater.Plan(ctx)
		if err != nil {
			return Selection{}, err
		}
		for i := range plan.Commands {
			plan.Commands[i].Path = path
		}
		managed := homebrewManaged(path)
		if e.id == "npm" {
			// npm can live outside Homebrew while still using Homebrew's node.
			if node, err := lookup("node"); err == nil {
				managed = managed || homebrewManaged(node)
			}
		}
		if managed && e.id != "brew" {
			plan.Resources = append(plan.Resources, "homebrew")
			if e.id == "rustup" {
				plan.Commands[0].Args = append(plan.Commands[0].Args, "--no-self-update")
			}
		}
		result.Plans = append(result.Plans, plan)
	}
	return result, nil
}

func homebrewManaged(path string) bool {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	path = filepath.ToSlash(filepath.Clean(path))
	return strings.Contains(path, "/Cellar/") || strings.Contains(path, "/Caskroom/")
}
