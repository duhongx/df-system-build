package model

import "time"

// ConfigItem stores configuration templates like Dockerfiles, K8s YAML, scripts
type ConfigItem struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:128;not null" json:"name"`
	Code        string    `gorm:"uniqueIndex;size:64;not null" json:"code"` // e.g. dockerfile-java, dockerfile-web, deployment-java, app-sh-java
	Category    string    `gorm:"size:32;not null" json:"category"`         // dockerfile / k8s / script
	ContentType string    `gorm:"size:20;not null" json:"contentType"`      // text / yaml / shell
	Content     string    `gorm:"type:text;not null" json:"content"`        // Template content with ${var} placeholders
	Description string    `gorm:"size:512" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
