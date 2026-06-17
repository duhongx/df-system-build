package model

import "time"

type DeploymentRuntimeVersion struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	Namespace           string    `gorm:"size:64;not null;uniqueIndex:idx_runtime_version_target" json:"namespace"`
	DeploymentName      string    `gorm:"size:128;not null;index;uniqueIndex:idx_runtime_version_target" json:"deploymentName"`
	AppID               uint      `gorm:"index" json:"appId"`
	AppName             string    `gorm:"size:128;index;uniqueIndex:idx_runtime_version_target" json:"appName"`
	AppCode             string    `gorm:"size:64;index" json:"appCode"`
	AppType             string    `gorm:"size:20" json:"appType"`
	VueRole             string    `gorm:"size:20" json:"vueRole"`
	RuntimeVersionPath  string    `gorm:"size:512;uniqueIndex:idx_runtime_version_target" json:"runtimeVersionPath"`
	Image               string    `gorm:"size:512" json:"image"`
	BusinessVersionJSON string    `gorm:"type:text" json:"businessVersionJson"`
	Status              string    `gorm:"size:32;index" json:"status"`
	ErrorMessage        string    `gorm:"type:text" json:"errorMessage"`
	SyncedAt            time.Time `json:"syncedAt"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

func (DeploymentRuntimeVersion) TableName() string {
	return "deployment_runtime_versions"
}
