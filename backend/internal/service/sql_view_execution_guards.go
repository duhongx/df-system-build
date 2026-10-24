package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func viewTaskBusy(status string) bool {
	switch status {
	case "ANALYZING", "PRECHECKING", "EXECUTING", "RESTORING", "VERIFYING":
		return true
	}
	return false
}

func ensureSQLViewDependencyTaskCanRestore(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) error {
	if task.Status == "SUCCESS" {
		return fmt.Errorf("任务已执行成功，无需恢复视图")
	}
	if len(items) == 0 {
		return fmt.Errorf("任务没有可恢复的依赖视图")
	}
	switch task.Status {
	case "FAILED", "RESTORE_FAILED", "VERIFY_FAILED", "UNKNOWN":
		return ensureSQLViewDependencyTaskHasPlan(task, items)
	default:
		return fmt.Errorf("仅任务执行失败、恢复失败、校验失败或结果待核实时可恢复视图")
	}
}

// A claimed phase remains exclusive until its terminal state has been stored.
func settleViewTaskPhase(id uint, phase, username string) {
	status, message := "FAILED", "任务未完成，请查看错误后重新处理"
	if phase == "EXECUTING" || phase == "RESTORING" || phase == "VERIFYING" {
		status, message = "UNKNOWN", "操作未完成，数据库结果待核实；请核实目标对象并使用备份恢复，不能直接重放变更"
	}
	result := repository.DB.Model(&model.SQLViewDependencyTask{}).Where("id = ? AND status = ?", id, phase).Updates(map[string]any{"status": status, "execute_message": message, "operator": username, "outcome_uncertain": status == "UNKNOWN"})
	if err := result.Error; err != nil {
		// Startup recovery will retain the uncertainty if the platform DB is unavailable.
		log.Printf("保存视图任务 %d 中断状态失败: %v", id, err)
	}
	if result.Error == nil && result.RowsAffected == 1 && status == "UNKNOWN" {
		if err := repository.DB.Model(&model.SQLViewDependencyChange{}).Where("task_id = ? AND status IN ?", id, []string{"RUNNING", "APPLIED"}).Updates(map[string]any{"status": "UNKNOWN", "error_message": message}).Error; err != nil {
			log.Printf("保存视图任务 %d 字段中断状态失败: %v", id, err)
		}
	}
}

func viewTimeouts(lock, statement string) (string, string, time.Duration, error) {
	if lock == "" {
		lock = "3s"
	}
	if statement == "" {
		statement = "10min"
	}
	ld, lok := parseSQLTimeoutDuration(lock)
	sd, sok := parseSQLTimeoutDuration(statement)
	if !lok || !sok || ld <= 0 || sd <= 0 || sd > time.Duration(1<<63-1)-5*time.Second {
		return "", "", 0, fmt.Errorf("锁等待和语句超时必须为正数，例如 3s、10min")
	}
	return normalizeSQLTimeoutLiteral(lock, ""), normalizeSQLTimeoutLiteral(statement, ""), sd + 5*time.Second, nil
}

func guardedViewConnection(ctx context.Context, db *sql.DB, lock, statement string) (*sql.Conn, func(), time.Duration, error) {
	lock, statement, timeout, err := viewTimeouts(lock, statement)
	if err != nil {
		return nil, nil, 0, err
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := db.Conn(connectCtx)
	if err != nil {
		return nil, nil, 0, err
	}
	cleanup := func() {
		// Never return transaction state or SET changes to the shared pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	}
	if _, err := conn.ExecContext(connectCtx, "SELECT set_config('lock_timeout', $1, false), set_config('statement_timeout', $2, false)", lock, statement); err != nil {
		cleanup()
		return nil, nil, 0, err
	}
	return conn, cleanup, timeout, nil
}

type sqlViewOutcomeUnknownError struct{ err error }

func (e sqlViewOutcomeUnknownError) Error() string { return "执行结果待核实: " + e.err.Error() }
func (e sqlViewOutcomeUnknownError) Unwrap() error { return e.err }

type sqlViewStepCommittedError struct{ err error }

func (e sqlViewStepCommittedError) Error() string { return e.err.Error() }
func (e sqlViewStepCommittedError) Unwrap() error { return e.err }
