package model

import "time"

type RemoteServer struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	Name                string     `gorm:"uniqueIndex;size:64;not null" json:"name"`
	Host                string     `gorm:"size:128;not null" json:"host"`
	Port                int        `gorm:"default:22" json:"port"`
	Username            string     `gorm:"size:64;not null" json:"username"`
	AuthType            string     `gorm:"size:20;not null" json:"authType"` // password / ssh_key
	CredentialEncrypted string     `gorm:"type:text" json:"-"`              // AES-256 encrypted
	Status              string     `gorm:"size:20;default:offline" json:"status"` // online / offline
	LastCheckTime       *time.Time `json:"lastCheckTime"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}
