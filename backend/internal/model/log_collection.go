package model

import "time"

// LogSource describes files on a managed host, not a log search/index service.
type LogSource struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128;not null" json:"name"`
	Category  string    `gorm:"size:32;not null" json:"category"`
	ServerID  uint      `gorm:"index;not null" json:"serverId"`
	Directory string    `gorm:"type:text;not null" json:"directory"`
	Pattern   string    `gorm:"type:text" json:"pattern"`
	Recursive bool      `json:"recursive"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type LogCollectionItem struct {
	Source      LogSource `json:"source"`
	Host        string    `json:"host"`
	Path        string    `json:"path"`
	ArchivePath string    `json:"archivePath"`
	Status      string    `json:"status"`
	Size        int64     `json:"size"`
	Bytes       int64     `json:"bytes"`
	ModifiedAt  time.Time `json:"modifiedAt"`
	Error       string    `json:"error"`
}

type LogCollectionJob struct {
	ID           uint                `gorm:"primaryKey" json:"id"`
	Status       string              `gorm:"size:24;index" json:"status"`
	Operator     string              `gorm:"size:128" json:"operator"`
	Message      string              `gorm:"type:text" json:"message"`
	Items        []LogCollectionItem `gorm:"serializer:json;type:text" json:"items"`
	ArchiveSize  int64               `json:"archiveSize"`
	TotalFiles   int                 `json:"totalFiles"`
	SuccessFiles int                 `json:"successFiles"`
	FailedFiles  int                 `json:"failedFiles"`
	CreatedAt    time.Time           `json:"createdAt"`
	FinishedAt   *time.Time          `json:"finishedAt"`
}
