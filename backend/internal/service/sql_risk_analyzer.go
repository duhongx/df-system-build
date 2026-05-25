package service

import (
	"regexp"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

func AnalyzeSQLRisk(sqlText string) RiskAnalysis {
	tree, err := pg_query.Parse(sqlText)
	if err != nil {
		return RiskAnalysis{SQLType: "SQL_PARSE_ERROR", RiskLevel: "BLOCKED", RiskReason: "SQL 语法解析失败: " + err.Error()}
	}
	if risk, ok := analyzeSQLRiskFromAST(tree); ok {
		return enrichStaticRisk(sqlText, risk)
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
		return RiskAnalysis{SQLType: "TRUNCATE", RiskLevel: "WARN", RiskReason: "TRUNCATE 会快速清空表数据，请确认对象范围"}
	case regexp.MustCompile(`^DROP\s+TABLE\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "DROP_TABLE", RiskLevel: "WARN", RiskReason: "DROP TABLE 会删除表结构和数据，请确认对象范围"}
	default:
		return RiskAnalysis{SQLType: firstKeyword(upper), RiskLevel: "LOW"}
	}
}

func enrichStaticRisk(sqlText string, risk RiskAnalysis) RiskAnalysis {
	if risk.SQLType != "ALTER_COLUMN_TYPE" {
		return risk
	}
	return classifyStaticColumnTypeChange(sqlText, risk)
}

func classifyStaticColumnTypeChange(sqlText string, risk RiskAnalysis) RiskAnalysis {
	targetType := extractAlterColumnTargetType(sqlText)
	hasUsing := hasAlterColumnUsing(sqlText)
	reasons := make([]string, 0, 3)

	if strings.TrimSpace(targetType) != "" && isVarcharType(targetType) {
		reasons = append(reasons, "varchar 类型变更需要结合原字段长度判断，避免缩容截断或误判扩容")
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

func hasAlterColumnUsing(sqlText string) bool {
	return regexp.MustCompile(`(?i)\bUSING\b`).MatchString(sqlText)
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
		return RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}, true
	case node.GetDeleteStmt() != nil:
		return RiskAnalysis{SQLType: "DELETE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}, true
	case node.GetCreateStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE", RiskLevel: "LOW"}, true
	case node.GetCreateTableAsStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE_AS", RiskLevel: "WARN", RiskReason: "CREATE TABLE AS 可能写入大量数据，请确认数据规模"}, true
	case node.GetViewStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}, true
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
		return RiskAnalysis{SQLType: "TRUNCATE", RiskLevel: "WARN", RiskReason: "TRUNCATE 会快速清空表数据，请确认对象范围"}, true
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
	default:
		return RiskAnalysis{}, false
	}
}

func analyzeDropStmt(stmt *pg_query.DropStmt) RiskAnalysis {
	switch stmt.GetRemoveType() {
	case pg_query.ObjectType_OBJECT_DATABASE:
		return RiskAnalysis{SQLType: "DROP_DATABASE", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口删除数据库"}
	case pg_query.ObjectType_OBJECT_SCHEMA:
		return RiskAnalysis{SQLType: "DROP_SCHEMA", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口删除 schema"}
	case pg_query.ObjectType_OBJECT_TABLE:
		return RiskAnalysis{SQLType: "DROP_TABLE", RiskLevel: "WARN", RiskReason: "DROP TABLE 会删除表结构和数据，请确认对象范围"}
	default:
		return RiskAnalysis{SQLType: "DROP", RiskLevel: "WARN", RiskReason: "DROP 会删除数据库对象，请确认对象范围"}
	}
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
			if constraint := cmd.GetDef().GetConstraint(); constraint != nil && constraint.GetContype() == pg_query.ConstrType_CONSTR_CHECK {
				risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_CHECK", RiskLevel: "WARN", RiskReason: "新增 CHECK 默认校验已有数据，可能扫描全表"})
			}
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
