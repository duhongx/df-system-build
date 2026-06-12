// Package store is dfctl-web's business persistence layer. Schema is
// applied automatically on Open; the Store interface keeps a single
// surface area for both production (SQLite) and tests (in-memory or
// temp-file SQLite).
package store

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Get* methods when the requested row does
// not exist. Handlers map this to HTTP 404.
var ErrNotFound = errors.New("store: not found")

// DeploymentFilter narrows ListDeployments. ProjectID is intentionally
// absent — single-project is a permanent simplification.
type DeploymentFilter struct {
	Component string
	Status    string
	Limit     int
	Offset    int
}

// Store is the abstract persistence contract.
type Store interface {
	// Singleton settings.
	GetDeploymentSettings(ctx context.Context) (*DeploymentSettings, error)
	UpdateDeploymentSettings(ctx context.Context, s *DeploymentSettings) error

	GetNetworkSettings(ctx context.Context) (*NetworkSettings, error)
	UpdateNetworkSettings(ctx context.Context, s *NetworkSettings) error

	// UpdateGlobalConfig atomically applies a partial PUT against the
	// deployment_settings + network_settings + env_settings tables in
	// one SQLite transaction. Used by /api/global-config to avoid
	// half-applied state when SQLite errors mid-flight.
	UpdateGlobalConfig(ctx context.Context, u GlobalConfigUpdate) error

	// Env (k-v map).
	ListEnv(ctx context.Context) ([]*EnvEntry, error)
	UpsertEnv(ctx context.Context, entries []*EnvEntry) error // replace-all semantics
	DeleteEnv(ctx context.Context, key string) error

	// Enabled components (ordered).
	ListEnabledComponents(ctx context.Context) ([]*EnabledComponent, error)
	ReplaceEnabledComponents(ctx context.Context, names []string) error

	// Hosts (CRUD).
	ListHosts(ctx context.Context) ([]*HostSpec, error)
	GetHost(ctx context.Context, id int64) (*HostSpec, error)
	CreateHost(ctx context.Context, h *HostSpec) error
	UpdateHost(ctx context.Context, h *HostSpec) error
	DeleteHost(ctx context.Context, id int64) error
	// CountTargetsByHost reports how many component_targets rows
	// reference the given host_id, plus the distinct component names
	// that reference it. Used by DELETE /api/hosts/{id} to surface a
	// 409 with a "this host is still bound to: redis, postgresql, ..."
	// message instead of silently CASCADE-deleting target rows.
	CountTargetsByHost(ctx context.Context, hostID int64) (int64, []string, error)

	// Component → host targeting.
	ListAllTargets(ctx context.Context) ([]*ComponentTargets, error)
	GetTargets(ctx context.Context, component string) (*ComponentTargets, error)
	ReplaceTargets(ctx context.Context, component string, hostIDs []int64) error
	// ReplaceTargetsForOwner is the virtualcomponent-scoped write.
	// Pipeline components shared across multiple virtualcomponents
	// (containerd, kube-lb, node) must use this so peers' selections
	// don't get overwritten.
	ReplaceTargetsForOwner(ctx context.Context, component, ownerVC string, hostIDs []int64) error
	// ReplaceTargetsForOwners writes several (component, owner_vc,
	// host_ids) tuples in a single SQLite transaction. Used by the
	// targets / deployment handlers when expanding one virtual
	// component fans out to multiple pipeline rows — without this,
	// a SQLite error halfway through could leave the targets table
	// reflecting only some of the pipeline rows the operator
	// intended.
	ReplaceTargetsForOwners(ctx context.Context, batch []OwnerComponentHosts) error
	// ListTargetsForOwner returns the per-component host_ids selected
	// by `ownerVC`. Used by /api/components and /api/targets to render
	// "what did this virtualcomponent pick?" without leaking peers'
	// choices.
	ListTargetsForOwner(ctx context.Context, ownerVC string) (map[string][]int64, error)
	// ListLegacyOwnerTargets returns rows that still have owner_vc=''
	// — leftovers from the pre-v4 schema. The web layer reads these
	// on boot and rewrites them with the inferred owner.
	ListLegacyOwnerTargets(ctx context.Context) ([]*ComponentTargets, error)

	// Component deploy-state machine. GetComponentDeployState returns
	// a synthetic not_deployed state (not ErrNotFound) when no row
	// exists, so callers never special-case "never deployed".
	GetComponentDeployState(ctx context.Context, component string) (*ComponentDeployState, error)
	ListComponentDeployStates(ctx context.Context) ([]*ComponentDeployState, error)
	SetComponentDeployState(ctx context.Context, component, status string, deploymentID int64) error

	// Component overrides (k-> map JSON).
	ListOverrides(ctx context.Context) ([]*ComponentOverride, error)
	GetOverride(ctx context.Context, name string) (*ComponentOverride, error)
	UpsertOverride(ctx context.Context, o *ComponentOverride) error
	DeleteOverride(ctx context.Context, name string) error

	// Deployments + logs (already used by runtime).
	CreateDeployment(ctx context.Context, d *Deployment) error
	GetDeployment(ctx context.Context, id int64) (*Deployment, error)
	UpdateDeployment(ctx context.Context, d *Deployment) error
	ListDeployments(ctx context.Context, f DeploymentFilter) ([]*Deployment, error)
	CountDeployments(ctx context.Context, f DeploymentFilter) (int64, error)
	PruneOldDeployments(ctx context.Context, keep int) (int64, error)

	AppendDeploymentLog(ctx context.Context, log *DeploymentLog) error
	ListDeploymentLogs(ctx context.Context, deploymentID, afterSeq int64, limit int) ([]*DeploymentLog, error)
	GetDeploymentLogsTail(ctx context.Context, deploymentID int64, n int) ([]*DeploymentLog, error)

	Close() error
}
