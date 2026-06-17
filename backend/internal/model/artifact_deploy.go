package model

import "time"

type ArtifactDeployBatch struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	DeployBatchNo   string     `gorm:"uniqueIndex;size:64;not null" json:"deployBatchNo"`
	VersionNo       string     `gorm:"size:64;index;not null" json:"versionNo"`
	Namespace       string     `gorm:"size:64;index" json:"namespace"`
	DeployMode      string     `gorm:"size:32;index" json:"deployMode"`
	Status          string     `gorm:"size:32;index" json:"status"`
	TriggerUser     string     `gorm:"size:64" json:"triggerUser"`
	TotalCount      int        `json:"totalCount"`
	SuccessCount    int        `json:"successCount"`
	FailedCount     int        `json:"failedCount"`
	RolledBackCount int        `json:"rolledBackCount"`
	ErrorMessage    string     `gorm:"type:text" json:"errorMessage"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (ArtifactDeployBatch) TableName() string {
	return "artifact_deploy_batches"
}

type ArtifactDeployRecord struct {
	ID                          uint       `gorm:"primaryKey" json:"id"`
	DeployBatchNo               string     `gorm:"size:64;index;not null" json:"deployBatchNo"`
	VersionNo                   string     `gorm:"size:64;index;not null" json:"versionNo"`
	ArtifactVersionItemID       uint       `gorm:"index" json:"artifactVersionItemId"`
	PipelineID                  uint       `gorm:"index" json:"pipelineId"`
	AppID                       uint       `gorm:"index" json:"appId"`
	AppName                     string     `gorm:"size:128;index" json:"appName"`
	AppCode                     string     `gorm:"size:64;index" json:"appCode"`
	AppType                     string     `gorm:"size:20" json:"appType"`
	VueRole                     string     `gorm:"size:20" json:"vueRole"`
	FileName                    string     `gorm:"size:255" json:"fileName"`
	Namespace                   string     `gorm:"size:64;index" json:"namespace"`
	DeploymentName              string     `gorm:"size:128;index" json:"deploymentName"`
	RuntimeVersionPath          string     `gorm:"size:512" json:"runtimeVersionPath"`
	BeforeImage                 string     `gorm:"size:512" json:"beforeImage"`
	AfterImage                  string     `gorm:"size:512" json:"afterImage"`
	PackageVersionJSON          string     `gorm:"type:text" json:"packageVersionJson"`
	BeforeBusinessVersionJSON   string     `gorm:"type:text" json:"beforeBusinessVersionJson"`
	AfterBusinessVersionJSON    string     `gorm:"type:text" json:"afterBusinessVersionJson"`
	RestoredBusinessVersionJSON string     `gorm:"type:text" json:"restoredBusinessVersionJson"`
	BuildStatus                 string     `gorm:"size:32;index" json:"buildStatus"`
	DeployStatus                string     `gorm:"size:32;index" json:"deployStatus"`
	VerifyStatus                string     `gorm:"size:32;index" json:"verifyStatus"`
	RollbackStatus              string     `gorm:"size:32;index" json:"rollbackStatus"`
	Status                      string     `gorm:"size:32;index" json:"status"`
	ErrorMessage                string     `gorm:"type:text" json:"errorMessage"`
	DeployedAt                  *time.Time `json:"deployedAt"`
	RolledBackAt                *time.Time `json:"rolledBackAt"`
	CreatedAt                   time.Time  `json:"createdAt"`
	UpdatedAt                   time.Time  `json:"updatedAt"`
}

func (ArtifactDeployRecord) TableName() string {
	return "artifact_deploy_records"
}
