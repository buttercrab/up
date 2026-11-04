// Package update schedules independent update plans. It does not render UI or
// discover package managers.
package update

import (
	"context"
	"time"

	"github.com/buttercrab/up/internal/output"
	"github.com/buttercrab/up/internal/process"
)

// Updater creates a plan without modifying installed packages.
type Updater interface {
	Plan(context.Context) (Plan, error)
}

// Plan executes commands sequentially and holds all Resources for its entire
// lifetime. Plans sharing any resource never overlap. ID must be unique.
type Plan struct {
	ID        string
	Commands  []process.Command
	Resources []string
}

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

// Result is a snapshot, including a bounded log tail and an optional full log.
// ExitCode is -1 when no process exit code is available.
type Result struct {
	ID           string
	Status       Status
	StartedAt    time.Time
	FinishedAt   time.Time
	Err          error
	ExitCode     int
	LogTail      string
	LogPath      string
	DisplayLog   string        // bounded terminal redraw projection; LogTail remains raw
	DisplayReady bool          // distinguishes an empty projection from an absent one
	Progress     []output.Item // advisory observations, never execution outcomes
}
