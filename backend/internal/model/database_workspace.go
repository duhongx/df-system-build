package model

import "time"

// Workspace drafts belong to a platform user. Query results and credentials are
// intentionally outside the workspace document.
type DatabaseWorkspace struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	Owner     string    `gorm:"size:128;uniqueIndex;not null" json:"-"`
	StateJSON string    `gorm:"type:jsonb;not null" json:"-"`
	Revision  uint      `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type DatabaseSavedQuery struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Owner          string    `gorm:"size:128;index;not null" json:"-"`
	Name           string    `gorm:"size:128;not null" json:"name"`
	DatabaseID     uint      `gorm:"index" json:"databaseId"`
	Schema         string    `gorm:"size:128" json:"schema"`
	SQL            string    `gorm:"type:text;not null" json:"sql"`
	ParametersJSON string    `gorm:"type:jsonb;not null" json:"parametersJSON"`
	Revision       uint      `json:"revision"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
