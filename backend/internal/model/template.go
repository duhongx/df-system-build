package model

import "time"

type Template struct {
	ID          uint              `gorm:"primaryKey" json:"id"`
	Name        string            `gorm:"size:128;not null" json:"name"`
	Code        string            `gorm:"uniqueIndex;size:32;not null" json:"code"`
	Category    string            `gorm:"size:20;not null" json:"category"` // Java / Vue / 工具
	Description string            `gorm:"size:512" json:"description"`
	Defaults    []TemplateDefault `gorm:"foreignKey:TemplateID" json:"defaults,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

type TemplateDefault struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	TemplateID uint   `gorm:"index;not null" json:"templateId"`
	Key        string `gorm:"size:64;not null" json:"key"`
	Value      string `gorm:"size:512" json:"value"`
}
