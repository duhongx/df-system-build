package model

import "time"

type NotificationWebhook struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:128;not null" json:"name"`
	Type            string    `gorm:"size:20;not null" json:"type"` // dingtalk / wecom
	WebhookURL      string    `gorm:"size:512;not null" json:"webhookUrl"`
	Secret          string    `gorm:"size:256" json:"secret"`
	NotifyOnSuccess bool      `gorm:"default:true" json:"notifyOnSuccess"`
	NotifyOnFailure bool      `gorm:"default:true" json:"notifyOnFailure"`
	Enabled         bool      `gorm:"default:true" json:"enabled"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
