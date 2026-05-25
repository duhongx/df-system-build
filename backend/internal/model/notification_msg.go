package model

import "time"

// NotificationMsg represents a system notification message
type NotificationMsg struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      string    `gorm:"size:32;not null;index" json:"type"` // build_complete / deploy_complete / deploy_failed / announcement
	Title     string    `gorm:"size:256;not null" json:"title"`
	Content   string    `gorm:"type:text" json:"content"`
	Level     string    `gorm:"size:20;default:info" json:"level"` // info / success / warning / error
	Read      bool      `gorm:"default:false" json:"read"`
	PipelineID uint     `gorm:"index" json:"pipelineId"`
	CreatedAt time.Time `json:"createdAt"`
}
