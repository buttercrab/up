package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/buttercrab/up/internal/process"
)

// Engine is immutable while Start is called. Each Start creates an independent
// run; callers must not run separate engines against the same package database.
type Engine struct {
	Jobs      int              // zero defaults to four
	Executor  process.Executor // nil uses process.Runner
	TailBytes int              // zero defaults to 64 KiB per task
	LogDir    string           // optional; must be unique to this run
}

// Run owns execution state. Snapshots never depend on a UI consuming events.
type Run struct {
	mu      sync.RWMutex
	results []Result
	done    chan struct{}
}

func (r *Run) Done() <-chan struct{} { return r.done }

func (r *Run) Snapshot() []Result {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.results)
}

func (r *Run) Wait() []Result {
	<-r.done
	return r.Snapshot()
}

func (e Engine) Start(ctx context.Context, plans []Plan) (*Run, error) {
	if e.Jobs < 0 || e.TailBytes < 0 {
		return nil, errors.New("jobs and tail bytes must be non-negative")
	}
	if e.Jobs == 0 {
		e.Jobs = 4
	}
	if e.TailBytes == 0 {
		e.TailBytes = 64 * 1024
	}
	if e.Executor == nil {
		e.Executor = process.Runner{}
	}
	// A user-supplied jobs value must not allocate more worker slots than
	// there are tasks, including for an empty run.
	e.Jobs = min(e.Jobs, max(1, len(plans)))
	// Take ownership of slices before background execution begins.
	owned := make([]Plan, len(plans))
	seen := make(map[string]bool)
	for i, p := range plans {
		if p.ID == "" || seen[p.ID] || len(p.Commands) == 0 {
			return nil, fmt.Errorf("plan %d requires a unique ID and at least one command", i)
		}
		seen[p.ID] = true
		p.Resources = slices.Clone(p.Resources)
		for _, resource := range p.Resources {
			if resource == "" {
				return nil, fmt.Errorf("plan %q has an empty resource", p.ID)
			}
		}
		p.Commands = slices.Clone(p.Commands)
		for j, c := range p.Commands {
			if c.Path == "" {
				return nil, fmt.Errorf("plan %q has an empty command", p.ID)
			}
			c.Args, c.Env = slices.Clone(c.Args), slices.Clone(c.Env)
			p.Commands[j] = c
		}
		owned[i] = p
	}
	if e.LogDir != "" {
		if err := os.MkdirAll(e.LogDir, 0o700); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
	}
	r := &Run{results: make([]Result, len(owned)), done: make(chan struct{})}
	for i, p := range owned {
		r.results[i] = Result{ID: p.ID, Status: Queued, ExitCode: -1}
	}
	go e.schedule(ctx, owned, r)
	return r, nil
}

func (e Engine) schedule(ctx context.Context, plans []Plan, r *Run) {
	defer close(r.done)
	pending := make([]int, len(plans))
	for i := range pending {
		pending[i] = i
	}
	locked := make(map[string]bool)
	completed := make(chan int, e.Jobs)
	active := 0
	cancel := ctx.Done()
	for len(pending) > 0 || active > 0 {
		if ctx.Err() != nil {
			for _, i := range pending {
				r.finish(i, ctx.Err(), true)
			}
			pending = nil
			cancel = nil
		}
		for pos := 0; pos < len(pending) && active < e.Jobs; {
			i := pending[pos]
			available := true
			for _, resource := range plans[i].Resources {
				if locked[resource] {
					available = false
					break
				}
			}
			if !available {
				pos++
				continue
			}
			for _, resource := range plans[i].Resources {
				locked[resource] = true
			}
			pending = slices.Delete(pending, pos, pos+1)
			active++
			go func() {
				e.execute(ctx, plans[i], i, r)
				completed <- i
			}()
		}
		if active == 0 {
			break
		}
		select {
		case i := <-completed:
			active--
			for _, resource := range plans[i].Resources {
				delete(locked, resource)
			}
		case <-cancel:
		}
	}
}

func (e Engine) execute(ctx context.Context, plan Plan, index int, r *Run) {
	if err := ctx.Err(); err != nil {
		r.finish(index, err, true)
		return
	}
	r.mu.Lock()
	r.results[index].Status = Running
	r.results[index].StartedAt = time.Now()
	r.mu.Unlock()
	sink := &logSink{run: r, index: index, limit: e.TailBytes}
	var err error
	if e.LogDir != "" {
		name := filepath.Join(e.LogDir, fmt.Sprintf("%03d.log", index+1))
		sink.file, err = os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			r.finish(index, fmt.Errorf("open log: %w", err), false)
			return
		}
		r.mu.Lock()
		r.results[index].LogPath = name
		r.mu.Unlock()
	}
	for _, command := range plan.Commands {
		command.TaskID = plan.ID
		if err = ctx.Err(); err != nil {
			break
		}
		if err = e.Executor.Run(ctx, command, sink); err != nil {
			break
		}
	}
	if sink.file != nil {
		err = errors.Join(err, sink.file.Close())
	}
	r.finish(index, err, ctx.Err() != nil && errors.Is(err, ctx.Err()))
}

func (r *Run) finish(index int, err error, cancelled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := &r.results[index]
	result.Err, result.FinishedAt = err, time.Now()
	switch {
	case cancelled:
		result.Status = Cancelled
	case err != nil:
		result.Status = Failed
	default:
		result.Status, result.ExitCode = Succeeded, 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
	}
}

type logSink struct {
	mu    sync.Mutex
	run   *Run
	index int
	limit int
	file  *os.File
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run.mu.Lock()
	result := &s.run.results[s.index]
	if len(p) >= s.limit {
		result.LogTail = string(p[len(p)-s.limit:])
	} else {
		tail := result.LogTail
		if len(tail)+len(p) > s.limit {
			tail = tail[len(tail)+len(p)-s.limit:]
		}
		result.LogTail = tail + string(p)
	}
	s.run.mu.Unlock()
	if s.file != nil {
		return s.file.Write(p)
	}
	return len(p), nil
}
