// Package npm updates global packages using npm's normal version policy.
package npm

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/buttercrab/up/internal/process"
	"github.com/buttercrab/up/internal/update"
)

type Updater struct{}

func (Updater) Plan(ctx context.Context) (update.Plan, error) {
	if err := ctx.Err(); err != nil {
		return update.Plan{}, err
	}
	return update.Plan{ID: "npm", Resources: []string{"npm-global"}, Commands: []process.Command{{Path: "npm", Args: []string{"update", "--global"}}}}, nil
}

// WithSelfUpdateFirst separates an existing global npm package from the batch.
// npm can unlink its own bin while reifying that batch; another package's
// lifecycle script invoking npm then fails with command-not-found. Never add
// a self-install to a custom prefix that does not already contain npm.
func WithSelfUpdateFirst(plan update.Plan, prefix string) update.Plan {
	if len(plan.Commands) != 1 || !filepath.IsAbs(prefix) {
		return plan
	}
	path := filepath.Join(prefix, "lib", "node_modules", "npm", "package.json")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return plan
	}
	file, err := os.Open(path)
	if err != nil {
		return plan
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return plan
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Name != "npm" {
		return plan
	}
	self := plan.Commands[0]
	self.Args = []string{"update", "--global", "npm"}
	self.Env = slices.Clone(self.Env)
	plan.Commands = []process.Command{self, plan.Commands[0]}
	return plan
}
