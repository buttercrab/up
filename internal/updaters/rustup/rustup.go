// Package rustup plans updates to installed Rust toolchains.
package rustup

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
	return update.Plan{ID: "rustup", Resources: []string{"rust-toolchains"}, Commands: []process.Command{{Path: "rustup", Args: []string{"update"}}}}, nil
}
