// Package deploy is the root of the deployment-management subsystem.
//
// It migrates the HIS infrastructure offline-deployment engine (originally the
// standalone his-deploy / dfctl project) into df-build-system. The subsystem
// deploys ~23 infrastructure components (K8s system, middleware, base services)
// to target hosts over direct SSH, rendering runtime config from PostgreSQL on
// the fly and streaming logs over SSE.
//
// Boundaries:
//   - Target hosts reuse the existing Server Management feature (model.Server);
//     no duplicate host table is maintained.
//   - Business data is persisted in PostgreSQL via GORM.
//   - Rollback markers live in State_Dir on each target host's local filesystem,
//     NOT in PostgreSQL.
//
// Sub-packages:
//   - planner:            Pipeline YAML -> ordered Action plan, virtual component expansion
//   - runner:             sequential Action execution with fail-stop semantics
//   - runtime:            concurrency slots, cancellation, conflict snapshot, log pubsub
//   - exec:               local + remote(SSH/SFTP) execution backends
//   - actions:            ~40 declarative Action handlers
//   - render:             Go template rendering + parameter merge (defaults->global->override)
//   - offline:            offline bundle sha256 verification + atomic swap
//   - defaults:           embedded component defaults and pipeline YAML
//   - conflict:           host-binding conflict matrix
//   - statedir:           rollback marker conventions on target hosts
//   - handler:            Gin HTTP handlers under /api/deployment
//   - repository:         GORM repositories for deployment business data
package deploy

import "context"

// CreateRunInput describes a request to deploy one or more components.
type CreateRunInput struct {
	TriggerUser string   // resolved from the JWT context
	ScopeType   string   // "components" | "virtual"
	Components  []string // component codes, or a single virtual component name
	DryRun      bool
}

// RollbackInput describes a request to roll back one or more components.
type RollbackInput struct {
	TriggerUser string
	ScopeType   string
	Components  []string
}

// RunPlan is the previewed action plan grouped by host (no execution).
type RunPlan struct {
	Components []string         `json:"components"`
	Hosts      []HostPlanGroup  `json:"hosts"`
}

// HostPlanGroup is the ordered set of planned actions for a single host.
type HostPlanGroup struct {
	Host    string       `json:"host"`
	Actions []PlanAction `json:"actions"`
}

// PlanAction is a single previewed action.
type PlanAction struct {
	Component string `json:"component"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Target    string `json:"target"`
}

// Service is the deployment-management orchestrator facade.
//
// Implementation is wired in Task 11; this interface establishes the package
// boundary so handlers (Task 12) and the rest of the subsystem can be developed
// against a stable contract.
type Service interface {
	CreateRun(ctx context.Context, in CreateRunInput) (uint, error)
	Preview(ctx context.Context, in CreateRunInput) (*RunPlan, error)
	Cancel(ctx context.Context, runID uint) error
	Rollback(ctx context.Context, in RollbackInput) (uint, error)
}
