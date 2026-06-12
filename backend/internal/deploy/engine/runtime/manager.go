package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Manager enforces the concurrency policy from §9 of requirements.md.
//
// Rules:
//   - At most one "all" scope run at any time.
//   - Many concurrent runs are allowed as long as they target different
//     components. (Hosts are not scoped — running deploy on redis on host
//     A blocks any redis run, even on a different host, because the
//     deployment engine takes per-component locks internally.)
//   - When an "all" run holds the lock, every other request fails with
//     ErrConflict. When a component lock exists, an "all" request fails
//     with ErrConflict.
//
// The lock state is purely in-memory; the manager itself does not know
// about cancelation. Cancel() exposes the goroutine cancel func registered
// at acquire time so HTTP handlers can stop a run cleanly.
type Manager struct {
	mu sync.Mutex

	full      *jobHandle            // non-nil while an "all" run holds the lock
	component map[string]*jobHandle // component name -> handle
}

type jobHandle struct {
	deploymentID    int64
	taskType        string
	targetComponent string
	startedAt       time.Time
	cancel          context.CancelFunc
}

// NewManager returns a fresh Manager. Manager is safe for concurrent use.
func NewManager() *Manager {
	return &Manager{component: map[string]*jobHandle{}}
}

// Acquire claims the lock requested by opts. On success it returns a
// release function (idempotent) and a cancel func bound to ctx. On
// conflict it returns ErrConflict together with a *Conflict snapshot of
// the offender suitable for 409 responses.
//
// deploymentID and taskType are recorded in the handle so a follow-up
// Conflict response can describe what is in the way without an extra
// store lookup.
func (m *Manager) Acquire(parent context.Context, deploymentID int64, taskType string, opts RunOptions) (context.Context, context.CancelFunc, func(), *Conflict, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.full != nil {
		return nil, nil, nil, snapshot(m.full), ErrConflict
	}

	if opts.Scope == ScopeAll {
		if len(m.component) > 0 {
			// pick any running component to advertise
			for _, h := range m.component {
				return nil, nil, nil, snapshot(h), ErrConflict
			}
		}
	} else {
		if existing, busy := m.component[opts.Component]; busy {
			return nil, nil, nil, snapshot(existing), ErrConflict
		}
	}

	ctx, cancel := context.WithCancel(parent)
	target := opts.Component
	if opts.Scope == ScopeAll {
		target = "all"
	}
	h := &jobHandle{
		deploymentID:    deploymentID,
		taskType:        taskType,
		targetComponent: target,
		startedAt:       time.Now().UTC(),
		cancel:          cancel,
	}
	if opts.Scope == ScopeAll {
		m.full = h
	} else {
		m.component[opts.Component] = h
	}

	released := false
	release := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if released {
			return
		}
		released = true
		if m.full == h {
			m.full = nil
		}
		if cur, ok := m.component[opts.Component]; ok && cur == h {
			delete(m.component, opts.Component)
		}
	}
	return ctx, cancel, release, nil, nil
}

// Cancel triggers cancellation of the run that was registered for the
// given deployment ID. Returns false when no in-flight run matches.
func (m *Manager) Cancel(deploymentID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.full != nil && m.full.deploymentID == deploymentID {
		m.full.cancel()
		return true
	}
	for _, h := range m.component {
		if h.deploymentID == deploymentID {
			h.cancel()
			return true
		}
	}
	return false
}

// Snapshot returns a Conflict describing the current running task, if any.
// Returns nil when nothing is running. Useful for status endpoints.
func (m *Manager) Snapshot() *Conflict {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.full != nil {
		return snapshot(m.full)
	}
	for _, h := range m.component {
		return snapshot(h)
	}
	return nil
}

// SnapshotAll returns one Conflict per in-flight task. Used by status
// pages to render every concurrent run rather than just the first one
// the iterator hands out. Order is undefined; callers should sort by
// StartedAt if they need stability.
func (m *Manager) SnapshotAll() []*Conflict {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Conflict, 0, len(m.component)+1)
	if m.full != nil {
		out = append(out, snapshot(m.full))
	}
	for _, h := range m.component {
		out = append(out, snapshot(h))
	}
	return out
}

// HasRunning reports whether any run holds a lock.
func (m *Manager) HasRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.full != nil || len(m.component) > 0
}

func snapshot(h *jobHandle) *Conflict {
	if h == nil {
		return nil
	}
	return &Conflict{
		DeploymentID:    h.deploymentID,
		TaskType:        h.taskType,
		TargetComponent: h.targetComponent,
		StartedAt:       h.startedAt,
	}
}

// String is used in fmt-based test diagnostics.
func (c *Conflict) String() string {
	if c == nil {
		return "<nil conflict>"
	}
	return fmt.Sprintf("deployment=%d type=%s target=%s startedAt=%s",
		c.DeploymentID, c.TaskType, c.TargetComponent, c.StartedAt.Format(time.RFC3339))
}
