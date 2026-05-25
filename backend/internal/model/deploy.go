package model

import "time"

// DeployPlan represents a deployment plan
type DeployPlan struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:128" json:"name"`
	PackageDir  string    `gorm:"size:512" json:"packageDir"`
	Assignments string    `gorm:"type:text" json:"assignments"` // JSON: [{component, hosts, params}]
	Status      string    `gorm:"size:20;default:draft" json:"status"` // draft / executing / completed / failed
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ComponentAssignment represents a component's server assignment and params
type ComponentAssignment struct {
	Component string            `json:"component"`
	HostIDs   []uint            `json:"hostIds"`
	Hosts     []Server          `json:"-"`
	Params    map[string]string `json:"params"`
}

// GetAssignment returns the assignment for a component code
func (p *DeployPlan) GetAssignment(code string) *ComponentAssignment {
	// This will be populated by the service layer from JSON
	return nil
}

// DeployExecution represents a single deployment execution
type DeployExecution struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	PlanID     uint       `gorm:"index" json:"planId"`
	Status     string     `gorm:"size:20;default:RUNNING" json:"status"` // RUNNING / SUCCESS / FAILED / CANCELED
	StartTime  *time.Time `json:"startTime"`
	EndTime    *time.Time `json:"endTime"`
	Duration   int        `json:"duration"`
	Error      string     `gorm:"type:text" json:"error"`
	Components string     `gorm:"type:text" json:"components"` // JSON: [{code, status, error, duration}]
	CreatedAt  time.Time  `json:"createdAt"`
}

// DeployLog stores deployment logs (permanent)
type DeployLog struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ExecutionID uint      `gorm:"index" json:"executionId"`
	Component   string    `gorm:"size:32;index" json:"component"`
	Content     string    `gorm:"type:text" json:"content"`
	CreatedAt   time.Time `json:"createdAt"`
}

// EnvironmentInfo stores the deployment output (connection info)
type EnvironmentInfo struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ExecutionID uint      `gorm:"index" json:"executionId"`
	Component   string    `gorm:"size:32" json:"component"`
	Info        string    `gorm:"type:text" json:"info"` // JSON: {host, port, username, password, url, ...}
	CreatedAt   time.Time `json:"createdAt"`
}
