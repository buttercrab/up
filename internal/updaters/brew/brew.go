// Package brew plans Homebrew upgrades. Other adapters will use the same contract.
package brew

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
	return update.Plan{
		ID:        "brew",
		Resources: []string{"homebrew"},
		Commands:  []process.Command{{Path: "brew", Args: []string{"upgrade", "--no-ask"}}},
	}, nil
}
