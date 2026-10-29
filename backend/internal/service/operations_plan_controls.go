package service

import (
	"context"
	"df-build-server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// Existing schedules stay visible and can be stopped without changing their
// original scope or interrupting an already running inspection.
func (s *OperationsService) SetInspectionPlanEnabled(ctx context.Context, id uint, enabled bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.InspectionPlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		updates := map[string]any{"enabled": enabled, "last_error": ""}
		if enabled {
			next, err := NextInspectionTime(time.Now(), row.DailyTime, row.Timezone)
			if err != nil {
				return err
			}
			updates["next_run_at"] = next
		}
		return tx.Model(&row).Updates(updates).Error
	})
}
