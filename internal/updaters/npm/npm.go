// Package npm updates global packages using npm's normal version policy.
package npm

import (
	"context"
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
