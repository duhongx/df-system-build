package service

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type SQLMetadataInspector struct {
	db *sql.DB
}

type TableStats struct {
	SchemaName    string
	TableName     string
	TotalBytes    int64
	EstimatedRows int64
}

type ColumnInfo struct {
	SchemaName                string
	TableName                 string
	ColumnName                string
	DataType                  string
	UDTName                   string
	CharacterMaximumLength    int64
	HasCharacterMaximumLength bool
	NumericPrecision          int64
	HasNumericPrecision       bool
	NumericScale              int64
	HasNumericScale           bool
	IsNullable                bool
	ColumnDefault             string
}

func NewSQLMetadataInspector(db *sql.DB) *SQLMetadataInspector {
	return &SQLMetadataInspector{db: db}
}

func (i *SQLMetadataInspector) GetTableStats(ctx context.Context, schemaName, tableName string) (TableStats, error) {
	if i == nil || i.db == nil {
		return TableStats{}, fmt.Errorf("PostgreSQL 连接未初始化")
	}
	var stats TableStats
	err := i.db.QueryRowContext(ctx, `
SELECT n.nspname,
       c.relname,
       pg_total_relation_size(c.oid),
       COALESCE(c.reltuples, 0)::bigint
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relname = $2
  AND c.relkind IN ('r', 'p')`, schemaName, tableName).Scan(
		&stats.SchemaName,
		&stats.TableName,
		&stats.TotalBytes,
		&stats.EstimatedRows,
	)
	return stats, err
}

func (i *SQLMetadataInspector) GetColumnInfo(ctx context.Context, schemaName, tableName, columnName string) (ColumnInfo, error) {
	if i == nil || i.db == nil {
		return ColumnInfo{}, fmt.Errorf("PostgreSQL 连接未初始化")
	}
	var info ColumnInfo
	var charLen sql.NullInt64
	var precision sql.NullInt64
	var scale sql.NullInt64
	var columnDefault sql.NullString
	var nullable string
	err := i.db.QueryRowContext(ctx, `
SELECT table_schema,
       table_name,
       column_name,
       data_type,
       udt_name,
       character_maximum_length,
       numeric_precision,
       numeric_scale,
       is_nullable,
       column_default
FROM information_schema.columns
WHERE table_schema = $1
  AND table_name = $2
  AND column_name = $3`, schemaName, tableName, columnName).Scan(
		&info.SchemaName,
		&info.TableName,
		&info.ColumnName,
		&info.DataType,
		&info.UDTName,
		&charLen,
		&precision,
		&scale,
		&nullable,
		&columnDefault,
	)
	if err != nil {
		return ColumnInfo{}, err
	}
	if charLen.Valid {
		info.CharacterMaximumLength = charLen.Int64
		info.HasCharacterMaximumLength = true
	}
	if precision.Valid {
		info.NumericPrecision = precision.Int64
		info.HasNumericPrecision = true
	}
	if scale.Valid {
		info.NumericScale = scale.Int64
		info.HasNumericScale = true
	}
	info.IsNullable = strings.EqualFold(nullable, "YES")
	if columnDefault.Valid {
		info.ColumnDefault = columnDefault.String
	}
	return info, nil
}

func classifyColumnTypeChangeWithMetadata(column ColumnInfo, targetType string, hasUsing bool) RiskAnalysis {
	targetType = strings.TrimSpace(targetType)
	reasons := make([]string, 0, 2)
	level := "WARN"

	if oldLen, newLen, ok := compareVarcharLengths(column, targetType); ok {
		switch {
		case newLen > oldLen:
			level = "LOW"
			reasons = append(reasons, fmt.Sprintf("varchar 长度扩容: %d -> %d", oldLen, newLen))
		case newLen == oldLen:
			level = "LOW"
			reasons = append(reasons, fmt.Sprintf("varchar 长度未变化: %d", oldLen))
		default:
			reasons = append(reasons, fmt.Sprintf("varchar 长度缩容可能截断数据: %d -> %d", oldLen, newLen))
		}
	} else {
		fromType := displayColumnType(column)
		if fromType == "" {
			fromType = "未知类型"
		}
		if targetType == "" {
			targetType = "未知目标类型"
		}
		reasons = append(reasons, fmt.Sprintf("字段类型从 %s 变更为 %s，可能触发表重写或转换失败", fromType, targetType))
	}

	if hasUsing {
		level = "WARN"
		reasons = append(reasons, "USING 表达式会逐行转换数据，请确认转换逻辑和执行窗口")
	}

	return RiskAnalysis{SQLType: "ALTER_COLUMN_TYPE", RiskLevel: level, RiskReason: strings.Join(reasons, "；")}
}

func compareVarcharLengths(column ColumnInfo, targetType string) (int64, int64, bool) {
	if !isColumnVarchar(column) || !column.HasCharacterMaximumLength || !isVarcharType(targetType) {
		return 0, 0, false
	}
	newLen, ok := parseVarcharLength(targetType)
	if !ok {
		return 0, 0, false
	}
	return column.CharacterMaximumLength, newLen, true
}

func isColumnVarchar(column ColumnInfo) bool {
	dataType := strings.ToLower(strings.TrimSpace(column.DataType))
	udtName := strings.ToLower(strings.TrimSpace(column.UDTName))
	return dataType == "character varying" || dataType == "varchar" || udtName == "varchar"
}

func parseVarcharLength(typeName string) (int64, bool) {
	re := regexp.MustCompile(`(?i)\b(?:varchar|character\s+varying)\s*\(\s*(\d+)\s*\)`)
	matches := re.FindStringSubmatch(typeName)
	if len(matches) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(matches[1], 10, 64)
	return n, err == nil
}

func displayColumnType(column ColumnInfo) string {
	if isColumnVarchar(column) && column.HasCharacterMaximumLength {
		return fmt.Sprintf("varchar(%d)", column.CharacterMaximumLength)
	}
	if strings.TrimSpace(column.DataType) != "" {
		return strings.TrimSpace(column.DataType)
	}
	return strings.TrimSpace(column.UDTName)
}
