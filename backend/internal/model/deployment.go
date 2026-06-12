package model

import "time"

// Deployment-management GORM models.
//
// These back the engine's store.Store interface (internal/deploy/engine/store).
// Host inventory is NOT modeled here — deployment targets reference the existing
// Server table (model.Server) by server_id, so there is no duplicate host table.
//
// Table names are prefixed `deployment_` to avoid collisions with the build
// subsystem.

// DeploymentSettings is the singleton connection/tuning row (id=1).
type DeploymentSettings struct {
	ID                    uint      `gorm:"primaryKey" json:"id"`
	SSHUser               string    `gorm:"size:64" json:"sshUser"`
	SSHPrivateKeyPath     string    `gorm:"size:512" json:"sshPrivateKeyPath"`
	SSHPort               int       `gorm:"default:22" json:"sshPort"`
	RemoteRoot            string    `gorm:"size:512" json:"remoteRoot"`
	RetainDeployments     int       `gorm:"default:20" json:"retainDeployments"`
	DefaultTimeoutSeconds int       `gorm:"default:1800" json:"defaultTimeoutSeconds"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

func (DeploymentSettings) TableName() string { return "deployment_settings" }

// DeploymentNetworkSettings is the singleton network row (id=1).
type DeploymentNetworkSettings struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	VIP              string    `gorm:"size:64" json:"vip"`
	ServiceCIDR      string    `gorm:"size:64" json:"serviceCidr"`
	ClusterCIDR      string    `gorm:"size:64" json:"clusterCidr"`
	NodeCIDRMaskSize int       `gorm:"default:24" json:"nodeCidrMaskSize"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (DeploymentNetworkSettings) TableName() string { return "deployment_network_settings" }

// DeploymentEnvEntry is one key in the global env map (replace-all semantics).
type DeploymentEnvEntry struct {
	Key       string    `gorm:"primaryKey;size:128" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (DeploymentEnvEntry) TableName() string { return "deployment_env_entries" }

// DeploymentEnabledComponent is one row of the ordered enabled-component list.
type DeploymentEnabledComponent struct {
	Name      string    `gorm:"primaryKey;size:64" json:"name"`
	Position  int       `gorm:"default:0" json:"position"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (DeploymentEnabledComponent) TableName() string { return "deployment_enabled_components" }

// DeploymentComponentTarget binds a component (optionally scoped to an owning
// virtual component) to a Server. ServerID references model.Server.ID.
type DeploymentComponentTarget struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	ComponentName string    `gorm:"size:64;index:idx_dct_comp_owner_server,unique" json:"componentName"`
	OwnerVC       string    `gorm:"size:64;index:idx_dct_comp_owner_server,unique;default:''" json:"ownerVc"`
	ServerID      uint      `gorm:"index:idx_dct_comp_owner_server,unique" json:"serverId"`
	CreatedAt     time.Time `json:"createdAt"`
}

func (DeploymentComponentTarget) TableName() string { return "deployment_component_targets" }

// DeploymentComponentState records the deploy state machine per component
// (or virtual component name).
type DeploymentComponentState struct {
	ComponentName    string    `gorm:"primaryKey;size:64" json:"componentName"`
	Status           string    `gorm:"size:20;index;default:not_deployed" json:"status"`
	LastDeploymentID int64     `json:"lastDeploymentId"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (DeploymentComponentState) TableName() string { return "deployment_component_states" }

// DeploymentComponentOverride stores arbitrary render parameters as JSON.
type DeploymentComponentOverride struct {
	ComponentName string    `gorm:"primaryKey;size:64" json:"componentName"`
	ParamsJSON    string    `gorm:"type:jsonb" json:"paramsJson"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (DeploymentComponentOverride) TableName() string { return "deployment_component_overrides" }

// Deployment is one deploy/rollback run.
type Deployment struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	TaskType        string     `gorm:"size:20;index" json:"taskType"` // deploy | rollback | dryrun
	TargetComponent string     `gorm:"size:64;index" json:"targetComponent"`
	TargetHost      string     `gorm:"size:128" json:"targetHost"`
	DryRun          bool       `json:"dryRun"`
	Status          string     `gorm:"size:20;index" json:"status"`
	StartedAt       *time.Time `json:"startedAt"`
	EndedAt         *time.Time `json:"endedAt"`
	ErrorSummary    string     `gorm:"type:text" json:"errorSummary"`
	ScopeKind       string     `gorm:"size:20" json:"scopeKind"`
	Phase           string     `gorm:"size:20" json:"phase"`
	DurationMS      int64      `json:"durationMs"`
	RunDir          string     `gorm:"size:512" json:"runDir"`
	TriggerUser     string     `gorm:"size:64" json:"triggerUser"`
	CreatedAt       time.Time  `json:"createdAt"`
}

func (Deployment) TableName() string { return "deployments" }

// DeploymentRunLog is one log line for a Deployment.
// (Named with the RunLog suffix to avoid colliding with the legacy
// model.DeployLog type, which is removed in Task 14.)
type DeploymentRunLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	DeploymentID int64     `gorm:"index:idx_drl_dep_seq" json:"deploymentId"`
	Sequence     int64     `gorm:"index:idx_drl_dep_seq" json:"sequence"`
	Timestamp    time.Time `json:"timestamp"`
	Component    string    `gorm:"size:64" json:"component"`
	Host         string    `gorm:"size:128" json:"host"`
	Phase        string    `gorm:"size:20" json:"phase"`
	ActionName   string    `gorm:"size:128" json:"actionName"`
	ActionType   string    `gorm:"size:64" json:"actionType"`
	Status       string    `gorm:"size:20" json:"status"`
	Detail       string    `gorm:"type:text" json:"detail"`
	IsError      bool      `json:"isError"`
}

func (DeploymentRunLog) TableName() string { return "deployment_logs" }

// OfflineBundle records the currently installed offline bundle metadata.
type OfflineBundle struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	BundleVersion   string    `gorm:"size:64;index" json:"bundleVersion"`
	ArchiveSHA256   string    `gorm:"size:64" json:"archiveSha256"`
	FileCount       int       `json:"fileCount"`
	ManifestSummary string    `gorm:"type:jsonb" json:"manifestSummary"`
	InstalledBy     string    `gorm:"size:64" json:"installedBy"`
	InstalledAt     time.Time `json:"installedAt"`
}

func (OfflineBundle) TableName() string { return "deployment_offline_bundles" }
