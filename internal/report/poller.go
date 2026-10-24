package report

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"
)

// Snapshot is everything the dashboard shows.
type Snapshot struct {
	Repos []RepositoryScan
	Deps  []Dependency
}

// Poller keeps the latest Snapshot of a Backend.
type Poller struct {
	backend Backend
	current atomic.Pointer[Snapshot]
}

// NewPoller starts empty; Run fills it.
func NewPoller(b Backend) *Poller {
	p := &Poller{backend: b}
	p.current.Store(&Snapshot{})
	return p
}

// Snapshot returns the latest successful read.
func (p *Poller) Snapshot() Snapshot { return *p.current.Load() }

// Run reads the backend now and then every interval until ctx ends.
func (p *Poller) Run(ctx context.Context, every time.Duration) {
	Every(ctx, every, func() { p.load(ctx) })
}

// Every calls fn now and then after each interval until ctx ends.
func Every(ctx context.Context, d time.Duration, fn func()) {
	for {
		fn()
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

func (p *Poller) load(ctx context.Context) {
	repos, err := p.backend.ListRepositoryScans(ctx)
	if err == nil {
		var deps []Dependency
		if deps, err = p.backend.ListDependencies(ctx, ""); err == nil {
			p.current.Store(&Snapshot{Repos: repos, Deps: deps})
			return
		}
	}
	slog.Warn("reading reports failed, keeping the previous snapshot", "error", err)
}

// decode parses one stored report; an unreadable one is skipped so it cannot freeze every other report.
func decode[T any](data []byte, name string) (T, bool) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		slog.Warn("skipping an unreadable report", "name", name, "error", err)
		return v, false
	}
	return v, true
}
