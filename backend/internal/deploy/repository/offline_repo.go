package repository

import (
	"context"
	"time"

	"df-build-server/internal/model"

	"gorm.io/gorm"
)

// OfflineRepo persists offline-bundle install metadata.
type OfflineRepo struct {
	db *gorm.DB
}

// NewOfflineRepo builds an OfflineRepo.
func NewOfflineRepo(db *gorm.DB) *OfflineRepo { return &OfflineRepo{db: db} }

// GetCurrent returns the most recently installed bundle, or nil if none.
func (r *OfflineRepo) GetCurrent(ctx context.Context) (*model.OfflineBundle, error) {
	var row model.OfflineBundle
	result := r.db.WithContext(ctx).Order("id desc").Limit(1).Find(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}

// RecordInstall appends a new installed-bundle row.
func (r *OfflineRepo) RecordInstall(ctx context.Context, b *model.OfflineBundle) error {
	if b.InstalledAt.IsZero() {
		b.InstalledAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(b).Error
}
