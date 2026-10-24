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

// Package-level compiled regexes to avoid recompilation in hot paths
var (
	reDropDatabase            = regexp.MustCompile(`^DROP\s+DATABASE\b`)
	reDropSchema              = regexp.MustCompile(`^DROP\s+SCHEMA\b`)
	reDropOwned               = regexp.MustCompile(`^DROP\s+OWNED\b`)
	reAlterSystem             = regexp.MustCompile(`^ALTER\s+SYSTEM\b`)
	reCopyProgram             = regexp.MustCompile(`^COPY\b.*\bPROGRAM\b`)
	reReindexDatabase         = regexp.MustCompile(`^REINDEX\s+DATABASE\b`)
	reVacuumFull              = regexp.MustCompile(`^VACUUM\s+FULL\b`)
	reCreateTable             = regexp.MustCompile(`^CREATE\s+TABLE\b`)
	reCreateOrReplaceView     = regexp.MustCompile(`^CREATE\s+(OR\s+REPLACE\s+)?VIEW\b`)
	reAlterTableAddColumn     = regexp.MustCompile(`^ALTER\s+TABLE\b.*\bADD\s+COLUMN\b`)
	reAlterTableAlterType     = regexp.MustCompile(`^ALTER\s+TABLE\b.*\bALTER\s+COLUMN\b.*\bTYPE\b`)
	reAlterTableSetNotNull    = regexp.MustCompile(`^ALTER\s+TABLE\b.*\bSET\s+NOT\s+NULL\b`)
	reAlterTableAddCheck      = regexp.MustCompile(`^ALTER\s+TABLE\b.*\bADD\s+CHECK\b`)
	reCreateIndexConcurrently = regexp.MustCompile(`^CREATE\s+INDEX\s+CONCURRENTLY\b`)
	reCreateIndex             = regexp.MustCompile(`^CREATE\s+INDEX\b`)
	reTruncate                = regexp.MustCompile(`^TRUNCATE\b`)
	reDropTable               = regexp.MustCompile(`^DROP\s+TABLE\b`)
	reAlterColumnUsing        = regexp.MustCompile(`(?i)\bTYPE\b[^;]*\bUSING\b`)
	reAlterColumnTargetType   = regexp.MustCompile(`(?is)\bTYPE\s+([a-zA-Z_][\w. ]*(?:\s*\([^)]*\))?)`)
	reAlterColumnUsingInType  = regexp.MustCompile(`(?i)\bUSING\b`)
	reWhereClause             = regexp.MustCompile(`(?is)\bWHERE\s+(.+)$`)
	reReturningClause         = regexp.MustCompile(`(?is)\bRETURNING\b.*$`)
	reWhitespace              = regexp.MustCompile(`\s+`)
	reIsNotNull               = regexp.MustCompile(`(?i)\bis\s+not\s+null\b`)
	reBooleanCondition        = regexp.MustCompile(`(?i)\b[a-zA-Z_][\w.]*\s*=\s*(true|false)\b`)
	reLikePrefix              = regexp.MustCompile(`(?i)\b(?:like|ilike)\s+'%`)
	reTimeCondition           = regexp.MustCompile(`(?i)(<|<=|>|>=)\s*(now\s*\(\s*\)|current_timestamp\b)`)
	reSetExpression           = regexp.MustCompile(`(?is)\bSET\s+(.+?)(?:\bWHERE\b|\bRETURNING\b|$)`)
	reNotValid                = regexp.MustCompile(`(?i)\bNOT\s+VALID\b`)
)

type blockedRule struct {
	re     *regexp.Regexp
	typ    string
	reason string
}

func AnalyzeSQLRisk(sqlText string) RiskAnalysis {
	tree, err := pg_query.Parse(sqlText)
	if err != nil {
		return RiskAnalysis{SQLType: "SQL_PARSE_ERROR", RiskLevel: "BLOCKED", RiskReason: "SQL 语法解析失败: " + err.Error()}
	}
	if reason, ok := missingExplicitSchemaReason(tree); ok {
		return RiskAnalysis{SQLType: "MISSING_SCHEMA", RiskLevel: "BLOCKED", RiskReason: reason}
	}
	if risk, ok := analyzeSQLRiskFromAST(tree); ok {
		return enrichStaticRisk(sqlText, classifyDMLExpressionRisk(sqlText, risk))
	}

	normalized := normalizeSQL(sqlText)
	upper := strings.ToUpper(normalized)

	blocked := []blockedRule{
		{reDropDatabase, "DROP_DATABASE", "禁止通过普通入口删除数据库"},
		{reDropSchema, "DROP_SCHEMA", "禁止通过普通入口删除 schema"},
		{reDropOwned, "DROP_OWNED", "禁止执行 DROP OWNED"},
		{reAlterSystem, "ALTER_SYSTEM", "禁止修改数据库系统级参数"},
		{reCopyProgram, "COPY_PROGRAM", "禁止执行 COPY PROGRAM"},
		{reReindexDatabase, "REINDEX_DATABASE", "禁止执行 REINDEX DATABASE"},
		{reVacuumFull, "VACUUM_FULL", "禁止执行 VACUUM FULL"},
	}
	for _, rule := range blocked {
		if rule.re.MatchString(upper) {
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
	case reCreateTable.MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_TABLE", RiskLevel: "LOW"}
	case reCreateOrReplaceView.MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}
	case reAlterTableAddColumn.MatchString(upper):
		return RiskAnalysis{SQLType: "ADD_COLUMN", RiskLevel: "LOW"}
	case reAlterTableAlterType.MatchString(upper):
		return enrichStaticRisk(sqlText, RiskAnalysis{SQLType: "ALTER_COLUMN_TYPE", RiskLevel: "WARN", RiskReason: "字段类型变更可能触发表重写或被视图依赖阻塞"})
	case reAlterTableSetNotNull.MatchString(upper):
		return RiskAnalysis{SQLType: "ALTER_SET_NOT_NULL", RiskLevel: "WARN", RiskReason: "设置 NOT NULL 可能扫描全表"}
	case reAlterTableAddCheck.MatchString(upper):
		return RiskAnalysis{SQLType: "ADD_CHECK", RiskLevel: "WARN", RiskReason: "新增 CHECK 默认校验已有数据，可能扫描全表"}
	case reCreateIndexConcurrently.MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: "并发索引耗时可能较长"}
	case reCreateIndex.MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入"}
	case reTruncate.MatchString(upper):
		return enrichStaticRisk(sqlText, RiskAnalysis{SQLType: "TRUNCATE", RiskLevel: "WARN", RiskReason: "TRUNCATE 会快速清空表数据，请确认对象范围"})
	case reDropTable.MatchString(upper):
		return enrichStaticRisk(sqlText, RiskAnalysis{SQLType: "DROP_TABLE", RiskLevel: "WARN", RiskReason: "DROP TABLE 会删除表结构和数据，请确认对象范围"})
	default:
		// 未识别的语句类型默认需要人工确认，避免新语法或少见语句被当作低风险直接执行
		return RiskAnalysis{SQLType: firstKeyword(upper), RiskLevel: "WARN", RiskReason: "未识别的语句类型，风险分析无法覆盖，请人工确认"}
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
	default:
		// DROP TABLE / TRUNCATE 已在 AST 分析中按全部目标表和 CASCADE 分类，这里不再按首个表名覆盖
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
	if reNotValid.MatchString(sqlText) {
		return RiskAnalysis{SQLType: "ADD_CHECK_NOT_VALID", RiskLevel: "LOW", RiskReason: "CHECK 使用 NOT VALID，不立即校验已有数据"}
	}
	return risk
}

func classifyTrivialDMLWhere(sqlText string, risk RiskAnalysis) RiskAnalysis {
	if risk.SQLType != "UPDATE" && risk.SQLType != "DELETE" {
		return risk
	}
	whereExpr := extractDMLWhereExpression(sqlText)
	operation := risk.SQLType
	if isTrivialWhereExpression(whereExpr) {
		if operation == "UPDATE" {
			return RiskAnalysis{SQLType: "UPDATE_TRIVIAL_WHERE", RiskLevel: "BLOCKED", RiskReason: "UPDATE 条件无有效过滤条件，禁止直接执行"}
		}
		return RiskAnalysis{SQLType: "DELETE_TRIVIAL_WHERE", RiskLevel: "BLOCKED", RiskReason: "DELETE 条件无有效过滤条件，禁止直接执行"}
	}
	if reason, blocked := weakDMLWhereReason(whereExpr); reason != "" {
		sqlType := operation + "_WEAK_WHERE"
		if blocked {
			return RiskAnalysis{SQLType: sqlType, RiskLevel: "BLOCKED", RiskReason: operation + " 条件过弱: " + reason}
		}
		return RiskAnalysis{SQLType: sqlType, RiskLevel: "WARN", RiskReason: operation + " 条件可能命中大量数据: " + reason}
	}
	return risk
}

func classifyDMLExpressionRisk(sqlText string, risk RiskAnalysis) RiskAnalysis {
	risk = classifyTrivialDMLWhere(sqlText, risk)
	if risk.RiskLevel == "BLOCKED" || !strings.HasPrefix(risk.SQLType, "UPDATE") {
		return risk
	}
	if reason := riskyUpdateSetReason(sqlText); reason != "" {
		if risk.RiskLevel == "WARN" {
			risk.RiskReason = appendRiskText(risk.RiskReason, reason)
			return risk
		}
		return RiskAnalysis{SQLType: "UPDATE_RISKY_SET", RiskLevel: "WARN", RiskReason: reason}
	}
	return risk
}

func extractDMLWhereExpression(sqlText string) string {
	normalized := strings.TrimSuffix(normalizeSQL(sqlText), ";")
	matches := reWhereClause.FindStringSubmatch(normalized)
	if len(matches) < 2 {
		return ""
	}
	whereExpr := matches[1]
	whereExpr = reReturningClause.ReplaceAllString(whereExpr, "")
	return strings.TrimSpace(whereExpr)
}

func isTrivialWhereExpression(whereExpr string) bool {
	normalized := strings.ToLower(strings.TrimSpace(whereExpr))
	for strings.HasPrefix(normalized, "(") && strings.HasSuffix(normalized, ")") {
		normalized = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(normalized, "("), ")"))
	}
	compact := reWhitespace.ReplaceAllString(normalized, "")
	return compact == "true" || compact == "1=1"
}

func weakDMLWhereReason(whereExpr string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(whereExpr))
	if reIsNotNull.MatchString(normalized) {
		return "IS NOT NULL 通常不能有效限制数据范围", true
	}
	if reBooleanCondition.MatchString(normalized) {
		return "布尔条件可能命中大量数据，请结合 EXPLAIN 估算影响行数", false
	}
	if reLikePrefix.MatchString(normalized) {
		return "LIKE 前缀通配符可能导致全表扫描", false
	}
	if reTimeCondition.MatchString(normalized) {
		return "时间条件使用当前时间且缺少固定边界，请确认影响范围", false
	}
	return "", false
}

// Pre-compiled regexes for riskyUpdateSetReason
var (
	reSetNull       = regexp.MustCompile(`(?i)=\s*null\b`)
	reSetArithmetic = regexp.MustCompile(`(?i)=\s*[a-zA-Z_][\w.]*\s*[-+*/]\s*`)
	reSetEmptyJSON  = regexp.MustCompile(`(?i)=\s*'(\{\}|\[\])'\s*(::\s*jsonb?)?`)
	reSetDeleted    = regexp.MustCompile(`(?i)\b(status|state)\b\s*=\s*'(deleted|delete|removed|invalid)'`)
)

func riskyUpdateSetReason(sqlText string) string {
	setExpr := extractUpdateSetExpression(sqlText)
	if setExpr == "" {
		return ""
	}
	checks := []struct {
		re     *regexp.Regexp
		reason string
	}{
		{reSetNull, "UPDATE 将字段置空，请确认业务含义和影响范围"},
		{reSetArithmetic, "UPDATE 使用算术表达式批量改值，请确认计算逻辑"},
		{reSetEmptyJSON, "UPDATE 清空 JSON 字段，请确认是否会覆盖已有扩展数据"},
		{reSetDeleted, "UPDATE 将状态设置为删除状态，请确认是否应使用软删除流程"},
	}
	for _, check := range checks {
		if check.re.MatchString(setExpr) {
			return check.reason
		}
	}
	return ""
}

func extractUpdateSetExpression(sqlText string) string {
	normalized := strings.TrimSuffix(normalizeSQL(sqlText), ";")
	matches := reSetExpression.FindStringSubmatch(normalized)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func extractAlterColumnTargetType(sqlText string) string {
	matches := reAlterColumnTargetType.FindStringSubmatch(sqlText)
	if len(matches) < 2 {
		return ""
	}
	typeName := strings.TrimSpace(matches[1])
	if idx := reAlterColumnUsingInType.FindStringIndex(typeName); idx != nil {
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
	return reAlterColumnUsing.MatchString(sqlText)
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
	case "ALTER_COLUMN_TYPE", "ALTER_SET_NOT_NULL", "ADD_CHECK", "CREATE_INDEX", "CREATE_UNIQUE_INDEX", "CREATE_INDEX_EXPRESSION", "CREATE_INDEX_PARTIAL", "ADD_COLUMN_DEFAULT_VOLATILE":
		return true
	default:
		return false
	}
}

func isDMLRiskWithAffectedRows(sqlType string) bool {
	return strings.HasPrefix(sqlType, "UPDATE") || strings.HasPrefix(sqlType, "DELETE")
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
	risk, ok := analyzeStmtNode(node)
	if !ok {
		return risk, false
	}
	return applyWritableCTERisk(node, risk), true
}

func analyzeStmtNode(node *pg_query.Node) (RiskAnalysis, bool) {
	switch {
	case node.GetInsertStmt() != nil:
		return RiskAnalysis{SQLType: "INSERT", RiskLevel: "LOW"}, true
	case node.GetUpdateStmt() != nil:
		return analyzeUpdateWhere(node.GetUpdateStmt().GetWhereClause()), true
	case node.GetDeleteStmt() != nil:
		return analyzeDeleteWhere(node.GetDeleteStmt().GetWhereClause()), true
	case node.GetMergeStmt() != nil:
		return RiskAnalysis{SQLType: "MERGE", RiskLevel: "WARN", RiskReason: "MERGE 可能批量插入、更新或删除数据，请确认匹配条件和影响范围"}, true
	case node.GetSelectStmt() != nil:
		return RiskAnalysis{SQLType: "SELECT", RiskLevel: "LOW"}, true
	case node.GetExplainStmt() != nil:
		return analyzeExplainStmt(node.GetExplainStmt())
	case node.GetCreateStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE", RiskLevel: "LOW"}, true
	case node.GetCreateTableAsStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE_AS", RiskLevel: "WARN", RiskReason: "CREATE TABLE AS 可能写入大量数据，请确认数据规模"}, true
	case node.GetViewStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}, true
	case node.GetCreateFunctionStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_FUNCTION", RiskLevel: "WARN", RiskReason: "创建或替换函数可能改变业务逻辑，请确认依赖范围"}, true
	case node.GetCreateTrigStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TRIGGER", RiskLevel: "WARN", RiskReason: "创建触发器会改变表写入行为，请确认触发时机和函数逻辑"}, true
	case node.GetCreateExtensionStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_EXTENSION", RiskLevel: "WARN", RiskReason: "创建扩展会修改数据库能力和对象，请确认权限和兼容性"}, true
	case node.GetAlterSystemStmt() != nil:
		return RiskAnalysis{SQLType: "ALTER_SYSTEM", RiskLevel: "BLOCKED", RiskReason: "禁止修改数据库系统级参数"}, true
	case node.GetDropOwnedStmt() != nil:
		return RiskAnalysis{SQLType: "DROP_OWNED", RiskLevel: "BLOCKED", RiskReason: "禁止执行 DROP OWNED"}, true
	case node.GetReassignOwnedStmt() != nil:
		return RiskAnalysis{SQLType: "REASSIGN_OWNED", RiskLevel: "BLOCKED", RiskReason: "禁止执行 REASSIGN OWNED"}, true
	case node.GetCreateRoleStmt() != nil, node.GetAlterRoleStmt() != nil, node.GetAlterRoleSetStmt() != nil, node.GetDropRoleStmt() != nil:
		return RiskAnalysis{SQLType: "ROLE_MANAGEMENT", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口创建、修改或删除数据库角色"}, true
	case node.GetGrantStmt() != nil, node.GetGrantRoleStmt() != nil, node.GetAlterDefaultPrivilegesStmt() != nil:
		return RiskAnalysis{SQLType: "GRANT", RiskLevel: "WARN", RiskReason: "授权或回收权限会影响数据访问控制，请确认授权对象和范围"}, true
	case node.GetAlterDatabaseStmt() != nil, node.GetAlterDatabaseSetStmt() != nil, node.GetAlterDatabaseRefreshCollStmt() != nil:
		return RiskAnalysis{SQLType: "ALTER_DATABASE", RiskLevel: "WARN", RiskReason: "修改数据库级配置会影响所有连接，请确认影响范围"}, true
	case node.GetDoStmt() != nil:
		return RiskAnalysis{SQLType: "DO_BLOCK", RiskLevel: "WARN", RiskReason: "DO 匿名代码块可执行任意动态 SQL，风险分析无法覆盖块内语句，请人工确认"}, true
	case node.GetCallStmt() != nil:
		return RiskAnalysis{SQLType: "CALL", RiskLevel: "WARN", RiskReason: "CALL 存储过程会执行过程内逻辑，风险分析无法覆盖，请人工确认"}, true
	case node.GetTransactionStmt() != nil:
		return RiskAnalysis{SQLType: "TRANSACTION_CONTROL", RiskLevel: "WARN", RiskReason: "平台按条执行 SQL，BEGIN/COMMIT 等事务控制语句不保证在同一连接生效，失败时已标记成功的语句可能被回滚"}, true
	case node.GetVariableSetStmt() != nil:
		return RiskAnalysis{SQLType: "SET", RiskLevel: "WARN", RiskReason: "SET 只影响当前会话，平台按条执行时不保证对后续语句生效"}, true
	case node.GetLockStmt() != nil:
		return RiskAnalysis{SQLType: "LOCK_TABLE", RiskLevel: "WARN", RiskReason: "显式锁表可能阻塞业务读写，请确认锁模式和执行窗口"}, true
	case node.GetClusterStmt() != nil:
		return RiskAnalysis{SQLType: "CLUSTER", RiskLevel: "WARN", RiskReason: "CLUSTER 会重写表并持有排他锁，请确认执行窗口"}, true
	case node.GetCommentStmt() != nil:
		return RiskAnalysis{SQLType: "COMMENT", RiskLevel: "LOW"}, true
	case node.GetCreateSeqStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_SEQUENCE", RiskLevel: "LOW"}, true
	case node.GetAlterSeqStmt() != nil:
		return RiskAnalysis{SQLType: "ALTER_SEQUENCE", RiskLevel: "LOW"}, true
	case node.GetCreateSchemaStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_SCHEMA", RiskLevel: "LOW"}, true
	case node.GetCreateEnumStmt() != nil, node.GetAlterEnumStmt() != nil, node.GetCompositeTypeStmt() != nil, node.GetCreateDomainStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TYPE", RiskLevel: "LOW"}, true
	case node.GetCopyStmt() != nil:
		copyStmt := node.GetCopyStmt()
		if copyStmt.GetIsProgram() {
			return RiskAnalysis{SQLType: "COPY_PROGRAM", RiskLevel: "BLOCKED", RiskReason: "禁止执行 COPY PROGRAM"}, true
		}
		if strings.TrimSpace(copyStmt.GetFilename()) != "" {
			return RiskAnalysis{SQLType: "COPY_FILE", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口读写数据库服务器本地文件"}, true
		}
		return RiskAnalysis{SQLType: "COPY", RiskLevel: "WARN", RiskReason: "COPY 可能批量导入或导出数据，请确认文件和数据范围"}, true
	case node.GetDropStmt() != nil:
		return analyzeDropStmt(node.GetDropStmt()), true
	case node.GetRenameStmt() != nil:
		return RiskAnalysis{SQLType: "RENAME_OBJECT", RiskLevel: "WARN", RiskReason: "重命名数据库对象可能影响业务 SQL、视图或程序引用"}, true
	case node.GetAlterObjectSchemaStmt() != nil:
		return RiskAnalysis{SQLType: "ALTER_SET_SCHEMA", RiskLevel: "WARN", RiskReason: "迁移 schema 会改变对象命名空间和依赖解析，请确认引用、权限和同名对象影响"}, true
	case node.GetAlterTableStmt() != nil:
		return analyzeAlterTableStmt(node.GetAlterTableStmt()), true
	case node.GetIndexStmt() != nil:
		return analyzeIndexStmt(node.GetIndexStmt()), true
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

func analyzeUpdateWhere(where *pg_query.Node) RiskAnalysis {
	if where == nil {
		return RiskAnalysis{SQLType: "UPDATE_WITHOUT_WHERE", RiskLevel: "BLOCKED", RiskReason: "UPDATE 缺少 WHERE 条件，禁止直接执行"}
	}
	if !protoMessageContainsColumnRef(where.ProtoReflect()) {
		return RiskAnalysis{SQLType: "UPDATE_TRIVIAL_WHERE", RiskLevel: "BLOCKED", RiskReason: "UPDATE 条件无有效过滤条件（未引用任何字段），禁止直接执行"}
	}
	return RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}
}

func analyzeDeleteWhere(where *pg_query.Node) RiskAnalysis {
	if where == nil {
		return RiskAnalysis{SQLType: "DELETE_WITHOUT_WHERE", RiskLevel: "BLOCKED", RiskReason: "DELETE 缺少 WHERE 条件，禁止直接执行"}
	}
	if !protoMessageContainsColumnRef(where.ProtoReflect()) {
		return RiskAnalysis{SQLType: "DELETE_TRIVIAL_WHERE", RiskLevel: "BLOCKED", RiskReason: "DELETE 条件无有效过滤条件（未引用任何字段），禁止直接执行"}
	}
	return RiskAnalysis{SQLType: "DELETE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}
}

func analyzeExplainStmt(stmt *pg_query.ExplainStmt) (RiskAnalysis, bool) {
	analyze := false
	for _, optionNode := range stmt.GetOptions() {
		def := optionNode.GetDefElem()
		if def == nil || !strings.EqualFold(def.GetDefname(), "analyze") {
			continue
		}
		analyze = true
		if arg := def.GetArg(); arg != nil {
			if b := arg.GetBoolean(); b != nil && !b.GetBoolval() {
				analyze = false
			}
			if str := arg.GetString_(); str != nil && (strings.EqualFold(str.GetSval(), "false") || strings.EqualFold(str.GetSval(), "off")) {
				analyze = false
			}
		}
	}
	if !analyze || stmt.GetQuery() == nil {
		return RiskAnalysis{SQLType: "EXPLAIN", RiskLevel: "LOW"}, true
	}
	// EXPLAIN ANALYZE 会真实执行内部语句，按内部语句评估风险
	risk, ok := analyzeStmtNode(stmt.GetQuery())
	if !ok {
		return RiskAnalysis{SQLType: "EXPLAIN_ANALYZE", RiskLevel: "WARN", RiskReason: "EXPLAIN ANALYZE 会真实执行语句，请人工确认"}, true
	}
	return applyWritableCTERisk(stmt.GetQuery(), risk), true
}

// applyWritableCTERisk 检查 WITH 子句中的 DELETE/UPDATE/MERGE，避免通过可写 CTE 绕过 WHERE 检查
func applyWritableCTERisk(node *pg_query.Node, risk RiskAnalysis) RiskAnalysis {
	for _, cteQuery := range collectCTEQueries(node.ProtoReflect(), nil) {
		var cteRisk RiskAnalysis
		switch {
		case cteQuery.GetDeleteStmt() != nil:
			cteRisk = analyzeDeleteWhere(cteQuery.GetDeleteStmt().GetWhereClause())
		case cteQuery.GetUpdateStmt() != nil:
			cteRisk = analyzeUpdateWhere(cteQuery.GetUpdateStmt().GetWhereClause())
		case cteQuery.GetMergeStmt() != nil:
			cteRisk = RiskAnalysis{SQLType: "MERGE", RiskLevel: "WARN", RiskReason: "MERGE 可能批量插入、更新或删除数据，请确认匹配条件和影响范围"}
		case cteQuery.GetInsertStmt() != nil:
			cteRisk = RiskAnalysis{SQLType: "INSERT", RiskLevel: "LOW"}
		default:
			continue
		}
		if cteRisk.RiskLevel != "BLOCKED" {
			cteRisk = RiskAnalysis{SQLType: "WRITABLE_CTE", RiskLevel: "WARN", RiskReason: appendRiskText("WITH 子句中包含数据修改语句，请确认影响范围", cteRisk.RiskReason)}
		}
		if riskRank(cteRisk.RiskLevel) > riskRank(risk.RiskLevel) {
			risk = cteRisk
		}
	}
	return risk
}

func collectCTEQueries(message protoreflect.Message, queries []*pg_query.Node) []*pg_query.Node {
	if !message.IsValid() {
		return queries
	}
	if cte, ok := message.Interface().(*pg_query.CommonTableExpr); ok && cte.GetCtequery() != nil {
		queries = append(queries, cte.GetCtequery())
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
				queries = collectCTEQueries(list.Get(j).Message(), queries)
			}
			continue
		}
		if value.Message().IsValid() {
			queries = collectCTEQueries(value.Message(), queries)
		}
	}
	return queries
}

func protoMessageContainsColumnRef(message protoreflect.Message) bool {
	if !message.IsValid() {
		return false
	}
	if _, ok := message.Interface().(*pg_query.ColumnRef); ok {
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
				if protoMessageContainsColumnRef(list.Get(j).Message()) {
					return true
				}
			}
			continue
		}
		if value.Message().IsValid() && protoMessageContainsColumnRef(value.Message()) {
			return true
		}
	}
	return false
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
	case node.GetCreateTrigStmt() != nil:
		return []schemaRequiredTarget{{name: "CREATE TRIGGER 目标表", rel: node.GetCreateTrigStmt().GetRelation()}}
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
	case pg_query.ObjectType_OBJECT_TABLE, pg_query.ObjectType_OBJECT_VIEW, pg_query.ObjectType_OBJECT_MATVIEW, pg_query.ObjectType_OBJECT_INDEX, pg_query.ObjectType_OBJECT_FUNCTION, pg_query.ObjectType_OBJECT_TRIGGER:
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
		return applyCascadeRisk(classifyDestructiveTables("DROP_TABLE", dropObjectNames(stmt)), stmt.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE)
	case pg_query.ObjectType_OBJECT_INDEX:
		if stmt.GetConcurrent() {
			return RiskAnalysis{SQLType: "DROP_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: "并发删除索引耗时可能较长"}
		}
		return RiskAnalysis{SQLType: "DROP_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 删除索引可能阻塞访问"}
	case pg_query.ObjectType_OBJECT_FUNCTION:
		return RiskAnalysis{SQLType: "DROP_FUNCTION", RiskLevel: "WARN", RiskReason: "删除函数可能影响依赖对象或业务调用"}
	case pg_query.ObjectType_OBJECT_EXTENSION:
		return RiskAnalysis{SQLType: "DROP_EXTENSION", RiskLevel: "WARN", RiskReason: "删除扩展会影响扩展对象和依赖功能"}
	case pg_query.ObjectType_OBJECT_TRIGGER:
		return RiskAnalysis{SQLType: "DROP_TRIGGER", RiskLevel: "WARN", RiskReason: "删除触发器会改变表写入行为，请确认业务影响"}
	default:
		return applyCascadeRisk(RiskAnalysis{SQLType: "DROP", RiskLevel: "WARN", RiskReason: "DROP 会删除数据库对象，请确认对象范围"}, stmt.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE)
	}
}

func analyzeTruncateStmt(stmt *pg_query.TruncateStmt) RiskAnalysis {
	names := make([]string, 0, len(stmt.GetRelations()))
	for _, relNode := range stmt.GetRelations() {
		if rel := relNode.GetRangeVar(); rel != nil {
			names = append(names, rel.GetRelname())
		}
	}
	return applyCascadeRisk(classifyDestructiveTables("TRUNCATE", names), stmt.GetBehavior() == pg_query.DropBehavior_DROP_CASCADE)
}

// classifyDestructiveTables 按全部目标表分类，取最高风险，避免只看第一个表名被临时表命名绕过
func classifyDestructiveTables(sqlType string, names []string) RiskAnalysis {
	var result RiskAnalysis
	found := false
	for _, name := range names {
		risk := classifyDestructiveTableOperation(sqlType, TableStats{TableName: name})
		if !found || riskRank(risk.RiskLevel) > riskRank(result.RiskLevel) {
			result = risk
			found = true
		}
	}
	if !found {
		return classifyDestructiveTableOperation(sqlType, TableStats{})
	}
	return result
}

func applyCascadeRisk(risk RiskAnalysis, cascade bool) RiskAnalysis {
	if !cascade {
		return risk
	}
	if riskRank(risk.RiskLevel) < riskRank("WARN") {
		risk.RiskLevel = "WARN"
	}
	risk.RiskReason = appendRiskText(risk.RiskReason, "CASCADE 会级联删除或清空依赖对象，请确认影响范围")
	return risk
}

func analyzeIndexStmt(stmt *pg_query.IndexStmt) RiskAnalysis {
	reasons := make([]string, 0, 4)
	if stmt.GetUnique() {
		reasons = append(reasons, "唯一索引会校验全表唯一性，可能因重复数据失败")
	}
	if indexHasExpression(stmt) {
		reasons = append(reasons, "表达式索引会计算表达式并可能长时间持锁")
	}
	if stmt.GetWhereClause() != nil {
		reasons = append(reasons, "部分索引需要确认 WHERE 条件能覆盖业务查询")
	}
	if stmt.GetConcurrent() {
		reasons = append([]string{"并发索引耗时可能较长"}, reasons...)
		return RiskAnalysis{SQLType: "CREATE_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: strings.Join(reasons, "；")}
	}
	if len(reasons) == 0 {
		return RiskAnalysis{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入"}
	}
	if stmt.GetUnique() {
		return RiskAnalysis{SQLType: "CREATE_UNIQUE_INDEX", RiskLevel: "WARN", RiskReason: "创建" + strings.Join(reasons, "；")}
	}
	if indexHasExpression(stmt) {
		return RiskAnalysis{SQLType: "CREATE_INDEX_EXPRESSION", RiskLevel: "WARN", RiskReason: "创建" + strings.Join(reasons, "；")}
	}
	return RiskAnalysis{SQLType: "CREATE_INDEX_PARTIAL", RiskLevel: "WARN", RiskReason: "创建" + strings.Join(reasons, "；")}
}

func indexHasExpression(stmt *pg_query.IndexStmt) bool {
	for _, paramNode := range stmt.GetIndexParams() {
		indexElem := paramNode.GetIndexElem()
		if indexElem != nil && indexElem.GetExpr() != nil {
			return true
		}
	}
	return false
}

func dropObjectNames(stmt *pg_query.DropStmt) []string {
	names := make([]string, 0, len(stmt.GetObjects()))
	for _, obj := range stmt.GetObjects() {
		parts := stringListFromNode(obj)
		if len(parts) > 0 {
			names = append(names, parts[len(parts)-1])
		}
	}
	return names
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
		case pg_query.AlterTableType_AT_DropColumn:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "DROP_COLUMN", RiskLevel: "WARN", RiskReason: "删除字段会造成数据丢失，并可能导致依赖对象失效"})
		case pg_query.AlterTableType_AT_DropConstraint:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "DROP_CONSTRAINT", RiskLevel: "WARN", RiskReason: "删除约束可能破坏数据一致性，请确认约束类型和依赖范围"})
		case pg_query.AlterTableType_AT_ChangeOwner:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ALTER_OWNER", RiskLevel: "WARN", RiskReason: "修改 OWNER 可能影响权限、DDL 和后续维护"})
		case pg_query.AlterTableType_AT_DisableTrig, pg_query.AlterTableType_AT_DisableTrigAll, pg_query.AlterTableType_AT_DisableTrigUser:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "DISABLE_TRIGGER", RiskLevel: "WARN", RiskReason: "禁用触发器可能破坏审计、同步或数据一致性，请确认执行窗口和恢复步骤"})
		case pg_query.AlterTableType_AT_EnableTrig, pg_query.AlterTableType_AT_EnableTrigAll, pg_query.AlterTableType_AT_EnableTrigUser:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ENABLE_TRIGGER", RiskLevel: "WARN", RiskReason: "启用触发器会改变表写入行为，请确认业务影响"})
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
