package model

import "time"

type StageLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	PipelineID uint      `gorm:"index;not null" json:"pipelineId"`
	StageID    uint      `gorm:"index;not null" json:"stageId"`
	LineNumber int       `gorm:"not null" json:"lineNumber"`
	Content    string    `gorm:"type:text" json:"content"`
	Stream     string    `gorm:"size:10" json:"stream"` // stdout / stderr
	Timestamp  time.Time `json:"timestamp"`
}
