package store

import "time"

// DeploymentSettings is the singleton row in `deployment_settings`. It
// captures the connection-level knobs the dfctl engine needs (SSH user,
// remote root) plus operational tuning (retention, timeouts).
//
// RemoteRoot is the single deploy root path (e.g. /opt/his-deploy). All
// machines (control node + deploy targets) share the same layout under
// it: resources/offline, cloudhis (runtime products), bin. It is
// written into the generated config.yml as cluster.remote_root.
type DeploymentSettings struct {
	SSHUser               string    `json:"ssh_user"`
	// SSHPrivateKeyPath is the absolute path to the private key the
	// control node uses to dial deploy hosts. Empty falls back to
	// ~/.ssh/id_rsa at runtime so day-1 setups don't require any
	// extra config.
	SSHPrivateKeyPath string `json:"ssh_private_key_path"`
	// SSHPort is the TCP port sshd listens on across deployment
	// hosts. Defaults to 22; operators only set this when their
	// security policy moves sshd to a non-standard port. The
	// host_check probe and the runtime SSH backend both read this.
	SSHPort               int       `json:"ssh_port"`
	RemoteRoot            string    `json:"remote_root"`
	RetainDeployments     int       `json:"retain_deployments"`
	DefaultTimeoutSeconds int       `json:"default_timeout_seconds"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// EnvEntry mirrors a single key in the legacy `env:` YAML block.
type EnvEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NetworkSettings is the singleton row in `network_settings`.
type NetworkSettings struct {
	VIP              string    `json:"vip"`
	ServiceCIDR      string    `json:"service_cidr"`
	ClusterCIDR      string    `json:"cluster_cidr"`
	NodeCIDRMaskSize int       `json:"node_cidr_mask_size"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// EnabledComponent is one row of the deploy_components list.
type EnabledComponent struct {
	Name      string    `json:"component_name"`
	Position  int       `json:"position"`
	UpdatedAt time.Time `json:"updated_at"`
}

// HostSpec is one row of `host_specs` — just an inventory entry. The
// "which components run here" decision is recorded in component_targets,
// not on the host itself.
type HostSpec struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Address   string            `json:"address"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ComponentTargets carries a component's selected host IDs. A
// component with zero targets simply has no hosts to deploy to until
// the operator picks some.
type ComponentTargets struct {
	ComponentName string  `json:"component_name"`
	HostIDs       []int64 `json:"host_ids"`
}

// OwnerComponentHosts is one (component, owner_vc, host_ids) tuple
// passed to ReplaceTargetsForOwners. Several of these together
// describe the full host selection a virtualcomponent wants to
// persist in one transaction.
type OwnerComponentHosts struct {
	Component string
	OwnerVC   string
	HostIDs   []int64
}

// ComponentOverride captures arbitrary parameters dfctl's render phase
// will splice into a component (replacing legacy `custom.yml` semantics).
// Params is left as map[string]any so nested YAML structures roundtrip
// without per-field plumbing.
type ComponentOverride struct {
	ComponentName string         `json:"component_name"`
	Params        map[string]any `json:"params"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// Deployment is one row of `deployments` — already wired by runtime.
type Deployment struct {
	ID              int64      `json:"id"`
	TaskType        string     `json:"task_type"`
	TargetComponent string     `json:"target_component"`
	TargetHost      string     `json:"target_host"`
	DryRun          bool       `json:"dry_run"`
	Status          string     `json:"status"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	ErrorSummary    string     `json:"error_summary"`
	ScopeKind       string     `json:"scope_kind"`
	Phase           string     `json:"phase,omitempty"`
	DurationMS      int64      `json:"duration_ms"`
	RunDir          string     `json:"run_dir,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Component deploy-state machine values. Tracked per
// deployments.target_component (a virtualcomponent name like
// kubernetes-master, or a single pipeline component name like redis).
const (
	// DeployStateNotDeployed: never deployed, or cleaned via a
	// successful rollback. This is the only state that allows a new
	// deploy.
	DeployStateNotDeployed = "not_deployed"
	// DeployStateDeployed: last deploy succeeded. Must rollback before
	// deploying again.
	DeployStateDeployed = "deployed"
	// DeployStateFailed: last deploy failed and likely left residue.
	// Operator must analyse + rollback (clean) before retrying — we
	// never allow a direct re-deploy on top of a failed run.
	DeployStateFailed = "failed"
)

// ComponentDeployState is one row of `component_deploy_state`. It
// records whether a component (identified the same way as
// deployments.target_component) is currently deployed, so the UI can
// show a badge and the deploy handler can enforce the
// "clean-before-redeploy" hard constraint.
type ComponentDeployState struct {
	ComponentName    string    `json:"component_name"`
	Status           string    `json:"status"`
	LastDeploymentID int64     `json:"last_deployment_id"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DeploymentLog is one row of `deployment_logs`.
type DeploymentLog struct {
	ID           int64     `json:"id"`
	DeploymentID int64     `json:"deployment_id"`
	Sequence     int64     `json:"sequence"`
	Timestamp    time.Time `json:"timestamp"`
	Component    string    `json:"component"`
	Host         string    `json:"host"`
	Phase        string    `json:"phase"`
	ActionName   string    `json:"action_name"`
	ActionType   string    `json:"action_type"`
	Status       string    `json:"status"`
	Detail       string    `json:"detail"`
	IsError      bool      `json:"is_error"`
}
