package model

import "time"

type ArtifactVersion struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	VersionNo       string    `gorm:"uniqueIndex;size:64;not null" json:"versionNo"`
	SourceType      string    `gorm:"size:32;index" json:"sourceType"`
	SourceLabel     string    `gorm:"size:64" json:"sourceLabel"`
	Status          string    `gorm:"size:20;index" json:"status"`
	LocalDir        string    `gorm:"size:1024" json:"localDir"`
	TargetPath      string    `gorm:"size:1024" json:"targetPath"`
	RemotePath      string    `gorm:"size:1024" json:"remotePath"`
	Count           int       `json:"count"`
	DeployableCount int       `json:"deployableCount"`
	MatchedCount    int       `json:"matchedCount"`
	ValidCount      int       `json:"validCount"`
	InvalidCount    int       `json:"invalidCount"`
	SkippedCount    int       `json:"skippedCount"`
	UnmatchedCount  int       `json:"unmatchedCount"`
	Error           string    `gorm:"type:text" json:"error"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func (ArtifactVersion) TableName() string {
	return "artifact_versions"
}

type ArtifactVersionItem struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	VersionNo          string    `gorm:"size:64;index;not null" json:"versionNo"`
	FileName           string    `gorm:"size:255;index" json:"fileName"`
	FileType           string    `gorm:"size:32" json:"fileType"`
	FileSizeBytes      int64     `json:"fileSizeBytes"`
	SHA256             string    `gorm:"size:64;index" json:"sha256"`
	AppID              uint      `gorm:"index" json:"appId"`
	AppName            string    `gorm:"size:128" json:"appName"`
	AppType            string    `gorm:"size:20" json:"appType"`
	MatchStatus        string    `gorm:"size:20;index" json:"matchStatus"`
	ValidateStatus     string    `gorm:"size:20;index" json:"validateStatus"`
	Deployable         bool      `gorm:"index" json:"deployable"`
	PackageVersionJSON string    `gorm:"type:text" json:"packageVersionJson"`
	StatusReason       string    `gorm:"type:text" json:"statusReason"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

func (ArtifactVersionItem) TableName() string {
	return "artifact_version_items"
}
