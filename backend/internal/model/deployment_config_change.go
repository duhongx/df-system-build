package model

import "time"

// Only editable values/differences are recorded; env and Secrets are excluded.
type DeploymentConfigChange struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	Namespace         string    `gorm:"size:63;index" json:"namespace"`
	DeploymentName    string    `gorm:"size:253;index" json:"deploymentName"`
	DeploymentUID     string    `gorm:"size:64" json:"deploymentUid"`
	ResourceVersion   string    `gorm:"size:64" json:"resourceVersion"`
	Operator          string    `gorm:"size:64" json:"operator"`
	ChangesJSON       string    `gorm:"type:text" json:"changesJson"`
	Status            string    `gorm:"size:24" json:"status"`
	Message           string    `gorm:"type:text" json:"message"`
	AppliedGeneration int64     `json:"appliedGeneration"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}
