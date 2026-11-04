// Package updaters discovers supported tools using read-only metadata queries.
package updaters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/buttercrab/up/internal/inventory"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
	"github.com/buttercrab/up/internal/updaters/brew"
	"github.com/buttercrab/up/internal/updaters/npm"
	"github.com/buttercrab/up/internal/updaters/rustup"
)

var ErrSelection = errors.New("invalid updater selection")

type Selection struct {
	Plans         []update.Plan
	Skipped       []string
	Installations []inventory.Installation
}

type entry struct {
	id         string
	updater    update.Updater
	definition inventory.Definition
}

func Discover(ctx context.Context, only, exclude string) (Selection, error) {
	return discover(ctx, only, exclude, discoveryDeps{lookup: exec.LookPath, query: queryPrefix})
}

// Inventory describes the first matching installation of each catalogue tool
// on the current PATH. It runs only the same bounded Homebrew/npm prefix probes
// used for upgrade planning, and never creates or executes an upgrade run.
func Inventory(ctx context.Context) (inventory.Report, error) {
	return inspectInventory(ctx, discoveryDeps{lookup: exec.LookPath, query: queryPrefix})
}

func inspectInventory(ctx context.Context, deps discoveryDeps) (inventory.Report, error) {
	selection, err := inspect(ctx, "", "", deps, false)
	if err != nil {
		return inventory.Report{}, err
	}
	report := inventory.Report{SchemaVersion: inventory.SchemaVersion, Scope: inventory.CurrentPath, Installations: []inventory.Installation{}, Missing: []inventory.MissingTool{}}
	known := make(map[string]inventory.Installation)
	for _, item := range selection.Installations {
		known[item.ID] = item
	}
	rustupPath := known["rustup"].Executable
	for _, definition := range inventory.Catalog() {
		if err := ctx.Err(); err != nil {
			return inventory.Report{}, err
		}
		if item, found := known[definition.ID]; found {
			report.Installations = append(report.Installations, item)
			continue
		}
		var path string
		if definition.UpdateSupport != inventory.Supported {
			for _, command := range definition.Commands {
				if err := ctx.Err(); err != nil {
					return inventory.Report{}, err
				}
				if found, err := deps.lookup(command); err == nil {
					path = found
					break
				}
			}
		}
		if path == "" {
			report.Missing = append(report.Missing, inventory.MissingTool{ID: definition.ID, Name: definition.Name, Commands: definition.Commands})
			continue
		}
		item := installation(definition, path)
		item.Reason = appendReason(item.Reason, "Update adapter is not implemented.")
		if definition.ID == "cargo" && sameExecutable(path, rustupPath) {
			if item.Owner == inventory.OwnerUnknown {
				item.Owner = inventory.OwnerRustup
			}
			item.Runtimes = []inventory.Runtime{runtimeInstallation("rustup", rustupPath)}
		}
		report.Installations = append(report.Installations, item)
	}
	if err := ctx.Err(); err != nil {
		return inventory.Report{}, err
	}
	return report, nil
}

func installation(definition inventory.Definition, path string) inventory.Installation {
	resolved, owner := executableIdentity(path)
	item := inventory.Installation{ID: definition.ID, Name: definition.Name, Executable: path, ResolvedExecutable: resolved, Owner: owner, UpdateSupport: definition.UpdateSupport}
	if resolved == "" {
		item.Reason = "Executable target could not be resolved."
	}
	return item
}

func runtimeInstallation(id, path string) inventory.Runtime {
	resolved, owner := executableIdentity(path)
	return inventory.Runtime{ID: id, Executable: path, ResolvedExecutable: resolved, Owner: owner}
}

func executableIdentity(path string) (string, string) {
	resolved, _ := filepath.EvalSymlinks(path)
	owner := inventory.OwnerUnknown
	if homebrewManaged(path) {
		owner = inventory.OwnerHomebrew
	}
	return resolved, owner
}

func sameExecutable(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && aInfo.Mode().IsRegular() && bInfo.Mode().IsRegular() && os.SameFile(aInfo, bInfo)
}

func appendReason(current, next string) string {
	if current == "" {
		return next
	}
	return current + " " + next
}

type discoveryDeps struct {
	lookup func(string) (string, error)
	query  func(context.Context, string, ...string) (string, error)
}

func discover(ctx context.Context, only, exclude string, deps discoveryDeps) (Selection, error) {
	return inspect(ctx, only, exclude, deps, true)
}

func supportedEntries() ([]entry, error) {
	implementations := map[string]update.Updater{"brew": brew.Updater{}, "rustup": rustup.Updater{}, "npm": npm.Updater{}}
	var entries []entry
	for _, definition := range inventory.Catalog() {
		if definition.UpdateSupport != inventory.Supported {
			continue
		}
		updater, found := implementations[definition.ID]
		if !found {
			return nil, fmt.Errorf("missing updater implementation for %s", definition.ID)
		}
		entries = append(entries, entry{id: definition.ID, updater: updater, definition: definition})
	}
	return entries, nil
}

// inspect shares the exact same metadata and safety analysis between upgrade
// planning and the read-only inventory. Only inventory may retain blocked tools.
func inspect(ctx context.Context, only, exclude string, deps discoveryDeps, strict bool) (Selection, error) {
	entries, err := supportedEntries()
	if err != nil {
		return Selection{}, err
	}
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
				return nil, fmt.Errorf("%w: unknown updater %q (choose %s)", ErrSelection, name, strings.Join(inventory.SupportedIDs(), ","))
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
		path, err := deps.lookup(e.id)
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
		result.Plans = append(result.Plans, plan)
		result.Installations = append(result.Installations, installation(e.definition, path))
	}

	paths := make(map[string]string)
	for _, p := range result.Plans {
		paths[p.ID] = p.Commands[0].Path
	}
	brewPath, brewFound := paths["brew"]
	_, npmFound := paths["npm"]
	if !brewFound && npmFound {
		brewPath, err = deps.lookup("brew")
		brewFound = err == nil
	}
	needsBrewPrefix := npmFound || !strict
	for _, p := range result.Plans {
		reference, _ := homebrewReference(p.Commands[0].Path)
		needsBrewPrefix = needsBrewPrefix || p.ID != "brew" && reference != ""
	}
	var brewPrefix string
	brewKnown := false
	if brewFound && needsBrewPrefix {
		brewPrefix, brewKnown = readPrefix(ctx, deps.query, brewPath, "--prefix")
	}
	for i := range result.Plans {
		if err := ctx.Err(); err != nil {
			return Selection{}, err
		}
		plan := &result.Plans[i]
		metadata := &result.Installations[i]
		path := plan.Commands[0].Path
		if plan.ID == "brew" {
			metadata.Owner = inventory.OwnerHomebrew
			metadata.Prefix, metadata.PrefixKnown = brewPrefix, brewKnown
			if !brewKnown {
				if needsBrewPrefix {
					metadata.Reason = "Homebrew prefix could not be determined."
				} else {
					metadata.Reason = "Homebrew prefix was not probed."
				}
			}
		}
		managed := homebrewManaged(path)
		var nodePath string
		if plan.ID == "npm" {
			// npm can live outside Homebrew while still using Homebrew's node.
			if node, err := deps.lookup("node"); err == nil {
				nodePath = node
				metadata.Runtimes = []inventory.Runtime{runtimeInstallation("node", node)}
				managed = managed || homebrewManaged(node)
			}
			prefix, known := readPrefix(ctx, deps.query, path, "prefix", "--global")
			metadata.Prefix, metadata.PrefixKnown = prefix, known
			if !known {
				metadata.Reason = "Global prefix could not be determined; Homebrew conflicts are conservatively serialized."
			}
			if nodePath == "" {
				metadata.Reason = appendReason(metadata.Reason, "Node runtime was not found on PATH.")
			}
			// A custom npm prefix can point into Homebrew independently of its
			// executable/runtime. Unknown scopes must never permit this overlap.
			managed = managed || !known || brewFound && (!brewKnown || prefixesOverlap(prefix, brewPrefix))
		}
		if err := ctx.Err(); err != nil {
			return Selection{}, err
		}
		if managed && plan.ID != "brew" {
			plan.Resources = append(plan.Resources, "homebrew")
			if plan.ID == "rustup" {
				plan.Commands[0].Args = append(plan.Commands[0].Args, "--no-self-update")
			}
		}
		metadata.Resources = append([]string(nil), plan.Resources...)
		if plan.ID != "brew" {
			_, selected := paths["brew"]
			stable, err := approvedExecutable(path, plan.ID, selected, brewPrefix, brewKnown)
			if err != nil {
				if strict {
					return Selection{}, err
				}
				metadata.UpdateSupport, metadata.Reason = inventory.Blocked, err.Error()
				continue
			}
			plan.Commands[0].Path = stable
			replacements := map[string]string{}
			if _, literal := versionedHomebrewPath(path); stable != path && literal {
				replacements[filepath.Dir(path)] = filepath.Dir(stable)
			}
			if nodePath != "" {
				stableNode, err := approvedExecutable(nodePath, "node", selected, brewPrefix, brewKnown)
				if err != nil {
					if strict {
						return Selection{}, err
					}
					metadata.UpdateSupport, metadata.Reason = inventory.Blocked, err.Error()
					continue
				}
				if stableNode != nodePath {
					replacements[filepath.Dir(nodePath)] = filepath.Dir(stableNode)
				}
			}
			plan.Commands[0].Env = stableSearchPath(plan.Commands[0].Env, replacements)
		}
		metadata.PlannedExecutable = plan.Commands[0].Path
	}
	return result, nil
}

func approvedExecutable(path, id string, brewSelected bool, brewPrefix string, brewKnown bool) (string, error) {
	versionedPath, chain := homebrewReference(path)
	if versionedPath == "" {
		return path, nil
	}
	root, _ := versionedHomebrewPath(versionedPath)
	for _, alias := range chain {
		if maintainedHomebrewAlias(alias, versionedPath, id) {
			return path, nil
		}
	}
	_, literal := versionedHomebrewPath(path)
	if !literal && id == "node" {
		if brewSelected && (!brewKnown || prefixesOverlap(root, brewPrefix)) {
			return "", errors.New("node runtime uses a custom alias into a versioned Homebrew keg; use its stable opt/bin directory on PATH or exclude brew")
		}
		return path, nil
	}
	if stable := stableHomebrewExecutable(versionedPath, id); stable != "" {
		return stable, nil
	}
	if brewSelected && (!brewKnown || prefixesOverlap(root, brewPrefix)) {
		return "", fmt.Errorf("%s uses a versioned Homebrew executable without a matching stable entry point; use its opt path or exclude brew", id)
	}
	return path, nil
}

// Preserve intermediate references: npm may link from a keg to a shared global
// file, so its final EvalSymlinks target alone can hide a soon-to-be-deleted keg.
func homebrewReference(path string) (string, []string) {
	path = filepath.Clean(path)
	var chain []string
	for range 255 {
		chain = append(chain, path)
		if _, found := versionedHomebrewPath(path); found {
			return path, chain
		}
		parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
		prefix := string(filepath.Separator)
		changed := false
		for i, part := range parts {
			prefix = filepath.Join(prefix, part)
			info, err := os.Lstat(prefix)
			if err != nil {
				return "", nil
			}
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(prefix)
			if err != nil {
				return "", nil
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(prefix), target)
			}
			path = filepath.Join(append([]string{target}, parts[i+1:]...)...)
			changed = true
			break
		}
		if !changed {
			return "", nil
		}
	}
	return "", nil
}

// Only Homebrew's own opt and prefix/bin entrypoints are expected to be
// retargeted during an upgrade. A same-file custom symlink is not that promise.
func maintainedHomebrewAlias(path, versionedPath, id string) bool {
	root, _ := versionedHomebrewPath(versionedPath)
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	path = filepath.Clean(path)
	var candidateRoot string
	if strings.HasSuffix(filepath.ToSlash(path), "/bin/"+id) {
		candidateRoot = filepath.Dir(filepath.Dir(path))
	}
	if _, tail, found := strings.Cut(filepath.ToSlash(versionedPath), "/Cellar/"); found {
		parts := strings.Split(tail, "/")
		if len(parts) >= 3 {
			suffix := "/opt/" + parts[0] + "/" + strings.Join(parts[2:], "/")
			if strings.HasSuffix(filepath.ToSlash(path), suffix) {
				candidateRoot = strings.TrimSuffix(filepath.ToSlash(path), suffix)
			}
		}
	}
	if candidateRoot == "" {
		return false
	}
	info, err := os.Stat(candidateRoot)
	return err == nil && os.SameFile(info, rootInfo)
}

// npm's env-based node lookup must survive the same keg replacement as its
// executable. Replace only matching PATH entries, preserving the selected
// runtime and search order rather than prepending a different installation.
func stableSearchPath(env []string, replacements map[string]string) []string {
	if len(replacements) == 0 {
		return env
	}
	path := os.Getenv("PATH")
	for _, entry := range env {
		if value, found := strings.CutPrefix(entry, "PATH="); found {
			path = value
		}
	}
	entries := filepath.SplitList(path)
	changed := false
	for i, entry := range entries {
		if replacement, found := replacements[filepath.Clean(entry)]; entry != "" && found {
			entries[i], changed = replacement, true
		}
	}
	if changed {
		env = append(env, "PATH="+strings.Join(entries, string(os.PathListSeparator)))
	}
	return env
}

const prefixQueryTimeout = 2 * time.Second
const prefixOutputLimit = 4096

// Metadata probes intentionally capture only stdout. Never collect full npm
// configuration (which can contain credentials), or echo probe output on error.
func queryPrefix(ctx context.Context, path string, args ...string) (string, error) {
	output := &prefixOutput{}
	command := process.Command{Path: path, Args: args, Env: []string{"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1", "NPM_CONFIG_UPDATE_NOTIFIER=false"}}
	err := (process.Runner{GracePeriod: 100 * time.Millisecond, DiscardStderr: true}).Run(ctx, command, output)
	return output.String(), err
}

type prefixOutput struct{ buffer bytes.Buffer }

func (w *prefixOutput) String() string { return w.buffer.String() }

func (w *prefixOutput) Write(p []byte) (int, error) {
	if len(p) > prefixOutputLimit-w.buffer.Len() {
		return 0, errors.New("prefix output exceeded limit")
	}
	return w.buffer.Write(p)
}

func readPrefix(ctx context.Context, query func(context.Context, string, ...string) (string, error), path string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, prefixQueryTimeout)
	defer cancel()
	text, err := query(ctx, path, args...)
	if err != nil || ctx.Err() != nil || len(text) > prefixOutputLimit {
		return "", false
	}
	prefix := strings.TrimSpace(text)
	if !filepath.IsAbs(prefix) || strings.IndexFunc(prefix, unicode.IsControl) >= 0 {
		return "", false
	}
	prefix, err = canonicalPrefix(prefix)
	return prefix, err == nil
}

// A not-yet-created global prefix is valid. Resolve the nearest existing parent
// so symlinked prefixes still share their physical mutation resource.
func canonicalPrefix(path string) (string, error) {
	path = filepath.Clean(path)
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			info, err := os.Stat(resolved)
			if err != nil {
				return "", err
			}
			if !info.IsDir() {
				return "", errors.New("prefix is not a directory")
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = filepath.Dir(path)
	}
}

func prefixesOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	inside := func(path, root string) bool {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
		rootInfo, err := os.Stat(root)
		if err != nil {
			return false
		}
		for {
			if info, err := os.Stat(path); err == nil && os.SameFile(info, rootInfo) {
				return true
			}
			parent := filepath.Dir(path)
			if parent == path {
				return false
			}
			path = parent
		}
	}
	return inside(a, b) || inside(b, a)
}

func versionedHomebrewPath(path string) (string, bool) {
	path = filepath.ToSlash(filepath.Clean(path))
	for _, marker := range []string{"/Cellar/", "/Caskroom/"} {
		root, suffix, found := strings.Cut(path, marker)
		if found && len(strings.Split(suffix, "/")) >= 3 {
			if root == "" {
				root = "/"
			}
			return filepath.FromSlash(root), true
		}
	}
	return "", false
}

// Keep the approved tool identity: accept a stable alias only when it points to
// the same executable now. Store the alias, not its versioned symlink target.
func stableHomebrewExecutable(path, id string) string {
	original, err := os.Stat(path)
	if err != nil {
		return ""
	}
	root, _ := versionedHomebrewPath(path)
	var candidates []string
	if _, suffix, found := strings.Cut(filepath.ToSlash(filepath.Clean(path)), "/Cellar/"); found {
		parts := strings.Split(suffix, "/")
		if len(parts) >= 3 {
			candidates = append(candidates, filepath.Join(root, "opt", parts[0], filepath.FromSlash(strings.Join(parts[2:], "/"))))
		}
	}
	candidates = append(candidates, filepath.Join(root, "bin", id))
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 && os.SameFile(original, info) {
			return candidate
		}
	}
	return ""
}

func homebrewManaged(path string) bool {
	reference, _ := homebrewReference(path)
	return reference != ""
}
