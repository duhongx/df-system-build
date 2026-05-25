package model

import "time"

type Artifact struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	PipelineID    uint      `gorm:"index" json:"pipelineId"`
	PipelineNo    string    `gorm:"size:32" json:"pipelineNo"`
	AppName       string    `gorm:"size:128" json:"appName"`
	ArtifactName  string    `gorm:"size:128" json:"artifactName"`
	GitBranch     string    `gorm:"size:128" json:"gitBranch"`
	GitCommitHash string    `gorm:"size:64" json:"gitCommitHash"`
	UploadPath    string    `gorm:"size:512" json:"uploadPath"`
	UploadTargets string    `gorm:"size:512" json:"uploadTargets"`
	FileSizeBytes int64     `json:"fileSizeBytes"`
	DurationSeconds int    `json:"durationSeconds"`
	CreatedAt     time.Time `json:"createdAt"`
}
