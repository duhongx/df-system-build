package repository

import (
	"time"

	"df-build-server/internal/model"

	"gorm.io/gorm"
)

// FinalizeOrphanedRuns marks any Deployment still in RUNNING/PENDING/DEPLOYING
// state as FAILED. Called once on startup so a process crash never leaves a run
// stuck "in flight" forever (supports CP-1's no-dangling-run invariant).
func FinalizeOrphanedRuns(db *gorm.DB) (int64, error) {
	now := time.Now()
	res := db.Model(&model.Deployment{}).
		Where("status IN ?", []string{"RUNNING", "PENDING", "DEPLOYING", "running", "pending"}).
		Updates(map[string]any{
			"status":        "FAILED",
			"error_summary": "process exited unexpectedly while the run was in flight",
			"ended_at":      &now,
		})
	return res.RowsAffected, res.Error
}
