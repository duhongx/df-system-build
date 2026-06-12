package repository

import (
	"context"
	"errors"
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
	err := r.db.WithContext(ctx).Order("id desc").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
