package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
)

// PgError.Error omits DETAIL, HINT and execution context. Keep those fields in
// the durable task/step record, including after transaction rollback.
func SQLExecutionErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg != nil {
		for _, field := range []struct{ name, value string }{
			{"SQLSTATE", pg.Code}, {"DETAIL", pg.Detail}, {"HINT", pg.Hint},
			{"SCHEMA", pg.SchemaName}, {"TABLE", pg.TableName}, {"COLUMN", pg.ColumnName},
			{"DATA TYPE", pg.DataTypeName}, {"CONSTRAINT", pg.ConstraintName},
			{"CONTEXT", pg.Where}, {"INTERNAL QUERY", pg.InternalQuery},
		} {
			if field.value != "" && !strings.Contains(message, field.name+": "+field.value) {
				message += "\n" + field.name + ": " + field.value
			}
		}
		if pg.Position > 0 {
			message += fmt.Sprintf("\nSQL 字符位置: %d", pg.Position)
		}
	}
	return message
}

// Keep the view API name for existing callers.
func SQLViewDependencyErrorMessage(err error) string { return SQLExecutionErrorMessage(err) }

func viewInspectionProgress(ctx context.Context, id uint, phase string) func(string) error {
	var last time.Time
	return func(message string) error {
		if !last.IsZero() && time.Since(last) < time.Second {
			return nil
		}
		result := repository.DB.WithContext(ctx).Model(&model.SQLViewDependencyTask{}).
			Where("id = ? AND status = ? AND is_deleted = false", id, phase).
			Update("execute_message", message)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("任务 #%d 的%s处理权已失效", id, phase)
		}
		last = time.Now()
		return nil
	}
}
