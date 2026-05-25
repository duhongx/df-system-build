package model

import "time"

type PipelineStage struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	PipelineID      uint       `gorm:"index;not null" json:"pipelineId"`
	StageCode       string     `gorm:"size:32;not null" json:"stageCode"`
	StageName       string     `gorm:"size:64;not null" json:"stageName"`
	StageOrder      int        `gorm:"not null" json:"stageOrder"`
	Status          string     `gorm:"size:20;default:PENDING" json:"status"` // PENDING/RUNNING/SUCCESS/FAILED/SKIPPED
	Command         string     `gorm:"type:text" json:"command"`
	StartTime       *time.Time `json:"startTime"`
	EndTime         *time.Time `json:"endTime"`
	DurationSeconds *int       `json:"durationSeconds"`
	ExitCode        *int       `json:"exitCode"`
	ErrorMessage    string     `gorm:"type:text" json:"errorMessage"`
}
