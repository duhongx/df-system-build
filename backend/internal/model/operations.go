package model

import "time"

// Monitoring and inspection have independent lifecycles from release SQL.
type HostMonitorConfig struct {
	ServerID        uint       `gorm:"primaryKey" json:"serverId"`
	MetricsURL      string     `gorm:"type:text" json:"metricsURL"`
	Enabled         bool       `json:"enabled"`
	IntervalSeconds int        `gorm:"default:60" json:"intervalSeconds"`
	SSHFingerprint  string     `gorm:"size:128" json:"sshFingerprint"`
	LastAttemptAt   *time.Time `json:"lastAttemptAt"`
	LastSampleAt    *time.Time `json:"lastSampleAt"`
	LastError       string     `gorm:"type:text" json:"lastError"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}
type HostMetricSample struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ServerID    uint      `gorm:"index:idx_host_metric_time,priority:1" json:"serverId"`
	CollectedAt time.Time `gorm:"index:idx_host_metric_time,priority:2" json:"collectedAt"`
	MetricsJSON string    `gorm:"type:jsonb;not null" json:"-"`
}
type OperationsDatabase struct {
	Source            string     `gorm:"size:24" json:"source"`
	EnvironmentDigest string     `gorm:"size:64" json:"-"`
	BackupServerID    uint       `json:"backupServerId"`
	BackupDirectory   string     `gorm:"type:text" json:"backupDirectory"`
	ArchiveDirectory  string     `gorm:"type:text" json:"archiveDirectory"`
	ID                uint       `gorm:"primaryKey" json:"id"`
	Name              string     `gorm:"size:128;not null" json:"name"`
	Host              string     `gorm:"size:255;not null" json:"host"`
	Port              int        `gorm:"default:5432" json:"port"`
	Database          string     `gorm:"size:128;not null" json:"database"`
	Username          string     `gorm:"size:128;not null" json:"username"`
	PasswordEncrypted string     `gorm:"type:text" json:"-"`
	SSLMode           string     `gorm:"size:24;default:require" json:"sslMode"`
	ServerID          uint       `gorm:"index" json:"serverId"`
	LogEnabled        bool       `json:"logEnabled"`
	LogDirectory      string     `gorm:"type:text" json:"logDirectory"`
	LogFormat         string     `gorm:"size:16;default:csv" json:"logFormat"`
	LogTimezone       string     `gorm:"size:64;default:Asia/Shanghai" json:"logTimezone"`
	LogPrefix         string     `gorm:"type:text" json:"logPrefix"`
	SlowThresholdMs   int        `gorm:"default:5000" json:"slowThresholdMs"`
	LookbackHours     int        `gorm:"default:24" json:"lookbackHours"`
	LogStartAt        *time.Time `json:"logStartAt"`
	LastLogSyncAt     *time.Time `json:"lastLogSyncAt"`
	LogError          string     `gorm:"type:text" json:"logError"`
	LogCapability     string     `gorm:"type:text" json:"logCapability"`
	Revision          uint       `gorm:"default:1" json:"revision"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}
type DatabaseLogCursor struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	DatabaseID   uint      `gorm:"uniqueIndex:idx_database_log_file,priority:1" json:"databaseId"`
	FileIdentity string    `gorm:"size:128;uniqueIndex:idx_database_log_file,priority:2" json:"fileIdentity"`
	Path         string    `gorm:"type:text" json:"path"`
	Generation   int       `json:"generation"`
	Offset       int64     `json:"offset"`
	HeadHash     string    `gorm:"size:64" json:"headHash"`
	HeadLength   int       `json:"headLength"`
	LastSize     int64     `json:"lastSize"`
	LastModified int64     `json:"lastModified"`
	LastError    string    `gorm:"type:text" json:"lastError"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
type DatabaseLogEvent struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	DatabaseID   uint      `gorm:"index:idx_db_event_time,priority:1;index:idx_db_event_type_time,priority:1" json:"databaseId"`
	SourceKey    string    `gorm:"size:160;uniqueIndex" json:"-"`
	Type         string    `gorm:"size:24;index:idx_db_event_type_time,priority:2" json:"type"`
	EventTime    time.Time `gorm:"index:idx_db_event_time,priority:2;index:idx_db_event_type_time,priority:3" json:"eventTime"`
	DurationMs   float64   `json:"durationMs"`
	Phase        string    `gorm:"size:16" json:"phase"`
	DBName       string    `gorm:"size:128" json:"dbName"`
	Username     string    `gorm:"size:128" json:"username"`
	PID          int       `json:"pid"`
	Client       string    `gorm:"size:255" json:"client"`
	Application  string    `gorm:"type:text" json:"application"`
	SQLState     string    `gorm:"size:16" json:"sqlState"`
	SQL          string    `gorm:"type:text" json:"sql"`
	Message      string    `gorm:"type:text" json:"message"`
	Detail       string    `gorm:"type:text" json:"detail"`
	Hint         string    `gorm:"type:text" json:"hint"`
	Context      string    `gorm:"type:text" json:"context"`
	Raw          string    `gorm:"type:text" json:"raw,omitempty"`
	SourcePath   string    `gorm:"type:text" json:"sourcePath"`
	SourceOffset int64     `json:"sourceOffset"`
	Fingerprint  string    `gorm:"size:64;index" json:"fingerprint"`
	CreatedAt    time.Time `json:"createdAt"`
}
type InspectionPlan struct {
	CommandsJSON string     `gorm:"type:jsonb;default:null" json:"commandsJSON"`
	ID           uint       `gorm:"primaryKey" json:"id"`
	Name         string     `gorm:"size:128;not null" json:"name"`
	Enabled      bool       `json:"enabled"`
	DailyTime    string     `gorm:"size:5" json:"dailyTime"`
	Timezone     string     `gorm:"size:64;default:Asia/Shanghai" json:"timezone"`
	TargetsJSON  string     `gorm:"type:jsonb;not null" json:"targetsJSON"`
	ServicesJSON string     `gorm:"type:jsonb;not null;default:'[]'" json:"servicesJSON"`
	NextRunAt    *time.Time `gorm:"index" json:"nextRunAt"`
	CreatedBy    string     `gorm:"size:64" json:"createdBy"`
	LastError    string     `gorm:"type:text" json:"lastError"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}
type InspectionRun struct {
	CollectionMode     string     `gorm:"size:24" json:"collectionMode"`
	ReportSnapshotJSON string     `gorm:"type:jsonb;default:null" json:"-"`
	LeaseOwner         string     `gorm:"size:64" json:"-"`
	LeaseUntil         *time.Time `gorm:"index" json:"-"`
	ID                 uint       `gorm:"primaryKey" json:"id"`
	PlanID             uint       `gorm:"index" json:"planId"`
	ScheduleKey        string     `gorm:"size:128;uniqueIndex;default:null" json:"-"`
	Name               string     `gorm:"size:128" json:"name"`
	Status             string     `gorm:"size:24;index" json:"status"`
	Trigger            string     `gorm:"size:16" json:"trigger"`
	Operator           string     `gorm:"size:64" json:"operator"`
	TotalTargets       int        `json:"totalTargets"`
	CompletedTargets   int        `json:"completedTargets"`
	WarningCount       int        `json:"warningCount"`
	CriticalCount      int        `json:"criticalCount"`
	ErrorCount         int        `json:"errorCount"`
	CancelRequested    bool       `json:"cancelRequested"`
	Message            string     `gorm:"type:text" json:"message"`
	HTMLStatus         string     `gorm:"size:16" json:"htmlStatus"`
	PDFStatus          string     `gorm:"size:16" json:"pdfStatus"`
	ReportError        string     `gorm:"type:text" json:"reportError"`
	StartedAt          *time.Time `json:"startedAt"`
	FinishedAt         *time.Time `json:"finishedAt"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}
type InspectionTargetRun struct {
	ExpectedJSON    string     `gorm:"type:jsonb;default:null" json:"expectedJSON"`
	ManualJSON      string     `gorm:"type:jsonb;default:null" json:"manualJSON"`
	ServerEndpoint  string     `gorm:"size:512" json:"serverEndpoint"`
	ServicesJSON    string     `gorm:"type:jsonb;not null;default:'[]'" json:"servicesJSON"`
	ID              uint       `gorm:"primaryKey" json:"id"`
	RunID           uint       `gorm:"index" json:"runId"`
	Type            string     `gorm:"size:16" json:"type"`
	TargetID        uint       `json:"targetId"`
	ServerID        uint       `json:"serverId"`
	Name            string     `gorm:"size:128" json:"name"`
	Endpoint        string     `gorm:"size:512" json:"endpoint"`
	ConfigRevision  uint       `json:"configRevision"`
	Status          string     `gorm:"size:24;index" json:"status"`
	Stage           string     `gorm:"size:64" json:"stage"`
	Message         string     `gorm:"type:text" json:"message"`
	RemoteDirectory string     `gorm:"type:text" json:"-"`
	ProgressJSON    string     `gorm:"type:text" json:"progressJSON"`
	ResultJSON      string     `gorm:"type:jsonb;default:null" json:"resultJSON"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	HeartbeatAt     *time.Time `json:"heartbeatAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}
