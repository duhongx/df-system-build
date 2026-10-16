package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// Index names belong to the indexed table's schema, independent of the first
// schema in search_path. A same-named table or invalid index is not a usable index.
func validateReleaseSQLIndex(ctx context.Context, conn *sql.Conn, sqlText string) error {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || len(tree.Stmts) != 1 {
		return nil
	}
	index := tree.Stmts[0].Stmt.GetIndexStmt()
	if index == nil || index.Idxname == "" || index.Relation == nil {
		return nil
	}
	table := quoteIdent(index.Relation.Relname)
	if index.Relation.Schemaname != "" {
		table = quoteIdent(index.Relation.Schemaname) + "." + table
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var valid bool
	err = conn.QueryRowContext(probe, `SELECT COALESCE((
	 SELECT i.indisvalid AND i.indisready AND i.indislive AND i.indrelid = t.oid
	 FROM pg_class t JOIN pg_class c ON c.relnamespace = t.relnamespace
	 JOIN pg_index i ON i.indexrelid = c.oid
	 WHERE t.oid = to_regclass($1) AND c.relname = $2
	), false)`, table, index.Idxname).Scan(&valid)
	if err != nil {
		return fmt.Errorf("检查索引 %s 有效性失败: %w", index.Idxname, err)
	}
	if !valid {
		return fmt.Errorf("索引 %s 不存在、无效或不属于目标表，不能按“索引已存在”跳过；请核实后修复索引", index.Idxname)
	}
	return nil
}
