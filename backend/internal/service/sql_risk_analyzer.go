package service

import (
	"fmt"
	"regexp"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	largeTableBytes = 10 * 1024 * 1024 * 1024
	largeTableRows  = 10_000_000
)

func AnalyzeSQLRisk(sqlText string) RiskAnalysis {
	tree, err := pg_query.Parse(sqlText)
	if err != nil {
		return RiskAnalysis{SQLType: "SQL_PARSE_ERROR", RiskLevel: "BLOCKED", RiskReason: "SQL 语法解析失败: " + err.Error()}
	}
	if reason, ok := missingExplicitSchemaReason(tree); ok {
		return RiskAnalysis{SQLType: "MISSING_SCHEMA", RiskLevel: "BLOCKED", RiskReason: reason}
	}
	if risk, ok := analyzeSQLRiskFromAST(tree); ok {
		return enrichStaticRisk(sqlText, classifyTrivialDMLWhere(sqlText, risk))
	}

	normalized := normalizeSQL(sqlText)
	upper := strings.ToUpper(normalized)

	blocked := []struct {
		pattern string
		typ     string
		reason  string
	}{
		{`^DROP\s+DATABASE\b`, "DROP_DATABASE", "禁止通过普通入口删除数据库"},
		{`^DROP\s+SCHEMA\b`, "DROP_SCHEMA", "禁止通过普通入口删除 schema"},
		{`^DROP\s+OWNED\b`, "DROP_OWNED", "禁止执行 DROP OWNED"},
		{`^ALTER\s+SYSTEM\b`, "ALTER_SYSTEM", "禁止修改数据库系统级参数"},
		{`^COPY\b.*\bPROGRAM\b`, "COPY_PROGRAM", "禁止执行 COPY PROGRAM"},
		{`^REINDEX\s+DATABASE\b`, "REINDEX_DATABASE", "禁止执行 REINDEX DATABASE"},
		{`^VACUUM\s+FULL\b`, "VACUUM_FULL", "禁止执行 VACUUM FULL"},
	}
	for _, rule := range blocked {
		if regexp.MustCompile(rule.pattern).MatchString(upper) {
			return RiskAnalysis{SQLType: rule.typ, RiskLevel: "BLOCKED", RiskReason: rule.reason}
		}
	}

	switch {
	case strings.HasPrefix(upper, "INSERT"):
		return RiskAnalysis{SQLType: "INSERT", RiskLevel: "LOW"}
	case strings.HasPrefix(upper, "UPDATE"):
		return RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}
	case strings.HasPrefix(upper, "DELETE"):
		return RiskAnalysis{SQLType: "DELETE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}
	case regexp.MustCompile(`^CREATE\s+TABLE\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_TABLE", RiskLevel: "LOW"}
	case regexp.MustCompile(`^CREATE\s+(OR\s+REPLACE\s+)?VIEW\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bADD\s+COLUMN\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "ADD_COLUMN", RiskLevel: "LOW"}
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bALTER\s+COLUMN\b.*\bTYPE\b`).MatchString(upper):
		return enrichStaticRisk(sqlText, RiskAnalysis{SQLType: "ALTER_COLUMN_TYPE", RiskLevel: "WARN", RiskReason: "字段类型变更可能触发表重写或被视图依赖阻塞"})
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bSET\s+NOT\s+NULL\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "ALTER_SET_NOT_NULL", RiskLevel: "WARN", RiskReason: "设置 NOT NULL 可能扫描全表"}
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bADD\s+CHECK\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "ADD_CHECK", RiskLevel: "WARN", RiskReason: "新增 CHECK 默认校验已有数据，可能扫描全表"}
	case regexp.MustCompile(`^CREATE\s+INDEX\s+CONCURRENTLY\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: "并发索引耗时可能较长"}
	case regexp.MustCompile(`^CREATE\s+INDEX\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入"}
	case regexp.MustCompile(`^TRUNCATE\b`).MatchString(upper):
		return enrichStaticRisk(sqlText, RiskAnalysis{SQLType: "TRUNCATE", RiskLevel: "WARN", RiskReason: "TRUNCATE 会快速清空表数据，请确认对象范围"})
	case regexp.MustCompile(`^DROP\s+TABLE\b`).MatchString(upper):
		return enrichStaticRisk(sqlText, RiskAnalysis{SQLType: "DROP_TABLE", RiskLevel: "WARN", RiskReason: "DROP TABLE 会删除表结构和数据，请确认对象范围"})
	default:
		return RiskAnalysis{SQLType: firstKeyword(upper), RiskLevel: "LOW"}
	}
}

func enrichStaticRisk(sqlText string, risk RiskAnalysis) RiskAnalysis {
	switch risk.SQLType {
	case "ALTER_COLUMN_TYPE":
		return classifyStaticColumnTypeChange(sqlText, risk)
	case "ADD_COLUMN":
		return classifyStaticAddColumn(sqlText, risk)
	case "ADD_CHECK":
		return classifyStaticAddCheck(sqlText, risk)
	case "DROP_TABLE", "TRUNCATE":
		tableName := extractDestructiveTableName(sqlText)
		if tableName == "" {
			return risk
		}
		return classifyDestructiveTableOperation(risk.SQLType, TableStats{TableName: tableName})
	default:
		return risk
	}
}

func classifyStaticColumnTypeChange(sqlText string, risk RiskAnalysis) RiskAnalysis {
	targetType := extractAlterColumnTargetType(sqlText)
	hasUsing := hasAlterColumnUsing(sqlText)
	reasons := make([]string, 0, 5)

	if strings.TrimSpace(targetType) != "" && isVarcharType(targetType) {
		reasons = append(reasons, "varchar 类型变更需要结合原字段长度判断，避免缩容截断或误判扩容")
	}
	if strings.TrimSpace(targetType) != "" && isNumericType(targetType) {
		reasons = append(reasons, "numeric 精度或 scale 变化需要校验已有数据范围")
	}
	if isStorageRewriteType(targetType) {
		reasons = append(reasons, "目标类型可能改变存储格式，可能触发表重写或转换失败")
	}
	if isTimezoneSemanticType(targetType) {
		reasons = append(reasons, "时间类型变更涉及时区语义，请确认业务含义")
	}
	if hasUsing {
		reasons = append(reasons, "USING 表达式会逐行转换数据，请确认转换逻辑和执行窗口")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "字段类型变更可能触发表重写或被视图依赖阻塞")
	}

	risk.RiskLevel = "WARN"
	risk.RiskReason = strings.Join(reasons, "；")
	return risk
}

func classifyStaticAddColumn(sqlText string, risk RiskAnalysis) RiskAnalysis {
	if hasVolatileDefault(sqlText) {
		return RiskAnalysis{
			SQLType:    "ADD_COLUMN_DEFAULT_VOLATILE",
			RiskLevel:  "WARN",
			RiskReason: "ADD COLUMN 使用 volatile 默认值，可能逐行计算并重写大表",
		}
	}
	return risk
}

func classifyStaticAddCheck(sqlText string, risk RiskAnalysis) RiskAnalysis {
	if regexp.MustCompile(`(?i)\bNOT\s+VALID\b`).MatchString(sqlText) {
		return RiskAnalysis{SQLType: "ADD_CHECK_NOT_VALID", RiskLevel: "LOW", RiskReason: "CHECK 使用 NOT VALID，不立即校验已有数据"}
	}
	return risk
}

func classifyTrivialDMLWhere(sqlText string, risk RiskAnalysis) RiskAnalysis {
	if risk.SQLType != "UPDATE" && risk.SQLType != "DELETE" {
		return risk
	}
	whereExpr := extractDMLWhereExpression(sqlText)
	if !isTrivialWhereExpression(whereExpr) {
		return risk
	}
	if risk.SQLType == "UPDATE" {
		return RiskAnalysis{SQLType: "UPDATE_TRIVIAL_WHERE", RiskLevel: "BLOCKED", RiskReason: "UPDATE 条件无有效过滤条件，禁止直接执行"}
	}
	return RiskAnalysis{SQLType: "DELETE_TRIVIAL_WHERE", RiskLevel: "BLOCKED", RiskReason: "DELETE 条件无有效过滤条件，禁止直接执行"}
}

func extractDMLWhereExpression(sqlText string) string {
	normalized := strings.TrimSuffix(normalizeSQL(sqlText), ";")
	matches := regexp.MustCompile(`(?is)\bWHERE\s+(.+)$`).FindStringSubmatch(normalized)
	if len(matches) < 2 {
		return ""
	}
	whereExpr := matches[1]
	whereExpr = regexp.MustCompile(`(?is)\bRETURNING\b.*$`).ReplaceAllString(whereExpr, "")
	return strings.TrimSpace(whereExpr)
}

func isTrivialWhereExpression(whereExpr string) bool {
	normalized := strings.ToLower(strings.TrimSpace(whereExpr))
	for strings.HasPrefix(normalized, "(") && strings.HasSuffix(normalized, ")") {
		normalized = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(normalized, "("), ")"))
	}
	compact := regexp.MustCompile(`\s+`).ReplaceAllString(normalized, "")
	return compact == "true" || compact == "1=1"
}

func extractAlterColumnTargetType(sqlText string) string {
	re := regexp.MustCompile(`(?is)\bTYPE\s+([a-zA-Z_][\w. ]*(?:\s*\([^)]*\))?)`)
	matches := re.FindStringSubmatch(sqlText)
	if len(matches) < 2 {
		return ""
	}
	typeName := strings.TrimSpace(matches[1])
	if idx := regexp.MustCompile(`(?i)\bUSING\b`).FindStringIndex(typeName); idx != nil {
		typeName = strings.TrimSpace(typeName[:idx[0]])
	}
	return strings.TrimSpace(typeName)
}

func isVarcharType(typeName string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(typeName), " "))
	return strings.HasPrefix(normalized, "varchar") || strings.HasPrefix(normalized, "character varying")
}

func isNumericType(typeName string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(typeName), " "))
	return strings.HasPrefix(normalized, "numeric") || strings.HasPrefix(normalized, "decimal")
}

func isStorageRewriteType(typeName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(typeName))
	prefixes := []string{"jsonb", "json", "uuid", "bigint", "integer", "int4", "int8", "date", "timestamp", "timestamptz"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func isTimezoneSemanticType(typeName string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(typeName), " "))
	return strings.HasPrefix(normalized, "timestamptz") || strings.HasPrefix(normalized, "timestamp with time zone")
}

func hasVolatileDefault(sqlText string) bool {
	if hasVolatile, parsed := hasVolatileDefaultFromAST(sqlText); parsed {
		return hasVolatile
	}
	upper := strings.ToUpper(sqlText)
	if !strings.Contains(upper, " DEFAULT ") {
		return false
	}
	volatilePatterns := []string{
		`(?i)\bnow\s*\(`,
		`(?i)\brandom\s*\(`,
		`(?i)\buuid_generate_v4\s*\(`,
		`(?i)\bgen_random_uuid\s*\(`,
		`(?i)\bclock_timestamp\s*\(`,
		`(?i)\bstatement_timestamp\s*\(`,
		`(?i)\btimeofday\s*\(`,
	}
	for _, pattern := range volatilePatterns {
		if regexp.MustCompile(pattern).MatchString(sqlText) {
			return true
		}
	}
	return false
}

func hasVolatileDefaultFromAST(sqlText string) (bool, bool) {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || tree == nil || len(tree.GetStmts()) == 0 {
		return false, false
	}
	for _, rawStmt := range tree.GetStmts() {
		alterStmt := rawStmt.GetStmt().GetAlterTableStmt()
		if alterStmt == nil {
			continue
		}
		for _, cmdNode := range alterStmt.GetCmds() {
			cmd := cmdNode.GetAlterTableCmd()
			if cmd == nil || cmd.GetSubtype() != pg_query.AlterTableType_AT_AddColumn {
				continue
			}
			column := cmd.GetDef().GetColumnDef()
			if column == nil {
				continue
			}
			if nodeContainsVolatileFunction(column.GetRawDefault()) {
				return true, true
			}
			for _, constraintNode := range column.GetConstraints() {
				constraint := constraintNode.GetConstraint()
				if constraint == nil || constraint.GetContype() != pg_query.ConstrType_CONSTR_DEFAULT {
					continue
				}
				if nodeContainsVolatileFunction(constraint.GetRawExpr()) {
					return true, true
				}
			}
		}
	}
	return false, true
}

func nodeContainsVolatileFunction(node *pg_query.Node) bool {
	if node == nil {
		return false
	}
	if call := node.GetFuncCall(); call != nil && isVolatileFunctionName(funcCallName(call)) {
		return true
	}
	return protoMessageContainsVolatileFunction(node.ProtoReflect())
}

func protoMessageContainsVolatileFunction(message protoreflect.Message) bool {
	if !message.IsValid() {
		return false
	}
	if call, ok := message.Interface().(*pg_query.FuncCall); ok && isVolatileFunctionName(funcCallName(call)) {
		return true
	}
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			continue
		}
		value := message.Get(field)
		if field.IsList() {
			list := value.List()
			for j := 0; j < list.Len(); j++ {
				if protoMessageContainsVolatileFunction(list.Get(j).Message()) {
					return true
				}
			}
			continue
		}
		if value.Message().IsValid() && protoMessageContainsVolatileFunction(value.Message()) {
			return true
		}
	}
	return false
}

func funcCallName(call *pg_query.FuncCall) string {
	if call == nil {
		return ""
	}
	parts := make([]string, 0, len(call.GetFuncname()))
	for _, part := range call.GetFuncname() {
		if str := part.GetString_(); str != nil {
			parts = append(parts, str.GetSval())
		}
	}
	return strings.ToLower(strings.Join(parts, "."))
}

func isVolatileFunctionName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if idx := strings.LastIndex(normalized, "."); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	switch normalized {
	case "now", "random", "uuid_generate_v4", "gen_random_uuid", "clock_timestamp", "statement_timestamp", "timeofday":
		return true
	default:
		return false
	}
}

func hasAlterColumnUsing(sqlText string) bool {
	return regexp.MustCompile(`(?i)\bUSING\b`).MatchString(sqlText)
}

func classifyDestructiveTableOperation(sqlType string, stats TableStats) RiskAnalysis {
	tableName := strings.TrimSpace(stats.TableName)
	if isTemporaryOrBackupTableName(tableName) {
		return RiskAnalysis{SQLType: sqlType, RiskLevel: "LOW", RiskReason: "临时/备份表命名规则命中"}
	}
	hasMetadata := strings.TrimSpace(stats.SchemaName) != "" || stats.TotalBytes > 0 || stats.EstimatedRows > 0
	if !hasMetadata {
		return RiskAnalysis{
			SQLType:    sqlType,
			RiskLevel:  "BLOCKED",
			RiskReason: fmt.Sprintf("%s 默认禁止直接执行，请确认对象范围或导出处理", sqlType),
		}
	}
	if stats.TotalBytes > largeTableBytes || stats.EstimatedRows > largeTableRows {
		return RiskAnalysis{
			SQLType:    sqlType,
			RiskLevel:  "BLOCKED",
			RiskReason: fmt.Sprintf("大表高危操作，表 %s 大小 %d bytes，估算行数 %d", tableName, stats.TotalBytes, stats.EstimatedRows),
		}
	}
	switch sqlType {
	case "DROP_TABLE":
		return RiskAnalysis{SQLType: sqlType, RiskLevel: "WARN", RiskReason: "DROP TABLE 会删除表结构和数据，请确认对象范围"}
	case "TRUNCATE":
		return RiskAnalysis{SQLType: sqlType, RiskLevel: "WARN", RiskReason: "TRUNCATE 会快速清空表数据，请确认对象范围"}
	default:
		return RiskAnalysis{SQLType: sqlType, RiskLevel: "WARN", RiskReason: "破坏性表操作，请确认对象范围"}
	}
}

func classifyLargeTableSensitiveOperation(base RiskAnalysis, stats TableStats) RiskAnalysis {
	if base.RiskLevel == "LOW" || base.RiskLevel == "BLOCKED" {
		return base
	}
	if stats.TotalBytes > largeTableBytes || stats.EstimatedRows > largeTableRows {
		base.RiskLevel = "BLOCKED"
		base.RiskReason = strings.TrimSpace(base.RiskReason + fmt.Sprintf("；大表高风险操作，表 %s 大小 %d bytes，估算行数 %d", stats.TableName, stats.TotalBytes, stats.EstimatedRows))
	}
	return base
}

func isLargeTableSensitiveSQLType(sqlType string) bool {
	switch sqlType {
	case "ALTER_COLUMN_TYPE", "ALTER_SET_NOT_NULL", "ADD_CHECK", "CREATE_INDEX", "ADD_COLUMN_DEFAULT_VOLATILE":
		return true
	default:
		return false
	}
}

func isDMLRiskWithAffectedRows(sqlType string) bool {
	return sqlType == "UPDATE" || sqlType == "DELETE"
}

func isTemporaryOrBackupTableName(name string) bool {
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), `"`))
	if name == "" {
		return false
	}
	return strings.HasPrefix(name, "temp_") ||
		strings.HasPrefix(name, "tmp_") ||
		strings.HasPrefix(name, "bak_") ||
		strings.HasPrefix(name, "backup_") ||
		strings.HasSuffix(name, "_tmp") ||
		strings.HasSuffix(name, "_bak")
}

func extractDestructiveTableName(sqlText string) string {
	normalized := normalizeSQL(sqlText)
	upper := strings.ToUpper(normalized)
	rest := ""
	switch {
	case strings.HasPrefix(upper, "DROP TABLE IF EXISTS "):
		rest = strings.TrimSpace(normalized[len("DROP TABLE IF EXISTS "):])
	case strings.HasPrefix(upper, "DROP TABLE "):
		rest = strings.TrimSpace(normalized[len("DROP TABLE "):])
	case strings.HasPrefix(upper, "TRUNCATE TABLE ONLY "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE TABLE ONLY "):])
	case strings.HasPrefix(upper, "TRUNCATE TABLE "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE TABLE "):])
	case strings.HasPrefix(upper, "TRUNCATE ONLY "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE ONLY "):])
	case strings.HasPrefix(upper, "TRUNCATE "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE "):])
	default:
		return ""
	}
	name := firstSQLName(rest)
	name = strings.TrimSuffix(name, ",")
	parts := strings.Split(name, ".")
	return strings.Trim(parts[len(parts)-1], `"`)
}

func firstSQLName(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	var b strings.Builder
	inDouble := false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if ch == '"' {
			inDouble = !inDouble
			b.WriteByte(ch)
			continue
		}
		if !inDouble && (ch == ' ' || ch == ',') {
			break
		}
		b.WriteByte(ch)
	}
	return strings.TrimSpace(b.String())
}

func analyzeSQLRiskFromAST(tree *pg_query.ParseResult) (RiskAnalysis, bool) {
	if tree == nil || len(tree.GetStmts()) == 0 {
		return RiskAnalysis{SQLType: "UNKNOWN", RiskLevel: "LOW"}, true
	}
	if len(tree.GetStmts()) > 1 {
		return RiskAnalysis{SQLType: "MULTI_STATEMENT", RiskLevel: "WARN", RiskReason: "单条记录包含多条 SQL，请确认拆分结果"}, true
	}
	node := tree.GetStmts()[0].GetStmt()
	if node == nil {
		return RiskAnalysis{SQLType: "UNKNOWN", RiskLevel: "LOW"}, true
	}

	switch {
	case node.GetInsertStmt() != nil:
		return RiskAnalysis{SQLType: "INSERT", RiskLevel: "LOW"}, true
	case node.GetUpdateStmt() != nil:
		if node.GetUpdateStmt().GetWhereClause() == nil {
			return RiskAnalysis{SQLType: "UPDATE_WITHOUT_WHERE", RiskLevel: "BLOCKED", RiskReason: "UPDATE 缺少 WHERE 条件，禁止直接执行"}, true
		}
		return RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}, true
	case node.GetDeleteStmt() != nil:
		if node.GetDeleteStmt().GetWhereClause() == nil {
			return RiskAnalysis{SQLType: "DELETE_WITHOUT_WHERE", RiskLevel: "BLOCKED", RiskReason: "DELETE 缺少 WHERE 条件，禁止直接执行"}, true
		}
		return RiskAnalysis{SQLType: "DELETE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}, true
	case node.GetCreateStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE", RiskLevel: "LOW"}, true
	case node.GetCreateTableAsStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE_AS", RiskLevel: "WARN", RiskReason: "CREATE TABLE AS 可能写入大量数据，请确认数据规模"}, true
	case node.GetViewStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}, true
	case node.GetCreateFunctionStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_FUNCTION", RiskLevel: "WARN", RiskReason: "创建或替换函数可能改变业务逻辑，请确认依赖范围"}, true
	case node.GetCreateExtensionStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_EXTENSION", RiskLevel: "WARN", RiskReason: "创建扩展会修改数据库能力和对象，请确认权限和兼容性"}, true
	case node.GetAlterSystemStmt() != nil:
		return RiskAnalysis{SQLType: "ALTER_SYSTEM", RiskLevel: "BLOCKED", RiskReason: "禁止修改数据库系统级参数"}, true
	case node.GetDropOwnedStmt() != nil:
		return RiskAnalysis{SQLType: "DROP_OWNED", RiskLevel: "BLOCKED", RiskReason: "禁止执行 DROP OWNED"}, true
	case node.GetCopyStmt() != nil:
		copyStmt := node.GetCopyStmt()
		if copyStmt.GetIsProgram() {
			return RiskAnalysis{SQLType: "COPY_PROGRAM", RiskLevel: "BLOCKED", RiskReason: "禁止执行 COPY PROGRAM"}, true
		}
		return RiskAnalysis{SQLType: "COPY", RiskLevel: "WARN", RiskReason: "COPY 可能批量导入或导出数据，请确认文件和数据范围"}, true
	case node.GetDropStmt() != nil:
		return analyzeDropStmt(node.GetDropStmt()), true
	case node.GetAlterTableStmt() != nil:
		return analyzeAlterTableStmt(node.GetAlterTableStmt()), true
	case node.GetIndexStmt() != nil:
		indexStmt := node.GetIndexStmt()
		if indexStmt.GetConcurrent() {
			return RiskAnalysis{SQLType: "CREATE_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: "并发索引耗时可能较长"}, true
		}
		return RiskAnalysis{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入"}, true
	case node.GetTruncateStmt() != nil:
		return analyzeTruncateStmt(node.GetTruncateStmt()), true
	case node.GetReindexStmt() != nil:
		reindexStmt := node.GetReindexStmt()
		if reindexStmt.GetKind() == pg_query.ReindexObjectType_REINDEX_OBJECT_DATABASE {
			return RiskAnalysis{SQLType: "REINDEX_DATABASE", RiskLevel: "BLOCKED", RiskReason: "禁止执行 REINDEX DATABASE"}, true
		}
		return RiskAnalysis{SQLType: "REINDEX", RiskLevel: "WARN", RiskReason: "REINDEX 可能长时间持锁，请确认对象范围和窗口期"}, true
	case node.GetVacuumStmt() != nil:
		if vacuumHasOption(node.GetVacuumStmt(), "full") {
			return RiskAnalysis{SQLType: "VACUUM_FULL", RiskLevel: "BLOCKED", RiskReason: "禁止执行 VACUUM FULL"}, true
		}
		return RiskAnalysis{SQLType: "VACUUM", RiskLevel: "LOW"}, true
	case node.GetRefreshMatViewStmt() != nil:
		return RiskAnalysis{SQLType: "REFRESH_MATERIALIZED_VIEW", RiskLevel: "WARN", RiskReason: "刷新物化视图可能长时间持锁或写入大量数据"}, true
	default:
		return RiskAnalysis{}, false
	}
}

func missingExplicitSchemaReason(tree *pg_query.ParseResult) (string, bool) {
	if tree == nil {
		return "", false
	}
	for _, rawStmt := range tree.GetStmts() {
		if rawStmt == nil || rawStmt.GetStmt() == nil {
			continue
		}
		for _, target := range schemaRequiredTargets(rawStmt.GetStmt()) {
			if target.rel == nil {
				continue
			}
			if strings.TrimSpace(target.rel.GetRelname()) == "" {
				continue
			}
			if strings.TrimSpace(target.rel.GetSchemaname()) == "" {
				return fmt.Sprintf("%s 未显式指定 schema，请使用 schema.object 形式", target.name), true
			}
		}
		cteNames := collectCTENames(rawStmt.GetStmt().ProtoReflect(), nil)
		if relName, ok := findMissingSchemaRangeVar(rawStmt.GetStmt().ProtoReflect(), cteNames); ok {
			return fmt.Sprintf("SQL 引用对象 %s 未显式指定 schema，请使用 schema.object 形式", relName), true
		}
	}
	return "", false
}

func collectCTENames(message protoreflect.Message, names map[string]struct{}) map[string]struct{} {
	if !message.IsValid() {
		return names
	}
	if cte, ok := message.Interface().(*pg_query.CommonTableExpr); ok {
		name := strings.ToLower(strings.TrimSpace(cte.GetCtename()))
		if name != "" {
			if names == nil {
				names = make(map[string]struct{})
			}
			names[name] = struct{}{}
		}
	}
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			continue
		}
		value := message.Get(field)
		if field.IsList() {
			list := value.List()
			for j := 0; j < list.Len(); j++ {
				names = collectCTENames(list.Get(j).Message(), names)
			}
			continue
		}
		if value.Message().IsValid() {
			names = collectCTENames(value.Message(), names)
		}
	}
	return names
}

func findMissingSchemaRangeVar(message protoreflect.Message, cteNames map[string]struct{}) (string, bool) {
	if !message.IsValid() {
		return "", false
	}
	if rel, ok := message.Interface().(*pg_query.RangeVar); ok {
		if strings.TrimSpace(rel.GetRelname()) != "" && strings.TrimSpace(rel.GetSchemaname()) == "" {
			if _, isCTE := cteNames[strings.ToLower(strings.TrimSpace(rel.GetRelname()))]; isCTE {
				return "", false
			}
			return rel.GetRelname(), true
		}
	}
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			continue
		}
		value := message.Get(field)
		if field.IsList() {
			list := value.List()
			for j := 0; j < list.Len(); j++ {
				if name, ok := findMissingSchemaRangeVar(list.Get(j).Message(), cteNames); ok {
					return name, true
				}
			}
			continue
		}
		if value.Message().IsValid() {
			if name, ok := findMissingSchemaRangeVar(value.Message(), cteNames); ok {
				return name, true
			}
		}
	}
	return "", false
}

type schemaRequiredTarget struct {
	name string
	rel  *pg_query.RangeVar
}

func schemaRequiredTargets(node *pg_query.Node) []schemaRequiredTarget {
	switch {
	case node.GetInsertStmt() != nil:
		return []schemaRequiredTarget{{name: "INSERT 目标表", rel: node.GetInsertStmt().GetRelation()}}
	case node.GetUpdateStmt() != nil:
		return []schemaRequiredTarget{{name: "UPDATE 目标表", rel: node.GetUpdateStmt().GetRelation()}}
	case node.GetDeleteStmt() != nil:
		return []schemaRequiredTarget{{name: "DELETE 目标表", rel: node.GetDeleteStmt().GetRelation()}}
	case node.GetCreateStmt() != nil:
		return []schemaRequiredTarget{{name: "CREATE TABLE 目标表", rel: node.GetCreateStmt().GetRelation()}}
	case node.GetAlterTableStmt() != nil:
		return []schemaRequiredTarget{{name: "ALTER TABLE 目标对象", rel: node.GetAlterTableStmt().GetRelation()}}
	case node.GetIndexStmt() != nil:
		return []schemaRequiredTarget{{name: "CREATE INDEX 目标表", rel: node.GetIndexStmt().GetRelation()}}
	case node.GetViewStmt() != nil:
		return []schemaRequiredTarget{{name: "CREATE VIEW 目标视图", rel: node.GetViewStmt().GetView()}}
	case node.GetCreateTableAsStmt() != nil:
		if into := node.GetCreateTableAsStmt().GetInto(); into != nil {
			return []schemaRequiredTarget{{name: "CREATE TABLE AS 目标表", rel: into.GetRel()}}
		}
	case node.GetCreateFunctionStmt() != nil:
		return []schemaRequiredTarget{{name: "CREATE FUNCTION 目标函数", rel: rangeVarFromNameNodes(node.GetCreateFunctionStmt().GetFuncname())}}
	case node.GetRefreshMatViewStmt() != nil:
		return []schemaRequiredTarget{{name: "REFRESH MATERIALIZED VIEW 目标对象", rel: node.GetRefreshMatViewStmt().GetRelation()}}
	case node.GetTruncateStmt() != nil:
		targets := make([]schemaRequiredTarget, 0, len(node.GetTruncateStmt().GetRelations()))
		for _, relNode := range node.GetTruncateStmt().GetRelations() {
			targets = append(targets, schemaRequiredTarget{name: "TRUNCATE 目标表", rel: relNode.GetRangeVar()})
		}
		return targets
	case node.GetDropStmt() != nil:
		return dropStmtSchemaRequiredTargets(node.GetDropStmt())
	}
	return nil
}

func dropStmtSchemaRequiredTargets(stmt *pg_query.DropStmt) []schemaRequiredTarget {
	switch stmt.GetRemoveType() {
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW, pg_query.ObjectType_OBJECT_INDEX, pg_query.ObjectType_OBJECT_FUNCTION:
	default:
		return nil
	}
	targets := make([]schemaRequiredTarget, 0, len(stmt.GetObjects()))
	for _, obj := range stmt.GetObjects() {
		if objectWithArgs := obj.GetObjectWithArgs(); objectWithArgs != nil {
			targets = append(targets, schemaRequiredTarget{name: "DROP 目标函数", rel: rangeVarFromNameNodes(objectWithArgs.GetObjname())})
			continue
		}
		parts := stringListFromNode(obj)
		if len(parts) == 0 {
			continue
		}
		if len(parts) < 2 {
			targets = append(targets, schemaRequiredTarget{name: "DROP 目标对象", rel: &pg_query.RangeVar{Relname: parts[len(parts)-1]}})
			continue
		}
		targets = append(targets, schemaRequiredTarget{name: "DROP 目标对象", rel: &pg_query.RangeVar{Schemaname: parts[len(parts)-2], Relname: parts[len(parts)-1]}})
	}
	return targets
}

func rangeVarFromNameNodes(nodes []*pg_query.Node) *pg_query.RangeVar {
	parts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if str := node.GetString_(); str != nil {
			parts = append(parts, str.GetSval())
		}
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return &pg_query.RangeVar{Relname: parts[0]}
	}
	return &pg_query.RangeVar{Schemaname: parts[len(parts)-2], Relname: parts[len(parts)-1]}
}

func stringListFromNode(node *pg_query.Node) []string {
	if node == nil {
		return nil
	}
	if list := node.GetList(); list != nil {
		parts := make([]string, 0, len(list.GetItems()))
		for _, item := range list.GetItems() {
			if str := item.GetString_(); str != nil {
				parts = append(parts, str.GetSval())
			}
		}
		return parts
	}
	if str := node.GetString_(); str != nil {
		return []string{str.GetSval()}
	}
	return nil
}

func analyzeDropStmt(stmt *pg_query.DropStmt) RiskAnalysis {
	switch stmt.GetRemoveType() {
	case pg_query.ObjectType_OBJECT_DATABASE:
		return RiskAnalysis{SQLType: "DROP_DATABASE", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口删除数据库"}
	case pg_query.ObjectType_OBJECT_SCHEMA:
		return RiskAnalysis{SQLType: "DROP_SCHEMA", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口删除 schema"}
	case pg_query.ObjectType_OBJECT_TABLE:
		return classifyDestructiveTableOperation("DROP_TABLE", TableStats{TableName: firstDropObjectName(stmt)})
	case pg_query.ObjectType_OBJECT_INDEX:
		if stmt.GetConcurrent() {
			return RiskAnalysis{SQLType: "DROP_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: "并发删除索引耗时可能较长"}
		}
		return RiskAnalysis{SQLType: "DROP_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 删除索引可能阻塞访问"}
	case pg_query.ObjectType_OBJECT_FUNCTION:
		return RiskAnalysis{SQLType: "DROP_FUNCTION", RiskLevel: "WARN", RiskReason: "删除函数可能影响依赖对象或业务调用"}
	case pg_query.ObjectType_OBJECT_EXTENSION:
		return RiskAnalysis{SQLType: "DROP_EXTENSION", RiskLevel: "WARN", RiskReason: "删除扩展会影响扩展对象和依赖功能"}
	default:
		return RiskAnalysis{SQLType: "DROP", RiskLevel: "WARN", RiskReason: "DROP 会删除数据库对象，请确认对象范围"}
	}
}

func analyzeTruncateStmt(stmt *pg_query.TruncateStmt) RiskAnalysis {
	for _, relNode := range stmt.GetRelations() {
		rel := relNode.GetRangeVar()
		if rel == nil {
			continue
		}
		risk := classifyDestructiveTableOperation("TRUNCATE", TableStats{TableName: rel.GetRelname()})
		if risk.RiskLevel != "LOW" {
			return risk
		}
	}
	return RiskAnalysis{SQLType: "TRUNCATE", RiskLevel: "LOW", RiskReason: "临时/备份表命名规则命中"}
}

func firstDropObjectName(stmt *pg_query.DropStmt) string {
	for _, obj := range stmt.GetObjects() {
		parts := stringListFromNode(obj)
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return ""
}

func analyzeAlterTableStmt(stmt *pg_query.AlterTableStmt) RiskAnalysis {
	risk := RiskAnalysis{SQLType: "ALTER_TABLE", RiskLevel: "LOW"}
	for _, cmdNode := range stmt.GetCmds() {
		cmd := cmdNode.GetAlterTableCmd()
		if cmd == nil {
			continue
		}
		switch cmd.GetSubtype() {
		case pg_query.AlterTableType_AT_AddColumn:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_COLUMN", RiskLevel: "LOW"})
		case pg_query.AlterTableType_AT_AlterColumnType:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ALTER_COLUMN_TYPE", RiskLevel: "WARN", RiskReason: "字段类型变更可能触发表重写或被视图依赖阻塞"})
		case pg_query.AlterTableType_AT_SetNotNull:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ALTER_SET_NOT_NULL", RiskLevel: "WARN", RiskReason: "设置 NOT NULL 可能扫描全表"})
		case pg_query.AlterTableType_AT_AddConstraint:
			if constraint := cmd.GetDef().GetConstraint(); constraint != nil {
				switch constraint.GetContype() {
				case pg_query.ConstrType_CONSTR_CHECK:
					if constraint.GetSkipValidation() {
						risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_CHECK_NOT_VALID", RiskLevel: "LOW", RiskReason: "CHECK 使用 NOT VALID，不立即校验已有数据"})
					} else {
						risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_CHECK", RiskLevel: "WARN", RiskReason: "新增 CHECK 默认校验已有数据，可能扫描全表"})
					}
				case pg_query.ConstrType_CONSTR_FOREIGN:
					risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_FOREIGN_KEY", RiskLevel: "WARN", RiskReason: "新增外键可能扫描已有数据并长时间持锁"})
				case pg_query.ConstrType_CONSTR_PRIMARY:
					risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_PRIMARY_KEY", RiskLevel: "WARN", RiskReason: "新增主键会创建索引并校验数据唯一性"})
				case pg_query.ConstrType_CONSTR_UNIQUE:
					risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_UNIQUE", RiskLevel: "WARN", RiskReason: "新增唯一约束会创建索引并校验数据唯一性"})
				}
			}
		case pg_query.AlterTableType_AT_ValidateConstraint:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "VALIDATE_CONSTRAINT", RiskLevel: "WARN", RiskReason: "VALIDATE CONSTRAINT 会扫描已有数据"})
		case pg_query.AlterTableType_AT_AddIndex, pg_query.AlterTableType_AT_AddIndexConstraint:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_INDEX_CONSTRAINT", RiskLevel: "WARN", RiskReason: "新增索引约束可能长时间持锁"})
		case pg_query.AlterTableType_AT_AttachPartition:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ATTACH_PARTITION", RiskLevel: "WARN", RiskReason: "挂载分区可能校验数据范围并持锁"})
		case pg_query.AlterTableType_AT_DetachPartition, pg_query.AlterTableType_AT_DetachPartitionFinalize:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "DETACH_PARTITION", RiskLevel: "WARN", RiskReason: "分离分区可能影响查询路由和数据可见性"})
		case pg_query.AlterTableType_AT_DropColumn, pg_query.AlterTableType_AT_DropConstraint:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ALTER_TABLE_DROP", RiskLevel: "WARN", RiskReason: "ALTER TABLE 删除字段或约束可能影响业务，请确认依赖范围"})
		}
	}
	return risk
}

func vacuumHasOption(stmt *pg_query.VacuumStmt, option string) bool {
	for _, node := range stmt.GetOptions() {
		def := node.GetDefElem()
		if def != nil && strings.EqualFold(def.GetDefname(), option) {
			return true
		}
	}
	return false
}

func mergeRisk(current, candidate RiskAnalysis) RiskAnalysis {
	if riskRank(candidate.RiskLevel) > riskRank(current.RiskLevel) {
		return candidate
	}
	if current.SQLType == "ALTER_TABLE" && candidate.SQLType != "" {
		return candidate
	}
	return current
}

func riskRank(level string) int {
	switch level {
	case "BLOCKED":
		return 3
	case "WARN":
		return 2
	case "LOW":
		return 1
	default:
		return 0
	}
}

func normalizeSQL(sqlText string) string {
	return strings.Join(strings.Fields(sqlText), " ")
}

func firstKeyword(sqlText string) string {
	for _, part := range strings.Fields(sqlText) {
		return strings.ToUpper(part)
	}
	return "UNKNOWN"
}
