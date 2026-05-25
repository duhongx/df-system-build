package model

import "time"

type User struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	Username           string    `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash       string    `gorm:"size:255;not null" json:"-"`
	Email              string    `gorm:"size:128" json:"email"`
	Phone              string    `gorm:"size:20" json:"phone"`
	Department         string    `gorm:"size:64" json:"department"`
	MustChangePassword bool      `gorm:"default:false" json:"mustChangePassword"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
