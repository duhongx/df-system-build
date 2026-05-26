package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	dmlAffectedRowsWarnThreshold  = 100_000
	dmlAffectedRowsBlockThreshold = 1_000_000
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

type ViewColumn struct {
	Name     string
	DataType string
	Ordinal  int
}

type ViewCompatibilityResult struct {
	Exists     bool
	Compatible bool
	Reason     string
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

func (i *SQLMetadataInspector) GetViewColumns(ctx context.Context, schemaName, viewName string) ([]ViewColumn, error) {
	if i == nil || i.db == nil {
		return nil, fmt.Errorf("PostgreSQL 连接未初始化")
	}
	rows, err := i.db.QueryContext(ctx, `
SELECT column_name, data_type, ordinal_position
FROM information_schema.columns
WHERE table_schema = $1
  AND table_name = $2
ORDER BY ordinal_position`, schemaName, viewName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []ViewColumn
	for rows.Next() {
		var column ViewColumn
		if err := rows.Scan(&column.Name, &column.DataType, &column.Ordinal); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func (i *SQLMetadataInspector) ProbeSelectColumns(ctx context.Context, selectSQL string) ([]ViewColumn, error) {
	if i == nil || i.db == nil {
		return nil, fmt.Errorf("PostgreSQL 连接未初始化")
	}
	rows, err := i.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM (%s) AS __df_sql_view_probe LIMIT 0", selectSQL))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	columns := make([]ViewColumn, 0, len(columnTypes))
	for i, columnType := range columnTypes {
		columns = append(columns, ViewColumn{
			Name:     columnType.Name(),
			DataType: columnType.DatabaseTypeName(),
			Ordinal:  i + 1,
		})
	}
	return columns, nil
}

func (i *SQLMetadataInspector) EstimateDMLAffectedRows(ctx context.Context, sqlText string) (int64, error) {
	if i == nil || i.db == nil {
		return 0, fmt.Errorf("PostgreSQL 连接未初始化")
	}
	explainSQL, ok := buildDMLExplainSQL(sqlText)
	if !ok {
		return 0, fmt.Errorf("仅支持 UPDATE/DELETE 影响行数预估")
	}
	var explainJSON string
	if err := i.db.QueryRowContext(ctx, explainSQL).Scan(&explainJSON); err != nil {
		return 0, err
	}
	rows, ok := extractPlanRowsFromExplainJSON(explainJSON)
	if !ok {
		return 0, fmt.Errorf("无法解析 EXPLAIN 结果")
	}
	return rows, nil
}

func buildDMLExplainSQL(sqlText string) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(sqlText, ";"))
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "UPDATE ") && !strings.HasPrefix(upper, "DELETE ") {
		return "", false
	}
	return "EXPLAIN (FORMAT JSON) " + trimmed, true
}

func extractPlanRowsFromExplainJSON(explainJSON string) (int64, bool) {
	var root []map[string]any
	if err := json.Unmarshal([]byte(explainJSON), &root); err != nil || len(root) == 0 {
		return 0, false
	}
	plan, ok := root[0]["Plan"].(map[string]any)
	if !ok {
		return 0, false
	}
	rows, ok := maxPlanRows(plan)
	return rows, ok
}

func maxPlanRows(plan map[string]any) (int64, bool) {
	var maxRows int64
	found := false
	if raw, ok := plan["Plan Rows"]; ok {
		if rows, ok := jsonNumberToInt64(raw); ok {
			maxRows = rows
			found = true
		}
	}
	if children, ok := plan["Plans"].([]any); ok {
		for _, child := range children {
			childPlan, ok := child.(map[string]any)
			if !ok {
				continue
			}
			if childRows, childOK := maxPlanRows(childPlan); childOK {
				if !found || childRows > maxRows {
					maxRows = childRows
				}
				found = true
			}
		}
	}
	return maxRows, found
}

func jsonNumberToInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

func classifyDMLAffectedRows(base RiskAnalysis, estimatedRows int64) RiskAnalysis {
	if base.SQLType != "UPDATE" && base.SQLType != "DELETE" {
		return base
	}
	base.RiskReason = appendRiskText(base.RiskReason, fmt.Sprintf("EXPLAIN 估算影响行数 %d", estimatedRows))
	switch {
	case estimatedRows > dmlAffectedRowsBlockThreshold:
		base.RiskLevel = "BLOCKED"
		base.RiskReason = appendRiskText(base.RiskReason, "超出内置高危阈值，已阻止直接执行")
	case estimatedRows > dmlAffectedRowsWarnThreshold && riskRank(base.RiskLevel) < riskRank("WARN"):
		base.RiskLevel = "WARN"
		base.RiskReason = appendRiskText(base.RiskReason, "影响行数较大，请确认 WHERE 条件和执行窗口")
	}
	return base
}

func compareViewColumns(oldCols, newCols []ViewColumn) ViewCompatibilityResult {
	if len(oldCols) == 0 {
		return ViewCompatibilityResult{Exists: false, Compatible: true}
	}
	if len(newCols) == 0 {
		return ViewCompatibilityResult{Exists: true, Compatible: false, Reason: "无法推断新视图列定义，重建视图可能受列名/顺序/类型兼容性限制"}
	}
	if len(oldCols) != len(newCols) {
		return ViewCompatibilityResult{Exists: true, Compatible: false, Reason: "视图输出列数量变化，CREATE OR REPLACE VIEW 可能不兼容"}
	}
	for i := range oldCols {
		if !strings.EqualFold(oldCols[i].Name, newCols[i].Name) {
			return ViewCompatibilityResult{Exists: true, Compatible: false, Reason: "视图输出列顺序或列名变化，CREATE OR REPLACE VIEW 可能不兼容"}
		}
		if normalizeViewDataType(oldCols[i].DataType) != normalizeViewDataType(newCols[i].DataType) {
			return ViewCompatibilityResult{Exists: true, Compatible: false, Reason: "视图输出列类型变化，CREATE OR REPLACE VIEW 可能不兼容"}
		}
	}
	return ViewCompatibilityResult{Exists: true, Compatible: true}
}

func normalizeViewDataType(dataType string) string {
	normalized := strings.ToLower(strings.TrimSpace(dataType))
	switch normalized {
	case "int2":
		return "smallint"
	case "int4":
		return "integer"
	case "int8":
		return "bigint"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "bool":
		return "boolean"
	case "varchar":
		return "character varying"
	case "bpchar":
		return "character"
	case "timestamptz":
		return "timestamp with time zone"
	case "timestamp":
		return "timestamp without time zone"
	default:
		return normalized
	}
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
	} else if isColumnVarchar(column) && isTextType(targetType) {
		level = "LOW"
		reasons = append(reasons, "varchar 转 text，通常为放宽长度限制")
	} else if isColumnText(column) && isVarcharType(targetType) {
		reasons = append(reasons, "text 转 varchar(n) 需要校验已有数据长度，可能失败")
	} else if oldPrecision, oldScale, newPrecision, newScale, ok := compareNumericPrecision(column, targetType); ok {
		switch {
		case newPrecision < oldPrecision || newScale < oldScale:
			reasons = append(reasons, fmt.Sprintf("numeric 精度缩小可能导致数据越界或舍入: (%d,%d) -> (%d,%d)", oldPrecision, oldScale, newPrecision, newScale))
		case newPrecision == oldPrecision && newScale == oldScale:
			level = "LOW"
			reasons = append(reasons, fmt.Sprintf("numeric 精度未变化: (%d,%d)", oldPrecision, oldScale))
		default:
			level = "LOW"
			reasons = append(reasons, fmt.Sprintf("numeric 精度放宽: (%d,%d) -> (%d,%d)", oldPrecision, oldScale, newPrecision, newScale))
		}
	} else if reason := blockedColumnTypeChangeReason(column, targetType); reason != "" {
		level = "BLOCKED"
		reasons = append(reasons, reason)
	} else if reason := warnedColumnTypeChangeReason(column, targetType); reason != "" {
		reasons = append(reasons, reason)
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

func blockedColumnTypeChangeReason(column ColumnInfo, targetType string) string {
	fromType := normalizeColumnTypeName(displayColumnType(column))
	toType := normalizeColumnTypeName(targetType)
	switch {
	case isIntegerTypeName(fromType) && isIntegerTypeName(toType) && integerTypeRank(toType) < integerTypeRank(fromType):
		return fmt.Sprintf("整数范围缩小: %s -> %s，可能导致数据越界", fromType, toType)
	case fromType == "numeric" && isIntegerTypeName(toType):
		return "numeric 转整数可能导致小数截断或数据越界"
	case (fromType == "text" || fromType == "character varying") && (toType == "uuid" || toType == "date" || strings.HasPrefix(toType, "timestamp")):
		return fmt.Sprintf("%s 转 %s 需要数据校验，可能存在无法转换的历史数据", fromType, toType)
	default:
		return ""
	}
}

func warnedColumnTypeChangeReason(column ColumnInfo, targetType string) string {
	fromType := normalizeColumnTypeName(displayColumnType(column))
	toType := normalizeColumnTypeName(targetType)
	switch {
	case strings.HasPrefix(fromType, "timestamp") && strings.HasPrefix(toType, "timestamp") && fromType != toType:
		return fmt.Sprintf("时间类型变更涉及时区语义: %s -> %s，请确认业务含义", fromType, toType)
	case fromType == "date" && strings.HasPrefix(toType, "timestamp"):
		return fmt.Sprintf("date 转 %s 涉及时间默认值语义，请确认业务含义", toType)
	case fromType == "json" && toType == "jsonb":
		return "JSON 存储格式变更为 jsonb，可能触发表重写并改变键顺序/重复键表现"
	default:
		return ""
	}
}

func normalizeColumnTypeName(typeName string) string {
	normalized := strings.ToLower(strings.TrimSpace(typeName))
	if idx := strings.Index(normalized, "("); idx >= 0 {
		normalized = strings.TrimSpace(normalized[:idx])
	}
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "int", "int4", "integer":
		return "integer"
	case "int2", "smallint":
		return "smallint"
	case "int8", "bigint":
		return "bigint"
	case "decimal", "numeric":
		return "numeric"
	case "varchar", "character varying":
		return "character varying"
	case "timestamptz", "timestamp with time zone":
		return "timestamp with time zone"
	case "timestamp", "timestamp without time zone":
		return "timestamp without time zone"
	default:
		return normalized
	}
}

func isIntegerTypeName(typeName string) bool {
	return typeName == "smallint" || typeName == "integer" || typeName == "bigint"
}

func integerTypeRank(typeName string) int {
	switch typeName {
	case "smallint":
		return 1
	case "integer":
		return 2
	case "bigint":
		return 3
	default:
		return 0
	}
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

func isColumnText(column ColumnInfo) bool {
	dataType := strings.ToLower(strings.TrimSpace(column.DataType))
	udtName := strings.ToLower(strings.TrimSpace(column.UDTName))
	return dataType == "text" || udtName == "text"
}

func isColumnNumeric(column ColumnInfo) bool {
	dataType := strings.ToLower(strings.TrimSpace(column.DataType))
	udtName := strings.ToLower(strings.TrimSpace(column.UDTName))
	return dataType == "numeric" || dataType == "decimal" || udtName == "numeric"
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

func compareNumericPrecision(column ColumnInfo, targetType string) (int64, int64, int64, int64, bool) {
	if !isColumnNumeric(column) || !column.HasNumericPrecision || !column.HasNumericScale || !isNumericType(targetType) {
		return 0, 0, 0, 0, false
	}
	precision, scale, ok := parseNumericPrecision(targetType)
	if !ok {
		return 0, 0, 0, 0, false
	}
	return column.NumericPrecision, column.NumericScale, precision, scale, true
}

func parseNumericPrecision(typeName string) (int64, int64, bool) {
	re := regexp.MustCompile(`(?i)\b(?:numeric|decimal)\s*\(\s*(\d+)\s*,\s*(\d+)\s*\)`)
	matches := re.FindStringSubmatch(typeName)
	if len(matches) < 3 {
		return 0, 0, false
	}
	precision, precisionErr := strconv.ParseInt(matches[1], 10, 64)
	scale, scaleErr := strconv.ParseInt(matches[2], 10, 64)
	return precision, scale, precisionErr == nil && scaleErr == nil
}

func isTextType(typeName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(typeName))
	return normalized == "text"
}

func displayColumnType(column ColumnInfo) string {
	if isColumnVarchar(column) && column.HasCharacterMaximumLength {
		return fmt.Sprintf("varchar(%d)", column.CharacterMaximumLength)
	}
	if isColumnNumeric(column) && column.HasNumericPrecision && column.HasNumericScale {
		return fmt.Sprintf("numeric(%d,%d)", column.NumericPrecision, column.NumericScale)
	}
	if strings.TrimSpace(column.DataType) != "" {
		return strings.TrimSpace(column.DataType)
	}
	return strings.TrimSpace(column.UDTName)
}
