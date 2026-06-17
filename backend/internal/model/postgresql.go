package model

import "time"

type SQLChangeFile struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	BatchID        uint       `gorm:"index" json:"batchId"`
	BatchSortNo    int        `json:"batchSortNo"`
	SystemCode     string     `gorm:"size:64" json:"systemCode"`
	Environment    string     `gorm:"size:64" json:"environment"`
	SchemaName     string     `gorm:"size:64" json:"schemaName"`
	Version        string     `gorm:"size:64" json:"version"`
	GroupSortNo    int        `json:"groupSortNo"`
	FileName       string     `gorm:"size:255" json:"fileName"`
	FileContent    string     `gorm:"type:text" json:"fileContent"`
	ExecuteStatus  string     `gorm:"size:24;index" json:"executeStatus"`
	ExecuteMessage string     `gorm:"type:text" json:"executeMessage"`
	ExecuteUser    string     `gorm:"size:64" json:"executeUser"`
	ExecuteTime    *time.Time `json:"executeTime"`
	IsDeleted      bool       `gorm:"index" json:"isDeleted"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type SQLChangeBatch struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	BatchName      string     `gorm:"size:255" json:"batchName"`
	ExecuteStatus  string     `gorm:"size:24;index" json:"executeStatus"`
	ExecuteMessage string     `gorm:"type:text" json:"executeMessage"`
	ExecuteUser    string     `gorm:"size:64" json:"executeUser"`
	ExecuteTime    *time.Time `json:"executeTime"`
	TotalFiles     int        `json:"totalFiles"`
	SuccessFiles   int        `json:"successFiles"`
	FailedFiles    int        `json:"failedFiles"`
	SkippedFiles   int        `json:"skippedFiles"`
	IsDeleted      bool       `gorm:"index" json:"isDeleted"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type SQLChangeStatement struct {
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
	ExecuteMessage      string     `gorm:"type:text" json:"executeMessage"`
	SQLState            string     `gorm:"size:16" json:"sqlState"`
	AffectedRows        int64      `json:"affectedRows"`
	DurationMs          int64      `json:"durationMs"`
	ExecuteTime         *time.Time `json:"executeTime"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
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
	ID               uint       `gorm:"primaryKey" json:"id"`
	SchemaName       string     `gorm:"size:64;index" json:"schemaName"`
	TableName        string     `gorm:"size:128;index" json:"tableName"`
	ColumnName       string     `gorm:"size:128;index" json:"columnName"`
	AlterSQL         string     `gorm:"type:text" json:"alterSql"`
	Status           string     `gorm:"size:32;index" json:"status"`
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
