package updaters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/buttercrab/up/internal/inventory"
	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
)

func tools(paths map[string]string) func(string) (string, error) {
	return func(id string) (string, error) {
		if path, ok := paths[id]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestDiscoveryPreparesNPMCommandsAfterEntrypointNormalization(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "lib", "node_modules", "npm", "package.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"npm"}`), 0600); err != nil {
		t.Fatal(err)
	}
	deps := discoveryTools(map[string]string{"npm": "/tools/npm", "node": "/tools/node"})
	deps.query = func(context.Context, string, ...string) (string, error) { return root, nil }
	selection, err := discover(context.Background(), "npm", "", deps)
	if err != nil {
		t.Fatal(err)
	}
	commands := selection.Plans[0].Commands
	if len(commands) != 2 || !slices.Equal(commands[0].Args, []string{"update", "--global", "npm"}) || !slices.Equal(commands[1].Args, []string{"update", "--global"}) {
		t.Fatalf("wrong npm plan: %+v", commands)
	}
	for _, command := range commands {
		if command.Path != "/tools/npm" {
			t.Fatal("lost selected npm entrypoint")
		}
	}
}

func discoveryTools(paths map[string]string) discoveryDeps {
	return discoveryDeps{lookup: tools(paths), query: func(_ context.Context, _ string, args ...string) (string, error) {
		if slices.Equal(args, []string{"--prefix"}) {
			return "/metadata/homebrew", nil
		}
		if slices.Equal(args, []string{"prefix", "--global"}) {
			return "/metadata/npm", nil
		}
		return "", errors.New("unexpected metadata query")
	}}
}

func TestNativeCommandsAndIndependentResources(t *testing.T) {
	s, err := discover(context.Background(), "", "", discoveryTools(map[string]string{"brew": "/tools/brew", "rustup": "/tools/rustup", "npm": "/tools/npm", "node": "/tools/node"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Plans) != 3 {
		t.Fatalf("plans=%+v", s)
	}
	if len(s.Installations) != len(s.Plans) {
		t.Fatalf("missing installation metadata: %+v", s)
	}
	want := [][]string{{"upgrade", "--no-ask"}, {"update"}, {"update", "--global"}}
	resources := map[string]bool{}
	for i, p := range s.Plans {
		metadata := s.Installations[i]
		if metadata.ID != p.ID || metadata.UpdateSupport != inventory.Supported || metadata.PlannedExecutable != p.Commands[0].Path || !reflect.DeepEqual(metadata.Resources, p.Resources) {
			t.Fatalf("metadata diverged from execution plan: %+v %+v", metadata, p)
		}
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

func TestInventoryCancellationDuringLastLookup(t *testing.T) {
	for _, found := range []bool{false, true} {
		t.Run(fmt.Sprintf("found=%v", found), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deps := discoveryTools(nil)
			deps.lookup = func(id string) (string, error) {
				if id == "softwareupdate" {
					cancel()
					if found {
						return "/fixture/softwareupdate", nil
					}
				}
				return "", exec.ErrNotFound
			}
			if _, err := inspectInventory(ctx, deps); !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation in final lookup: %v", err)
			}
		})
	}
}

func TestInventoryCatalogUsesOnlyExistingBoundedPrefixQueries(t *testing.T) {
	root := t.TempDir()
	paths := make(map[string]string)
	for _, definition := range inventory.Catalog() {
		path := filepath.Join(root, "bin", definition.Commands[0])
		writeExecutable(t, path, "#!/bin/sh\nexit 93\n")
		paths[definition.Commands[0]] = path
	}
	// Cargo is a rustup proxy when the two entrypoints share file identity.
	if err := os.Remove(paths["cargo"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(paths["rustup"], paths["cargo"]); err != nil {
		t.Fatal(err)
	}
	physicalNode := filepath.Join(root, "Cellar", "node", "1", "bin", "node")
	writeExecutable(t, physicalNode, "#!/bin/sh\nexit 93\n")
	paths["node"] = filepath.Join(root, "bin", "node")
	if err := os.Symlink(physicalNode, paths["node"]); err != nil {
		t.Fatal(err)
	}
	physicalNpm := filepath.Join(root, "Cellar", "node", "1", "bin", "npm")
	writeExecutable(t, physicalNpm, "#!/bin/sh\nexit 93\n")
	if err := os.Remove(paths["npm"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physicalNpm, paths["npm"]); err != nil {
		t.Fatal(err)
	}
	deps := discoveryTools(paths)
	var queries []string
	deps.query = func(ctx context.Context, path string, args ...string) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("prefix query lacked a deadline")
		}
		queries = append(queries, path+" "+strings.Join(args, " "))
		if path == paths["brew"] && slices.Equal(args, []string{"--prefix"}) {
			return root, nil
		}
		if path == paths["npm"] && slices.Equal(args, []string{"prefix", "--global"}) {
			return filepath.Join(root, "global"), nil
		}
		t.Fatalf("inventory executed an unexpected tool: %s %v", path, args)
		return "", nil
	}
	report, err := inspectInventory(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Scope != inventory.CurrentPath || len(report.Installations) != len(inventory.Catalog()) || len(report.Missing) != 0 {
		t.Fatalf("incomplete inventory: %+v", report)
	}
	if len(queries) != 2 {
		t.Fatalf("extra metadata probes: %v", queries)
	}
	byID := make(map[string]inventory.Installation)
	for _, item := range report.Installations {
		byID[item.ID] = item
	}
	if !byID["brew"].PrefixKnown || !byID["npm"].PrefixKnown || byID["npm"].Owner != inventory.OwnerHomebrew || !slices.Contains(byID["npm"].Resources, "homebrew") {
		t.Fatalf("execution ownership metadata was lost: %+v", byID["npm"])
	}
	if len(byID["npm"].Runtimes) != 1 || byID["npm"].Runtimes[0].Owner != inventory.OwnerHomebrew || byID["npm"].Runtimes[0].Executable != paths["node"] {
		t.Fatalf("missing node runtime: %+v", byID["npm"])
	}
	if byID["cargo"].Owner != inventory.OwnerRustup || len(byID["cargo"].Runtimes) != 1 || byID["cargo"].Runtimes[0].Executable != paths["rustup"] {
		t.Fatalf("missing rustup proxy ownership: %+v", byID["cargo"])
	}
	for _, definition := range inventory.Catalog() {
		if definition.UpdateSupport == inventory.Unsupported {
			item := byID[definition.ID]
			if item.UpdateSupport != inventory.Unsupported || item.PlannedExecutable != "" || item.PrefixKnown || len(item.Resources) != 0 {
				t.Fatalf("unsupported tool gained execution capability: %+v", item)
			}
		}
	}
}

func TestInventoryDoesNotExecuteUnsupportedToolsAndSeparatesMissing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "code-insiders")
	writeExecutable(t, path, "#!/bin/sh\nexit 93\n")
	deps := discoveryTools(map[string]string{"code-insiders": path})
	deps.query = func(context.Context, string, ...string) (string, error) {
		t.Fatal("unsupported inventory tool was executed")
		return "", nil
	}
	report, err := inspectInventory(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Installations) != 1 || report.Installations[0].ID != "vscode" || report.Installations[0].Executable != path || report.Installations[0].UpdateSupport != inventory.Unsupported || len(report.Missing) != len(inventory.Catalog())-1 {
		t.Fatalf("incorrect PATH catalogue: %+v", report)
	}
	if _, err := discover(context.Background(), "vscode", "", deps); !errors.Is(err, ErrSelection) {
		t.Fatalf("unsupported inventory item became executable: %v", err)
	}
}

func TestInventoryRetainsBlockedSupportedAdapterAndContinues(t *testing.T) {
	root := t.TempDir()
	npm := filepath.Join(root, "Cellar", "node", "1", "bin", "npm")
	writeExecutable(t, npm, "#!/bin/sh\nexit 93\n")
	uv := filepath.Join(root, "uv")
	writeExecutable(t, uv, "#!/bin/sh\nexit 93\n")
	deps := discoveryTools(map[string]string{"brew": filepath.Join(root, "bin", "brew"), "npm": npm, "node": "/independent/node", "uv": uv})
	deps.query = func(_ context.Context, _ string, _ ...string) (string, error) { return root, nil }
	report, err := inspectInventory(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]inventory.Installation)
	for _, item := range report.Installations {
		byID[item.ID] = item
	}
	if byID["npm"].UpdateSupport != inventory.Blocked || byID["npm"].PlannedExecutable != "" || !strings.Contains(byID["npm"].Reason, "stable entry point") {
		t.Fatalf("unsafe adapter was not blocked: %+v", byID["npm"])
	}
	if byID["uv"].UpdateSupport != inventory.Unsupported || byID["brew"].UpdateSupport != inventory.Supported {
		t.Fatalf("blocked adapter aborted other detection: %+v", report)
	}
	if _, err := discover(context.Background(), "brew,npm", "", deps); err == nil {
		t.Fatal("strict upgrade planning accepted blocked adapter")
	}
}

func TestInventoryUnknownPrefixAndCancellation(t *testing.T) {
	deps := discoveryTools(map[string]string{"brew": "/tools/brew", "npm": "/tools/npm"})
	deps.query = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("metadata failed without returning sensitive output")
	}
	report, err := inspectInventory(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range report.Installations {
		if item.PrefixKnown || item.Prefix != "" || !strings.Contains(item.Reason, "could not be determined") {
			t.Fatalf("unknown scope was presented as known: %+v", item)
		}
		if item.ID == "npm" && !slices.Contains(item.Resources, "homebrew") {
			t.Fatal("unknown prefix lost conservative resource lock")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps.query = func(context.Context, string, ...string) (string, error) { cancel(); return "", context.Canceled }
	if _, err := inspectInventory(ctx, deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("inventory swallowed cancellation: %v", err)
	}
}

func TestDiscoverySelectionAndMissingTools(t *testing.T) {
	lookup := discoveryTools(map[string]string{"brew": "/tools/brew", "npm": "/tools/npm"})
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
	s, err = discover(context.Background(), "", "", discoveryTools(nil))
	if err != nil || len(s.Plans) != 0 || len(s.Skipped) != 3 {
		t.Fatalf("empty discovery=%+v err=%v", s, err)
	}
}

func TestHomebrewRuntimeConflictsAreSerialized(t *testing.T) {
	root := t.TempDir()
	rustupPath := filepath.Join(root, "Cellar", "rustup", "1", "bin", "rustup")
	writeExecutable(t, rustupPath, "#!/bin/sh\nexit 0\n")
	if err := os.MkdirAll(filepath.Join(root, "opt"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Cellar", "rustup", "1"), filepath.Join(root, "opt", "rustup")); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(root, "Cellar", "node", "1", "bin", "node"), "#!/bin/sh\nexit 0\n")
	if err := os.Symlink(filepath.Join(root, "Cellar", "node", "1"), filepath.Join(root, "opt", "node")); err != nil {
		t.Fatal(err)
	}
	deps := discoveryTools(map[string]string{"brew": filepath.Join(root, "bin", "brew"), "rustup": rustupPath, "npm": "/home/user/npm/bin/npm", "node": filepath.Join(root, "Cellar", "node", "1", "bin", "node")})
	deps.query = func(_ context.Context, _ string, args ...string) (string, error) {
		if slices.Equal(args, []string{"--prefix"}) {
			return root, nil
		}
		return "/metadata/npm", nil
	}
	s, err := discover(context.Background(), "", "", deps)
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
	if s.Plans[1].Commands[0].Path != filepath.Join(root, "opt", "rustup", "bin", "rustup") {
		t.Fatalf("did not select stable rustup entrypoint: %s", s.Plans[1].Commands[0].Path)
	}
}

func writeExecutable(t *testing.T, path, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestNpmMutationPrefixDeterminesHomebrewConflict(t *testing.T) {
	root := t.TempDir()
	brewPrefix := filepath.Join(root, "brew")
	if err := os.MkdirAll(brewPrefix, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(brewPrefix, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, prefix string
		queryErr     error
		locked       bool
	}{
		{"same prefix", brewPrefix, nil, true},
		{"nested prefix", filepath.Join(brewPrefix, "global"), nil, true},
		{"symlinked prefix", filepath.Join(alias, "global"), nil, true},
		{"independent prefix", filepath.Join(root, "other"), nil, false},
		{"sibling name", brewPrefix + "-other", nil, false},
		{"empty scope", "", nil, true},
		{"relative scope", "relative", nil, true},
		{"multiline scope", "/safe\n/private-token", nil, true},
		{"query failed", "/safe", errors.New("metadata unavailable"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := discoveryTools(map[string]string{"brew": "/tools/brew", "npm": "/nvm/bin/npm", "node": "/nvm/bin/node"})
			var called []string
			deps.query = func(ctx context.Context, path string, args ...string) (string, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > prefixQueryTimeout {
					t.Error("metadata query is not bounded")
				}
				called = append(called, path+" "+strings.Join(args, " "))
				if path == "/tools/brew" && slices.Equal(args, []string{"--prefix"}) {
					return brewPrefix + "\n", nil
				}
				if path == "/nvm/bin/npm" && slices.Equal(args, []string{"prefix", "--global"}) {
					return tc.prefix, tc.queryErr
				}
				t.Fatalf("unexpected metadata query: %s %v", path, args)
				return "", nil
			}
			selection, err := discover(context.Background(), "brew,npm", "", deps)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(selection.Plans[1].Resources, "homebrew"); got != tc.locked {
				t.Fatalf("lock=%v want=%v resources=%v", got, tc.locked, selection.Plans[1].Resources)
			}
			if len(called) != 2 {
				t.Fatalf("queries=%v", called)
			}
		})
	}
}

func TestUnknownBrewPrefixSerializesNpm(t *testing.T) {
	deps := discoveryTools(map[string]string{"brew": "/tools/brew", "npm": "/tools/npm", "node": "/tools/node"})
	deps.query = func(_ context.Context, path string, _ ...string) (string, error) {
		if path == "/tools/brew" {
			return "", errors.New("unreadable prefix")
		}
		return "/independent/npm", nil
	}
	s, err := discover(context.Background(), "", "", deps)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.Plans[1].Resources, "homebrew") {
		t.Fatal("unknown Homebrew mutation scope allowed parallel npm")
	}
}

func TestUnreadablePrefixSerializesNpm(t *testing.T) {
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	deps := discoveryTools(map[string]string{"brew": "/tools/brew", "npm": "/tools/npm", "node": "/tools/node"})
	deps.query = func(_ context.Context, path string, _ ...string) (string, error) {
		if path == "/tools/brew" {
			return root, nil
		}
		return loop, nil
	}
	s, err := discover(context.Background(), "", "", deps)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.Plans[1].Resources, "homebrew") {
		t.Fatal("unreadable prefix allowed parallel npm")
	}
}

func TestDiscoveryPropagatesProbeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := discoveryTools(map[string]string{"brew": "/tools/brew", "npm": "/tools/npm"})
	deps.query = func(context.Context, string, ...string) (string, error) { cancel(); return "", context.Canceled }
	if _, err := discover(ctx, "brew,npm", "", deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestPrefixProbeHelper(t *testing.T) {
	switch os.Getenv("UP_PREFIX_PROBE_HELPER") {
	case "":
		return
	case "prefix":
		if os.Getenv("HOMEBREW_NO_AUTO_UPDATE") != "1" || os.Getenv("HOMEBREW_NO_ANALYTICS") != "1" || os.Getenv("NPM_CONFIG_UPDATE_NOTIFIER") != "false" {
			os.Exit(7)
		}
		fmt.Fprintln(os.Stderr, "private-warning-do-not-return")
		fmt.Fprintln(os.Stdout, os.TempDir())
	case "large":
		fmt.Fprint(os.Stdout, strings.Repeat("x", prefixOutputLimit+1))
	case "wait":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func TestPrefixQueryBoundsOutputTimeoutAndCapturedData(t *testing.T) {
	t.Setenv("UP_PREFIX_PROBE_HELPER", "prefix")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	text, err := queryPrefix(context.Background(), os.Args[0], "-test.run=^TestPrefixProbeHelper$")
	if err != nil || strings.TrimSpace(text) != os.TempDir() || strings.Contains(text, "private-warning") {
		t.Fatalf("prefix=%q err=%v", text, err)
	}
	t.Setenv("UP_PREFIX_PROBE_HELPER", "large")
	text, err = queryPrefix(context.Background(), os.Args[0], "-test.run=^TestPrefixProbeHelper$")
	if err == nil || len(text) > prefixOutputLimit {
		t.Fatalf("output not bounded: bytes=%d err=%v", len(text), err)
	}
	t.Setenv("UP_PREFIX_PROBE_HELPER", "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, known := readPrefix(ctx, queryPrefix, os.Args[0], "-test.run=^TestPrefixProbeHelper$")
	if known || time.Since(start) > time.Second {
		t.Fatalf("timeout not respected: known=%v elapsed=%s", known, time.Since(start))
	}
}

type registryExecutor func(context.Context, process.Command, io.Writer) error

func (f registryExecutor) Run(ctx context.Context, c process.Command, w io.Writer) error {
	return f(ctx, c, w)
}

func TestVersionedExecutableUsesStableOptAliasAcrossUpgrade(t *testing.T) {
	t.Run("manager and runtime", func(t *testing.T) { testStableNodeAcrossUpgrade(t, true) })
	t.Run("runtime only", func(t *testing.T) { testStableNodeAcrossUpgrade(t, false) })
}

func testStableNodeAcrossUpgrade(t *testing.T, versionedManager bool) {
	t.Helper()
	root := t.TempDir()
	old := filepath.Join(root, "Cellar", "node", "1", "bin", "npm")
	newPath := filepath.Join(root, "Cellar", "node", "2", "bin", "npm")
	writeExecutable(t, old, "#!/usr/bin/env node\n")
	writeExecutable(t, newPath, "#!/usr/bin/env node\n")
	writeExecutable(t, filepath.Join(filepath.Dir(old), "node"), "#!/bin/sh\nprintf old\n")
	writeExecutable(t, filepath.Join(filepath.Dir(newPath), "node"), "#!/bin/sh\nprintf new\n")
	t.Setenv("PATH", filepath.Dir(old)+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	if err := os.MkdirAll(filepath.Join(root, "opt"), 0o700); err != nil {
		t.Fatal(err)
	}
	opt := filepath.Join(root, "opt", "node")
	if err := os.Symlink(filepath.Join(root, "Cellar", "node", "1"), opt); err != nil {
		t.Fatal(err)
	}
	selectedNpm := old
	if !versionedManager {
		selectedNpm = filepath.Join(root, "user", "bin", "npm")
		writeExecutable(t, selectedNpm, "#!/usr/bin/env node\n")
	}
	deps := discoveryTools(map[string]string{"brew": filepath.Join(root, "bin", "brew"), "npm": selectedNpm, "node": filepath.Join(root, "Cellar", "node", "1", "bin", "node")})
	deps.query = func(_ context.Context, _ string, args ...string) (string, error) {
		if slices.Equal(args, []string{"--prefix"}) {
			return root, nil
		}
		return filepath.Join(root, "globals"), nil
	}
	s, err := discover(context.Background(), "brew,npm", "", deps)
	if err != nil {
		t.Fatal(err)
	}
	wantNpm := selectedNpm
	if versionedManager {
		wantNpm = filepath.Join(opt, "bin", "npm")
	}
	if got := s.Plans[1].Commands[0].Path; got != wantNpm {
		t.Fatalf("unstable path selected: %s", got)
	}
	executor := registryExecutor(func(ctx context.Context, c process.Command, w io.Writer) error {
		if c.TaskID == "brew" {
			if err := os.Remove(opt); err != nil {
				return err
			}
			if err := os.Symlink(filepath.Join(root, "Cellar", "node", "2"), opt); err != nil {
				return err
			}
			return os.RemoveAll(filepath.Join(root, "Cellar", "node", "1"))
		}
		return (process.Runner{}).Run(ctx, c, w)
	})
	run, err := (update.Engine{Jobs: 2, Executor: executor}).Start(context.Background(), s.Plans)
	if err != nil {
		t.Fatal(err)
	}
	results := run.Wait()
	if results[0].Status != update.Succeeded || results[1].Status != update.Succeeded || results[1].LogTail != "new" {
		t.Fatalf("stale entrypoint after upgrade: %+v", results)
	}
}

func TestStableRuntimePreservesSearchOrderAndCommandOverrides(t *testing.T) {
	t.Setenv("PATH", "/inherited/only")
	result := stableSearchPath([]string{"KEEP=value", "PATH=/first:/Cellar/node/old/bin:/last"}, map[string]string{"/Cellar/node/old/bin": "/opt/node/bin"})
	if result[len(result)-1] != "PATH=/first:/opt/node/bin:/last" || result[0] != "KEEP=value" {
		t.Fatalf("changed runtime precedence: %v", result)
	}
}

func TestVersionedExecutableWithoutEquivalentAliasFailsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "Cellar", "node@24", "1", "bin", "npm")
	writeExecutable(t, old, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(root, "bin", "npm"), "#!/bin/sh\nexit 0\n")
	deps := discoveryTools(map[string]string{"brew": filepath.Join(root, "bin", "brew"), "npm": old, "node": "/independent/node"})
	deps.query = func(_ context.Context, _ string, _ ...string) (string, error) { return root, nil }
	if _, err := discover(context.Background(), "brew,npm", "", deps); err == nil || !strings.Contains(err.Error(), "without a matching stable entry point") {
		t.Fatalf("unrelated alias accepted: %v", err)
	}
}

func TestCustomManagerAliasUsesStableCommandWithoutReplacingUserBin(t *testing.T) {
	for _, sharedTarget := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared final target=%v", sharedTarget), func(t *testing.T) {
			root := t.TempDir()
			old := filepath.Join(root, "Cellar", "node", "1", "bin", "npm")
			fresh := filepath.Join(root, "Cellar", "node", "2", "bin", "npm")
			writeExecutable(t, old, "#!/bin/sh\nprintf old\n")
			writeExecutable(t, fresh, "#!/bin/sh\nprintf new\n")
			if sharedTarget {
				shared := filepath.Join(root, "shared", "npm")
				writeExecutable(t, shared, "#!/bin/sh\nprintf old\n")
				if err := os.Remove(old); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(shared, old); err != nil {
					t.Fatal(err)
				}
			}
			opt := filepath.Join(root, "opt", "node")
			if err := os.MkdirAll(filepath.Dir(opt), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "Cellar", "node", "1"), opt); err != nil {
				t.Fatal(err)
			}
			custom := filepath.Join(root, "custom", "bin", "npm")
			if err := os.MkdirAll(filepath.Dir(custom), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(old, custom); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(custom)+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
			deps := discoveryTools(map[string]string{"brew": filepath.Join(root, "bin", "brew"), "npm": custom, "node": "/independent/node"})
			deps.query = func(_ context.Context, _ string, args ...string) (string, error) {
				if slices.Equal(args, []string{"--prefix"}) {
					return root, nil
				}
				return filepath.Join(root, "globals"), nil
			}
			s, err := discover(context.Background(), "brew,npm", "", deps)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Plans[1].Commands[0].Path; got != filepath.Join(opt, "bin", "npm") {
				t.Fatalf("custom alias retained: %s", got)
			}
			for _, entry := range s.Plans[1].Commands[0].Env {
				if strings.HasPrefix(entry, "PATH=") {
					t.Fatalf("arbitrary user-bin directory replaced: %s", entry)
				}
			}
			executor := registryExecutor(func(ctx context.Context, c process.Command, w io.Writer) error {
				if c.TaskID == "brew" {
					if err := os.Remove(opt); err != nil {
						return err
					}
					if err := os.Symlink(filepath.Join(root, "Cellar", "node", "2"), opt); err != nil {
						return err
					}
					return os.RemoveAll(filepath.Join(root, "Cellar", "node", "1"))
				}
				return (process.Runner{}).Run(ctx, c, w)
			})
			run, err := (update.Engine{Jobs: 2, Executor: executor}).Start(context.Background(), s.Plans)
			if err != nil {
				t.Fatal(err)
			}
			r := run.Wait()
			if r[1].Status != update.Succeeded || r[1].LogTail != "new" {
				t.Fatalf("custom alias failed after old keg removal: %+v", r)
			}
		})
	}
}

func TestKnownMaintainedAliasesArePreserved(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "Cellar", "node", "1", "bin", "node")
	writeExecutable(t, physical, "#!/bin/sh\nexit 0\n")
	opt := filepath.Join(root, "opt", "node")
	if err := os.MkdirAll(filepath.Dir(opt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Cellar", "node", "1"), opt); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin", "node")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, bin); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(root, "custom", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(custom), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(opt, "bin", "node"), custom); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(opt, "bin", "node"), bin, custom} {
		got, err := approvedExecutable(path, "node", true, root, true)
		if err != nil || got != path {
			t.Fatalf("maintained alias changed: %s -> %s (%v)", path, got, err)
		}
	}
}

func TestCustomRuntimeAliasFailsClosedWithoutReplacingUserBin(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "Cellar", "node", "1", "bin", "node")
	writeExecutable(t, physical, "#!/bin/sh\nexit 0\n")
	opt := filepath.Join(root, "opt", "node")
	if err := os.MkdirAll(filepath.Dir(opt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Cellar", "node", "1"), opt); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(root, "custom", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(custom), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, custom); err != nil {
		t.Fatal(err)
	}
	deps := discoveryTools(map[string]string{"brew": filepath.Join(root, "bin", "brew"), "npm": "/independent/npm", "node": custom})
	deps.query = func(_ context.Context, _ string, _ ...string) (string, error) { return root, nil }
	if _, err := discover(context.Background(), "brew,npm", "", deps); err == nil || !strings.Contains(err.Error(), "custom alias") {
		t.Fatalf("unsafe runtime alias accepted: %v", err)
	}
}
