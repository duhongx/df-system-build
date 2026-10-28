package model

import "time"

type DatabaseQueryRecord struct {
	ParametersJSON string    `gorm:"type:jsonb;default:null" json:"parametersJSON"`
	ID             uint      `gorm:"primaryKey" json:"id"`
	DatabaseID     uint      `gorm:"index:idx_db_query_time,priority:1" json:"databaseId"`
	Revision       uint      `json:"revision"`
	Target         string    `gorm:"size:512" json:"target"`
	Schema         string    `gorm:"size:128" json:"schema"`
	SQL            string    `gorm:"type:text" json:"sql"`
	Kind           string    `gorm:"size:16" json:"kind"`
	Status         string    `gorm:"size:20" json:"status"`
	Error          string    `gorm:"type:text" json:"error"`
	SQLState       string    `gorm:"size:10" json:"sqlState"`
	Rows           int64     `json:"rows"`
	HasMore        bool      `json:"hasMore"`
	DurationMs     float64   `json:"durationMs"`
	Operator       string    `gorm:"size:64" json:"operator"`
	StartedAt      time.Time `gorm:"index:idx_db_query_time,priority:2" json:"startedAt"`
	FinishedAt     time.Time `json:"finishedAt"`
}
