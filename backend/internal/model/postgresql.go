package model

import "time"

type SQLChangeFile struct {
	DatabaseTarget   string     `gorm:"type:text;not null;default:''" json:"databaseTarget,omitempty"`
	ID               uint       `gorm:"primaryKey" json:"id"`
	BatchID          uint       `gorm:"index" json:"batchId"`
	BatchSortNo      int        `json:"batchSortNo"`
	SystemCode       string     `gorm:"size:64" json:"systemCode"`
	Environment      string     `gorm:"size:64" json:"environment"`
	SchemaName       string     `gorm:"size:64" json:"schemaName"`
	SchemaConfigured bool       `json:"schemaConfigured"`
	Version          string     `gorm:"size:64" json:"version"`
	GroupSortNo      int        `json:"groupSortNo"`
	FileName         string     `gorm:"size:255" json:"fileName"`
	FileContent      string     `gorm:"type:text" json:"fileContent"`
	ImportMessage    string     `gorm:"type:text" json:"importMessage"`
	ExecuteStatus    string     `gorm:"size:24;index" json:"executeStatus"`
	ExecuteMessage   string     `gorm:"type:text" json:"executeMessage"`
	ExecuteUser      string     `gorm:"size:64" json:"executeUser"`
	CreatedUser      string     `gorm:"size:64" json:"createdUser"`
	ExecutionBatchID uint       `gorm:"index" json:"executionBatchId"`
	ExecuteTime      *time.Time `json:"executeTime"`
	IsDeleted        bool       `gorm:"index" json:"isDeleted"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type SQLChangeBatch struct {
	StopRequested     bool                     `json:"stopRequested"`
	StopUser          string                   `gorm:"size:64" json:"stopUser"`
	StopTime          *time.Time               `json:"stopTime"`
	HistoryIncomplete bool                     `gorm:"-" json:"historyIncomplete,omitempty"`
	FilteredFiles     []SQLExecutionFileFilter `gorm:"-" json:"filteredFiles,omitempty"`
	FileIDsJSON       string                   `gorm:"type:text" json:"-"`
	ID                uint                     `gorm:"primaryKey" json:"id"`
	BatchName         string                   `gorm:"size:255" json:"batchName"`
	ExecuteStatus     string                   `gorm:"size:24;index" json:"executeStatus"`
	ExecuteMessage    string                   `gorm:"type:text" json:"executeMessage"`
	ExecuteUser       string                   `gorm:"size:64" json:"executeUser"`
	ExecuteTime       *time.Time               `json:"executeTime"`
	TotalFiles        int                      `json:"totalFiles"`
	SuccessFiles      int                      `json:"successFiles"`
	FailedFiles       int                      `json:"failedFiles"`
	SkippedFiles      int                      `json:"skippedFiles"`
	IsDeleted         bool                     `gorm:"index" json:"isDeleted"`
	CreatedAt         time.Time                `json:"createdAt"`
	UpdatedAt         time.Time                `json:"updatedAt"`
}

type SQLExecutionFileFilter struct {
	ID       uint   `json:"id"`
	FileName string `json:"fileName"`
	Reason   string `json:"reason"`
}

type SQLChangeStatement struct {
	ResultOrigin        string     `gorm:"-" json:"resultOrigin,omitempty"`
	ID                  uint       `gorm:"primaryKey" json:"id"`
	FileID              uint       `gorm:"index" json:"fileId"`
	LineNumber          int        `json:"lineNumber"`
	SQLContent          string     `gorm:"type:text" json:"sqlContent"`
	SQLType             string     `gorm:"size:64" json:"sqlType"`
	RiskLevel           string     `gorm:"size:24" json:"riskLevel"`
	RiskReason          string     `gorm:"type:text" json:"riskReason"`
	EstimatedRows       int64      `json:"estimatedRows"`
	CanRunInTransaction bool       `json:"canRunInTransaction"`
	ExecutionStrategy   string     `gorm:"size:32" json:"executionStrategy"`
	ExecuteStatus       string     `gorm:"size:24;index" json:"executeStatus"`
	SkipKind            string     `gorm:"size:24" json:"skipKind"`
	ExecuteMessage      string     `gorm:"type:text" json:"executeMessage"`
	ExecuteUser         string     `gorm:"size:64" json:"executeUser"`
	SQLState            string     `gorm:"size:16" json:"sqlState"`
	AffectedRows        int64      `json:"affectedRows"`
	DurationMs          int64      `json:"durationMs"`
	ExecuteTime         *time.Time `json:"executeTime"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

// SQLStatementEdit retains the original SQL and failure when a pending release
// statement is corrected. Saving an edit never executes business SQL.
type SQLStatementEdit struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	FileID         uint      `gorm:"index" json:"fileId"`
	StatementID    uint      `gorm:"index" json:"statementId"`
	BeforeSQL      string    `gorm:"type:text" json:"beforeSql"`
	AfterSQL       string    `gorm:"type:text" json:"afterSql"`
	BeforeStatus   string    `gorm:"size:24" json:"beforeStatus"`
	BeforeMessage  string    `gorm:"type:text" json:"beforeMessage"`
	BeforeSQLState string    `gorm:"size:16" json:"beforeSqlState"`
	Editor         string    `gorm:"size:64" json:"editor"`
	CreatedAt      time.Time `json:"createdAt"`
}

// One execution attempt or explicit action. Completed records are never reused
// by retry, editing, manual skipping, file deletion or overwrite.
type SQLStatementRecord struct {
	SourceStatementID uint       `gorm:"index" json:"sourceStatementId"`
	ID                uint       `gorm:"primaryKey" json:"id"`
	BatchID           uint       `gorm:"index" json:"batchId"`
	FileID            uint       `gorm:"index" json:"fileId"`
	StatementID       uint       `gorm:"index" json:"statementId"`
	FileName          string     `gorm:"size:255" json:"fileName"`
	SchemaName        string     `gorm:"size:64;index" json:"schemaName"`
	DatabaseHost      string     `gorm:"size:255" json:"databaseHost"`
	DatabasePort      string     `gorm:"size:16" json:"databasePort"`
	DatabaseName      string     `gorm:"size:128" json:"databaseName"`
	DatabaseUser      string     `gorm:"size:64" json:"databaseUser"`
	SQLHash           string     `gorm:"size:64;index" json:"sqlHash"`
	SQLContent        string     `gorm:"type:text" json:"sqlContent"`
	LineNumber        int        `json:"lineNumber"`
	Action            string     `gorm:"size:32;index" json:"action"`
	SkipKind          string     `gorm:"size:24;index" json:"skipKind"`
	Status            string     `gorm:"size:24;index" json:"status"`
	Submitted         *bool      `json:"submitted"`
	Message           string     `gorm:"type:text" json:"message"`
	SQLState          string     `gorm:"size:16" json:"sqlState"`
	Operator          string     `gorm:"size:64" json:"operator"`
	StartedAt         time.Time  `gorm:"index" json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt"`
	AffectedRows      int64      `json:"affectedRows"`
	DurationMs        int64      `json:"durationMs"`
	BeforeStatus      string     `gorm:"size:24" json:"beforeStatus"`
	BeforeMessage     string     `gorm:"type:text" json:"beforeMessage"`
	BeforeSQLState    string     `gorm:"size:16" json:"beforeSqlState"`
	BeforeOperator    string     `gorm:"size:64" json:"beforeOperator"`
	BeforeTime        *time.Time `json:"beforeTime"`
}

type SQLExecutionFileSnapshot struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	BatchID        uint   `gorm:"uniqueIndex:idx_sql_batch_file_snapshot;index" json:"batchId"`
	FileID         uint   `gorm:"uniqueIndex:idx_sql_batch_file_snapshot" json:"fileId"`
	FileJSON       string `gorm:"type:text" json:"-"`
	StatementsJSON string `gorm:"type:text" json:"-"`
}

type SQLViewBackup struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	FileID       uint      `gorm:"index" json:"fileId"`
	StatementID  uint      `gorm:"index" json:"statementId"`
	SchemaName   string    `gorm:"size:64;index" json:"schemaName"`
	ViewName     string    `gorm:"size:128;index" json:"viewName"`
	Definition   string    `gorm:"type:text" json:"definition"`
	DropSQL      string    `gorm:"type:text" json:"dropSql"`
	CreateSQL    string    `gorm:"type:text" json:"createSql"`
	BackupReason string    `gorm:"type:text" json:"backupReason"`
	CreatedAt    time.Time `json:"createdAt"`
}

type SQLViewDependencyTask struct {
	OutcomeUncertain bool       `json:"outcomeUncertain"`
	ID               uint       `gorm:"primaryKey" json:"id"`
	SchemaName       string     `gorm:"size:64;index" json:"schemaName"`
	TableName        string     `gorm:"size:128;index" json:"tableName"`
	ColumnName       string     `gorm:"size:128;index" json:"columnName"`
	AlterSQL         string     `gorm:"type:text" json:"alterSql"`
	Status           string     `gorm:"size:32;index" json:"status"`
	ExecutionMode    string     `gorm:"size:24;index" json:"executionMode"`
	RiskLevel        string     `gorm:"size:24" json:"riskLevel"`
	RiskReason       string     `gorm:"type:text" json:"riskReason"`
	LockTimeout      string     `gorm:"size:32" json:"lockTimeout"`
	StatementTimeout string     `gorm:"size:32" json:"statementTimeout"`
	Operator         string     `gorm:"size:64" json:"operator"`
	ExecuteMessage   string     `gorm:"type:text" json:"executeMessage"`
	AnalyzedAt       *time.Time `json:"analyzedAt"`
	ExecutedAt       *time.Time `json:"executedAt"`
	IsDeleted        bool       `gorm:"index" json:"isDeleted"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type SQLViewDependencyChange struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TaskID       uint      `gorm:"index" json:"taskId"`
	ChangeOrder  int       `gorm:"index" json:"changeOrder"`
	LineNumber   int       `json:"lineNumber"`
	SchemaName   string    `gorm:"size:64;index" json:"schemaName"`
	TableName    string    `gorm:"size:128;index" json:"tableName"`
	ColumnName   string    `gorm:"size:128;index" json:"columnName"`
	AlterSQL     string    `gorm:"type:text" json:"alterSql"`
	Status       string    `gorm:"size:32;index" json:"status"`
	ErrorMessage string    `gorm:"type:text" json:"errorMessage"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type SQLViewDependencyItem struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	TaskID             uint      `gorm:"index" json:"taskId"`
	ObjectSchema       string    `gorm:"size:64;index" json:"objectSchema"`
	ObjectName         string    `gorm:"size:128;index" json:"objectName"`
	ObjectKind         string    `gorm:"size:8" json:"objectKind"`
	Depth              int       `json:"depth"`
	DropOrder          int       `json:"dropOrder"`
	RestoreOrder       int       `json:"restoreOrder"`
	Definition         string    `gorm:"type:text" json:"definition"`
	OwnerName          string    `gorm:"size:128" json:"ownerName"`
	GrantsJSON         string    `gorm:"type:text" json:"grantsJson"`
	CommentsJSON       string    `gorm:"type:text" json:"commentsJson"`
	IndexesJSON        string    `gorm:"type:text" json:"indexesJson"`
	RulesJSON          string    `gorm:"type:text" json:"rulesJson"`
	TriggersJSON       string    `gorm:"type:text" json:"triggersJson"`
	OptionsJSON        string    `gorm:"type:text" json:"optionsJson"`
	BackupHash         string    `gorm:"size:128;index" json:"backupHash"`
	DropSQL            string    `gorm:"type:text" json:"dropSql"`
	CreateSQL          string    `gorm:"type:text" json:"createSql"`
	RestoreRefreshSQL  string    `gorm:"type:text" json:"restoreRefreshSql"`
	RestoreOwnerSQL    string    `gorm:"type:text" json:"restoreOwnerSql"`
	RestoreGrantsSQL   string    `gorm:"type:text" json:"restoreGrantsSql"`
	RestoreCommentsSQL string    `gorm:"type:text" json:"restoreCommentsSql"`
	RestoreIndexesSQL  string    `gorm:"type:text" json:"restoreIndexesSql"`
	RestoreRulesSQL    string    `gorm:"type:text" json:"restoreRulesSql"`
	RestoreTriggersSQL string    `gorm:"type:text" json:"restoreTriggersSql"`
	VerifySQL          string    `gorm:"type:text" json:"verifySql"`
	Status             string    `gorm:"size:32;index" json:"status"`
	ErrorMessage       string    `gorm:"type:text" json:"errorMessage"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type SQLViewDependencyStep struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	TaskID       uint       `gorm:"index" json:"taskId"`
	StepName     string     `gorm:"size:64;index" json:"stepName"`
	StepOrder    int        `json:"stepOrder"`
	SQLContent   string     `gorm:"type:text" json:"sqlContent"`
	Status       string     `gorm:"size:32;index" json:"status"`
	ErrorMessage string     `gorm:"type:text" json:"errorMessage"`
	StartedAt    *time.Time `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}
