package model

import "time"

// ComponentConnection supplies both daily monitoring and archived reports.
// PostgreSQL connections continue to use OperationsDatabase.
type ComponentConnection struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	Kind              string    `gorm:"size:24;index;not null" json:"kind"`
	Name              string    `gorm:"size:128;not null" json:"name"`
	Endpoint          string    `gorm:"size:512" json:"endpoint"`
	Username          string    `gorm:"size:128" json:"username"`
	SecretEncrypted   string    `gorm:"type:text" json:"-"`
	Source            string    `gorm:"size:24" json:"source"`
	EnvironmentDigest string    `gorm:"size:64" json:"-"`
	ServerID          uint      `json:"serverId"`
	Enabled           bool      `json:"enabled"`
	OptionsJSON       string    `gorm:"type:jsonb;not null;default:'{}'" json:"optionsJSON"`
	Revision          uint      `gorm:"default:1" json:"revision"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// CollectionSample is a disposable last inspection result. Formal reports have
// their own immutable snapshot and are never deleted by the 48-hour cleanup.
type CollectionSample struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TargetType   string    `gorm:"size:24;index:idx_collection_target,priority:1" json:"targetType"`
	TargetID     uint      `gorm:"index:idx_collection_target,priority:2" json:"targetId"`
	Revision     uint      `json:"revision"`
	Lane         string    `gorm:"size:24;not null;default:''" json:"lane"`
	SourceDigest string    `gorm:"size:64" json:"-"`
	CollectedAt  time.Time `gorm:"index" json:"collectedAt"`
	ResultJSON   string    `gorm:"type:jsonb;not null" json:"resultJSON"`
}

// MonitoringState keeps the latest bounded snapshot separately from the
// compact 48-hour time series. A database lease prevents duplicate collectors.
type MonitoringState struct {
	ID              uint       `gorm:"primaryKey" json:"-"`
	TargetType      string     `gorm:"size:24;uniqueIndex:idx_monitoring_target,priority:1" json:"targetType"`
	TargetID        uint       `gorm:"uniqueIndex:idx_monitoring_target,priority:2" json:"targetId"`
	Lane            string     `gorm:"size:24;uniqueIndex:idx_monitoring_target,priority:3" json:"lane"`
	SourceDigest    string     `gorm:"size:64" json:"-"`
	Revision        uint       `json:"revision"`
	Enabled         bool       `json:"enabled"`
	IntervalSeconds int        `json:"intervalSeconds"`
	LastAttemptAt   *time.Time `json:"lastAttemptAt"`
	LastSuccessAt   *time.Time `json:"lastSuccessAt"`
	NextAttemptAt   time.Time  `gorm:"index" json:"nextAttemptAt"`
	LeaseUntil      *time.Time `json:"-"`
	LeaseToken      string     `gorm:"size:128" json:"-"`
	Failures        int        `json:"-"`
	LastError       string     `gorm:"type:text" json:"lastError"`
	ResultJSON      string     `gorm:"type:jsonb;default:null" json:"-"`
}

type ManagedService struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	ServerID      uint      `gorm:"index;not null" json:"serverId"`
	Name          string    `gorm:"size:128" json:"name"`
	Mode          string    `gorm:"size:16" json:"mode"`
	Unit          string    `gorm:"size:128" json:"unit"`
	RunUser       string    `gorm:"size:64" json:"runUser"`
	Directory     string    `gorm:"size:512" json:"directory"`
	StartScript   string    `gorm:"size:512" json:"startScript"`
	Identity      string    `gorm:"size:512" json:"identity"`
	LogPath       string    `gorm:"size:512" json:"logPath"`
	OperatorsJSON string    `gorm:"type:jsonb;not null;default:'[]'" json:"operatorsJSON"`
	CreatedBy     string    `gorm:"size:64" json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ServiceOperation struct {
	TargetName        string     `gorm:"size:128" json:"targetName"`
	TargetEndpoint    string     `gorm:"size:512" json:"targetEndpoint"`
	ConfigurationJSON string     `gorm:"type:jsonb;default:null" json:"configurationJSON"`
	ID                uint       `gorm:"primaryKey" json:"id"`
	ServiceID         uint       `gorm:"index" json:"serviceId"`
	ServerID          uint       `gorm:"index" json:"serverId"`
	Action            string     `gorm:"size:16" json:"action"`
	Status            string     `gorm:"size:24;index" json:"status"`
	Operator          string     `gorm:"size:64" json:"operator"`
	Message           string     `gorm:"type:text" json:"message"`
	StartedAt         time.Time  `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt"`
}
