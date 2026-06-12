// Package runtime is the bridge between the dfctlv2 deployment engine and
// the web platform. It owns concurrency control (Manager), live log fan-out
// (Hub), and end-to-end orchestration of deploy/rollback/phase runs
// (Executor) so that HTTP handlers can stay thin.
package runtime

import (
	"errors"
	"time"

	"df-build-server/internal/deploy/engine/store"
)

// Mode picks the high-level behaviour of a run.
//
//   - ModeDeploy: runs preflight + render + deploy + test phases for the
//     selected components. On all-scope failure the Executor reverses the
//     successful prefix.
//   - ModeRollback: runs the rollback phase, then automatically appends the
//     residue phase to surface leftovers.
//   - ModePhase: runs a single phase the caller picks (preflight | render |
//     deploy | test | rollback | residue).
type Mode string

const (
	ModeDeploy   Mode = "deploy"
	ModeRollback Mode = "rollback"
	ModePhase    Mode = "phase"
)

// ScopeKind picks the breadth of a run.
type ScopeKind string

const (
	ScopeAll       ScopeKind = "all"
	ScopeComponent ScopeKind = "component"
)

// RunOptions describes a single dispatch from the HTTP layer.
//
// Component is required when Scope is "component". Phase is required when
// Mode is "phase". Host (name or address) is optional and, when set,
// restricts execution to that single target — see Executor for the
// reachability checks.
type RunOptions struct {
	Mode      Mode
	Scope     ScopeKind
	Component string
	Phase     string
	Host      string
	DryRun    bool
}

// Conflict carries the public-facing details of an in-flight run that
// blocks a new request. Returned in 409 responses (§9.3).
type Conflict struct {
	DeploymentID    int64     `json:"deployment_id"`
	TaskType        string    `json:"task_type"`
	TargetComponent string    `json:"target_component"`
	StartedAt       time.Time `json:"started_at"`
}

// LogEntry is the on-the-wire shape pushed through the SSE Hub. Mirrors
// store.DeploymentLog but with timezone-stable JSON tags suitable for
// browser consumption (RFC3339, omits internal IDs).
type LogEntry struct {
	Sequence  int64     `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Component string    `json:"component"`
	Host      string    `json:"host"`
	// Phase, when set, is the pipeline phase the action belongs to —
	// one of preflight/render/deploy/test/rollback/residue. Set by
	// hubLogger from TaskContext.Phase. Optional because synthetic
	// task-event entries (任务启动 / 任务完成) span the whole run and
	// don't pin to a single phase.
	Phase   string `json:"phase,omitempty"`
	Action  string `json:"action_name"`
	Type    string `json:"action_type,omitempty"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// EntryFromStore converts a persisted log row into the wire shape.
func EntryFromStore(l *store.DeploymentLog) LogEntry {
	if l == nil {
		return LogEntry{}
	}
	return LogEntry{
		Sequence:  l.Sequence,
		Timestamp: l.Timestamp,
		Component: l.Component,
		Host:      l.Host,
		Phase:     l.Phase,
		Action:    l.ActionName,
		Type:      l.ActionType,
		Status:    l.Status,
		Detail:    l.Detail,
		IsError:   l.IsError,
	}
}

// ErrConflict is returned by Manager.Acquire when the requested scope
// collides with another running task. The accompanying *Conflict tells the
// HTTP layer which job is in the way.
var ErrConflict = errors.New("runtime: conflict with running task")

// ErrAlreadyDone is a soft signal that a deployment finished before
// callers tried to act on it (cancel, subscribe-then-publish-done, ...).
var ErrAlreadyDone = errors.New("runtime: deployment already finished")
