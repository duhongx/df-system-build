package model

import "time"

type Task struct {
	ID                  uint        `gorm:"primaryKey" json:"id"`
	TaskName            string      `gorm:"uniqueIndex;size:128;not null" json:"taskName"`
	ApplicationID       uint        `gorm:"not null" json:"applicationId"`
	Application         Application `gorm:"foreignKey:ApplicationID" json:"application,omitempty"`
	GitBranch           string      `gorm:"size:128;not null" json:"gitBranch"`
	BuildConfigID       uint        `gorm:"default:0" json:"buildConfigId"`
	BuildConfig         BuildConfig `gorm:"foreignKey:BuildConfigID" json:"buildConfig,omitempty"`
	DeployMode          string      `gorm:"size:32;default:deploy" json:"deployMode"` // deploy / upload_only / upload_and_deploy
	K8sNamespace        string      `gorm:"size:64" json:"k8sNamespace"`
	LastStatus          string      `gorm:"size:20" json:"lastStatus"`
	LastRunTime         *time.Time  `json:"lastRunTime"`
	LastDurationSeconds *int        `json:"lastDurationSeconds"`
	Enabled             bool        `gorm:"default:true" json:"enabled"`
	CreatedAt           time.Time   `json:"createdAt"`
	UpdatedAt           time.Time   `json:"updatedAt"`
}
