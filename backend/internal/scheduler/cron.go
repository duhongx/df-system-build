package scheduler

import (
	"strconv"
	"time"

	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
)

// StartCronJobs starts background scheduled tasks
func StartCronJobs() {
	go logRetentionJob()
	logger.Log.Info("Cron jobs started")
}

// logRetentionJob runs daily to clean up old logs
func logRetentionJob() {
	for {
		// Run at 2:00 AM daily
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 2, 0, 0, 0, now.Location())
		time.Sleep(time.Until(next))

		cleanLogs()
	}
}

func cleanLogs() {
	settingsRepo := repository.NewSettingsRepo()
	logRepo := repository.NewStageLogRepo()

	// Get retention days from settings
	retentionDays := 30
	if val, err := settingsRepo.GetByKey("log_retention_days"); err == nil {
		if days, err := strconv.Atoi(val); err == nil && days > 0 {
			retentionDays = days
		}
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	deleted, err := logRepo.DeleteOlderThan(cutoff)
	if err != nil {
		logger.Log.Errorf("Log retention cleanup failed: %v", err)
		return
	}

	if deleted > 0 {
		logger.Log.Infof("Log retention: deleted %d log entries older than %d days", deleted, retentionDays)
	}
}

// RunCleanupNow triggers an immediate log cleanup (for testing/manual use)
func RunCleanupNow() {
	cleanLogs()
}
