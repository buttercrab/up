package npm_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/buttercrab/up/internal/updaters/npm"
)

func TestSelfUpdateBeforeLifecycleBatchOnlyForExistingGlobalNPM(t *testing.T) {
	plan, err := (npm.Updater{}).Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan.Commands[0].Path = "/stable/npm"
	plan.Commands[0].Env = []string{"PATH=/stable"}
	root := t.TempDir()
	if got := npm.WithSelfUpdateFirst(plan, root); len(got.Commands) != 1 {
		t.Fatal("missing npm package caused a self-install")
	}
	path := filepath.Join(root, "lib", "node_modules", "npm", "package.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"name":"npm"}`, `{"name":"other"}`, `invalid`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		got := npm.WithSelfUpdateFirst(plan, root)
		if body != `{"name":"npm"}` {
			if len(got.Commands) != 1 {
				t.Fatal("invalid metadata caused a self-install")
			}
			continue
		}
		if len(got.Commands) != 2 || !reflect.DeepEqual(got.Commands[0].Args, []string{"update", "--global", "npm"}) || !reflect.DeepEqual(got.Commands[1].Args, []string{"update", "--global"}) {
			t.Fatalf("wrong command order: %+v", got.Commands)
		}
		for _, command := range got.Commands {
			if command.Path != "/stable/npm" || !reflect.DeepEqual(command.Env, plan.Commands[0].Env) {
				t.Fatal("stable entrypoint or environment was lost")
			}
		}
		got.Commands[0].Env[0] = "changed"
		if plan.Commands[0].Env[0] != "PATH=/stable" {
			t.Fatal("self command aliases the original environment")
		}
	}
}
