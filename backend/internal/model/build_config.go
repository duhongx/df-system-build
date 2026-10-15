package model

import "time"

type BuildConfig struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Name           string    `gorm:"uniqueIndex;size:64;not null" json:"name"`
	Category       string    `gorm:"size:20;not null" json:"category"`      // java / vue
	InstallCommand string    `gorm:"size:512" json:"installCommand"`        // e.g. yarn install
	BuildCommand   string    `gorm:"size:512;not null" json:"buildCommand"` // e.g. gradle clean build -x test
	ArtifactDir    string    `gorm:"size:256" json:"artifactDir"`           // e.g. build/libs, dist
	EnvVars        string    `gorm:"type:text" json:"envVars"`              // local mode, KEY=VALUE per line
	Description    string    `gorm:"size:255" json:"description"`
	Status         string    `gorm:"size:20;default:online" json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
