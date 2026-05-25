package model

import "time"

type Pipeline struct {
	ID               uint            `gorm:"primaryKey" json:"id"`
	PipelineNo       string          `gorm:"uniqueIndex;size:32;not null" json:"pipelineNo"`
	TaskID           uint            `json:"taskId"`
	ApplicationID    uint            `json:"applicationId"`
	AppName          string          `gorm:"size:128;not null" json:"appName"`
	AppType          string          `gorm:"size:20" json:"appType"`
	GitRepo          string          `gorm:"size:512" json:"gitRepo"`
	GitBranch        string          `gorm:"size:128" json:"gitBranch"`
	GitCommitHash    string          `gorm:"size:64" json:"gitCommitHash"`
	GitCommitAuthor  string          `gorm:"size:64" json:"gitCommitAuthor"`
	GitCommitMessage string          `gorm:"type:text" json:"gitCommitMessage"`
	Status           string          `gorm:"size:20;not null;default:PENDING" json:"status"` // PENDING/RUNNING/IMAGE_READY/DEPLOYING/SUCCESS/FAILED/CANCELED
	TriggerUser      string          `gorm:"size:64;not null" json:"triggerUser"`
	BuilderImage     string          `gorm:"size:256" json:"builderImage"`
	ArtifactName     string          `gorm:"size:128" json:"artifactName"`
	UploadPath       string          `gorm:"size:512" json:"uploadPath"`
	UploadTargets    string          `gorm:"size:512" json:"uploadTargets"`
	BatchID          string          `gorm:"size:64;index" json:"batchId"`
	DeployMode       string          `gorm:"size:32" json:"deployMode"` // upload_only / upload_and_deploy
	ImageName        string          `gorm:"size:256" json:"imageName"` // Built Docker image name (e.g. registry/app:tag)
	K8sNamespace     string          `gorm:"size:64" json:"k8sNamespace"` // K8s namespace
	StartTime        *time.Time      `json:"startTime"`
	EndTime          *time.Time      `json:"endTime"`
	DurationSeconds  *int            `json:"durationSeconds"`
	ErrorStage       string          `gorm:"size:64" json:"errorStage"`
	ErrorMessage     string          `gorm:"type:text" json:"errorMessage"`
	Stages           []PipelineStage `gorm:"foreignKey:PipelineID" json:"stages,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
}
