package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	var tableOID, objectOID, indexedTableOID int64
	var tableSchema, tableName, objectKind, indexedTableSchema, indexedTableName string
	var hasIndexMetadata, valid, ready, live bool
	// Keep the individual facts instead of collapsing missing metadata, a name
	// conflict and an invalid index into one false value and a guessed diagnosis.
	err = conn.QueryRowContext(probe, `SELECT
	 COALESCE(t.oid::bigint, 0), COALESCE(ns.nspname, ''), COALESCE(t.relname, ''),
	 COALESCE(c.oid::bigint, 0), COALESCE(c.relkind::text, ''),
	 i.indexrelid IS NOT NULL, COALESCE(i.indrelid::bigint, 0),
	 COALESCE(indexed_ns.nspname, ''), COALESCE(indexed_table.relname, ''),
	 COALESCE(i.indisvalid, false), COALESCE(i.indisready, false), COALESCE(i.indislive, false)
	 FROM (SELECT 1) AS singleton
	 LEFT JOIN pg_catalog.pg_class t ON t.oid = pg_catalog.to_regclass($1)
	 LEFT JOIN pg_catalog.pg_namespace ns ON ns.oid = t.relnamespace
	 LEFT JOIN pg_catalog.pg_class c ON c.relnamespace = t.relnamespace AND c.relname = $2
	 LEFT JOIN pg_catalog.pg_index i ON i.indexrelid = c.oid
	 LEFT JOIN pg_catalog.pg_class indexed_table ON indexed_table.oid = i.indrelid
	 LEFT JOIN pg_catalog.pg_namespace indexed_ns ON indexed_ns.oid = indexed_table.relnamespace`, table, index.Idxname).Scan(
		&tableOID, &tableSchema, &tableName, &objectOID, &objectKind,
		&hasIndexMetadata, &indexedTableOID, &indexedTableSchema, &indexedTableName,
		&valid, &ready, &live,
	)
	if err != nil {
		return fmt.Errorf("无法确认索引 %s 的状态：查询数据库元数据失败: %w", index.Idxname, err)
	}
	if tableOID == 0 {
		return fmt.Errorf("校验时未能解析目标表 %s，因此无法核实索引 %s 的状态；这不代表同名索引不存在", table, index.Idxname)
	}
	targetTable := quoteIdent(tableSchema) + "." + quoteIdent(tableName)
	indexName := quoteIdent(tableSchema) + "." + quoteIdent(index.Idxname)
	if objectOID == 0 {
		return fmt.Errorf("已定位目标表 %s，但校验时未在 schema %s 中查到同名对象 %s，无法确认索引状态；请核对执行时的 schema 或对象是否发生变化", targetTable, quoteIdent(tableSchema), quoteIdent(index.Idxname))
	}
	if objectKind != "i" && objectKind != "I" {
		kind := map[string]string{"r": "普通表", "p": "分区表", "v": "视图", "m": "物化视图", "S": "序列", "f": "外部表", "c": "复合类型", "t": "TOAST 表"}[objectKind]
		if kind == "" {
			kind = fmt.Sprintf("其他对象（relkind=%s）", objectKind)
		}
		return fmt.Errorf("同名对象 %s 已存在，但它是%s，并非索引", indexName, kind)
	}
	if !hasIndexMetadata {
		return fmt.Errorf("索引对象 %s 已存在，但未查到对应的 pg_index 元数据，无法确认其所属表和有效性", indexName)
	}
	if indexedTableOID != tableOID {
		actualTable := fmt.Sprintf("OID=%d", indexedTableOID)
		if indexedTableName != "" {
			actualTable = quoteIdent(indexedTableSchema) + "." + quoteIdent(indexedTableName)
		}
		return fmt.Errorf("索引 %s 已存在，但属于表 %s，当前 SQL 的目标表是 %s", indexName, actualTable, targetTable)
	}
	var invalidStates []string
	if !valid {
		invalidStates = append(invalidStates, "当前无效（indisvalid=false）")
	}
	if !ready {
		invalidStates = append(invalidStates, "尚未就绪（indisready=false）")
	}
	if !live {
		invalidStates = append(invalidStates, "已标记为正在删除（indislive=false）")
	}
	if len(invalidStates) > 0 {
		return fmt.Errorf("索引 %s 已存在，且属于目标表 %s，但%s；请核实其构建或删除状态", indexName, targetTable, strings.Join(invalidStates, "、"))
	}
	return nil
}
