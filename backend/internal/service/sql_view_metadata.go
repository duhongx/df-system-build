package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"df-build-server/internal/model"
)

// Check the entire set in one round trip. The same information_schema visibility
// rules as GetColumnInfo apply, without issuing one query for every changed field.
func validateSQLViewDependencyColumns(ctx context.Context, db *sql.DB, changes []model.SQLViewDependencyChange) error {
	if len(changes) == 0 {
		return nil
	}
	type target struct {
		Schema string `json:"schema_name"`
		Table  string `json:"table_name"`
		Column string `json:"column_name"`
		Order  int    `json:"change_order"`
	}
	targets := make([]target, len(changes))
	for i, change := range changes {
		targets[i] = target{change.SchemaName, change.TableName, change.ColumnName, i + 1}
	}
	data, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	var missing target
	err = db.QueryRowContext(ctx, `
WITH targets AS (
  SELECT * FROM jsonb_to_recordset($1::jsonb)
    AS t(schema_name text, table_name text, column_name text, change_order integer)
)
SELECT t.schema_name, t.table_name, t.column_name
FROM targets t
WHERE NOT EXISTS (
  SELECT 1 FROM information_schema.columns c
  WHERE c.table_schema = t.schema_name
    AND c.table_name = t.table_name
    AND c.column_name = t.column_name
)
ORDER BY t.change_order
LIMIT 1`, string(data)).Scan(&missing.Schema, &missing.Table, &missing.Column)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("目标字段元数据检查失败: %w", err)
	}
	return fmt.Errorf("目标字段 %s.%s.%s 不存在或无法读取", missing.Schema, missing.Table, missing.Column)
}

// One lock probe for the whole batch. Object names remain case-sensitive, and
// relation OIDs are scoped to this database (OIDs can coincide across databases).
func validateSQLViewDependencyLocks(ctx context.Context, db *sql.DB, changes []model.SQLViewDependencyChange) error {
	if len(changes) == 0 {
		return nil
	}
	type target struct {
		Schema string `json:"schema_name"`
		Table  string `json:"table_name"`
	}
	targets := make([]target, len(changes))
	for i, change := range changes {
		targets[i] = target{change.SchemaName, change.TableName}
	}
	data, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	var schema, table string
	var waiting int
	err = db.QueryRowContext(ctx, `
WITH targets AS (
 SELECT DISTINCT schema_name, table_name FROM jsonb_to_recordset($1::jsonb)
 AS t(schema_name text, table_name text)
)
SELECT n.nspname, c.relname, count(*)
FROM pg_locks l JOIN pg_class c ON c.oid=l.relation
JOIN pg_namespace n ON n.oid=c.relnamespace
JOIN targets t ON t.schema_name=n.nspname AND t.table_name=c.relname
WHERE NOT l.granted AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database())
GROUP BY n.nspname, c.relname ORDER BY n.nspname, c.relname LIMIT 1`, string(data)).Scan(&schema, &table, &waiting)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("批量锁检查失败: %w", err)
	}
	return fmt.Errorf("目标表 %s.%s 当前存在 %d 个等待锁，建议稍后再执行", schema, table, waiting)
}
