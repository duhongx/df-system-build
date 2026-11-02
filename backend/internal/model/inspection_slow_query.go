package model

import "time"

// InspectionSlowQuery is part of a formal report, not the disposable monitoring
// history. Source event cleanup cannot change a report's pagination or SQL.
type InspectionSlowQuery struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	RunID         uint      `gorm:"index:idx_report_slow_time,priority:1" json:"runId"`
	TargetRunID   uint      `gorm:"index:idx_report_slow_time,priority:2" json:"targetRunId"`
	SourceEventID uint      `json:"sourceEventId"`
	DatabaseName  string    `gorm:"size:128" json:"databaseName"`
	EventTime     time.Time `gorm:"index:idx_report_slow_time,priority:3,sort:desc" json:"eventTime"`
	DurationMs    float64   `json:"durationMs"`
	Phase         string    `gorm:"size:16" json:"phase"`
	Username      string    `gorm:"size:128" json:"username"`
	Client        string    `gorm:"size:255" json:"client"`
	SQL           string    `gorm:"type:text" json:"sql"`
	CollectedAt   time.Time `json:"collectedAt"`
}
