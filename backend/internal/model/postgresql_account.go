package model

import "time"

// Password overrides belong to this platform installation's business database.
type PostgreSQLAccountPassword struct {
	Username          string    `gorm:"primaryKey;size:63" json:"username"`
	PasswordEncrypted string    `gorm:"type:text;not null" json:"-"`
	UpdatedAt         time.Time `json:"updatedAt"`
}
