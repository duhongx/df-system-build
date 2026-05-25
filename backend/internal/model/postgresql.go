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
