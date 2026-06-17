package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"df-build-server/internal/model"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

type ViewDependency struct {
	Schema     string
	View       string
	Kind       string
	Definition string
}

type ViewRebuildPlan struct {
	DropSQL   string
	CreateSQL string
}

type SQLViewDependencyTaskRequest struct {
	SchemaName       string `json:"schemaName" binding:"required"`
	TableName        string `json:"tableName" binding:"required"`
	ColumnName       string `json:"columnName" binding:"required"`
	AlterSQL         string `json:"alterSql" binding:"required"`
	LockTimeout      string `json:"lockTimeout"`
	StatementTimeout string `json:"statementTimeout"`
}

type viewDependencySnapshot struct {
	Schema        string
	Name          string
	Kind          string
	Depth         int
	Definition    string
	Owner         string
	GrantSQL      []string
	CommentSQL    []string
	IndexSQL      []string
	OptionsJSON   string
	DropOrder     int
	RestoreOrder  int
	Materialized  bool
	AdditionalSQL []string
}

func BuildViewRebuildPlan(dep ViewDependency) ViewRebuildPlan {
	qualifiedName := quoteQualifiedName(dep.Schema, dep.View)
	definition := strings.TrimSpace(dep.Definition)
	createSQL := fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s;", qualifiedName, strings.TrimSuffix(definition, ";"))
	dropObject := "VIEW"
	if dep.Kind == "m" {
		dropObject = "MATERIALIZED VIEW"
		createSQL = fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS\n%s\nWITH NO DATA;", qualifiedName, strings.TrimSuffix(definition, ";"))
	}
	return ViewRebuildPlan{
		DropSQL:   fmt.Sprintf("DROP %s IF EXISTS %s;", dropObject, qualifiedName),
		CreateSQL: createSQL,
	}
}

func quoteQualifiedName(schemaName, objectName string) string {
	schemaName = strings.TrimSpace(schemaName)
	objectName = strings.TrimSpace(objectName)
	if schemaName == "" {
		return quoteIdent(objectName)
	}
	return quoteIdent(schemaName) + "." + quoteIdent(objectName)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(strings.TrimSpace(name), `"`, `""`) + `"`
}

func validateViewDependencyAlterSQL(req SQLViewDependencyTaskRequest) (alterColumnRef, error) {
	req.SchemaName = strings.TrimSpace(req.SchemaName)
	req.TableName = strings.TrimSpace(req.TableName)
	req.ColumnName = strings.TrimSpace(req.ColumnName)
	req.AlterSQL = strings.TrimSpace(req.AlterSQL)
	if req.SchemaName == "" || req.TableName == "" || req.ColumnName == "" || req.AlterSQL == "" {
		return alterColumnRef{}, fmt.Errorf("schema、table、column 和 ALTER SQL 不能为空")
	}
	if err := validateSchemaName(req.SchemaName); err != nil {
		return alterColumnRef{}, err
	}
	tree, err := pg_query.Parse(req.AlterSQL)
	if err != nil {
		return alterColumnRef{}, fmt.Errorf("ALTER SQL 解析失败: %w", err)
	}
	if len(tree.GetStmts()) != 1 {
		return alterColumnRef{}, fmt.Errorf("只允许一条 ALTER TABLE 语句")
	}
	analysis := AnalyzeSQLRisk(req.AlterSQL)
	if analysis.SQLType != "ALTER_COLUMN_TYPE" {
		return alterColumnRef{}, fmt.Errorf("只支持 ALTER TABLE 修改字段类型语句")
	}
	ref := parseAlterColumnTypeRef(req.AlterSQL, "")
	if ref.schema == "" {
		return alterColumnRef{}, fmt.Errorf("ALTER SQL 必须显式指定 schema.table")
	}
	if !strings.EqualFold(ref.schema, req.SchemaName) || !strings.EqualFold(ref.table, req.TableName) || !strings.EqualFold(ref.column, req.ColumnName) {
		return alterColumnRef{}, fmt.Errorf("ALTER SQL 目标字段与入参不一致")
	}
	return ref, nil
}

func BuildSQLViewDependencyManualPlan(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) string {
	dropItems := append([]model.SQLViewDependencyItem(nil), items...)
	sort.SliceStable(dropItems, func(i, j int) bool {
		if dropItems[i].DropOrder == dropItems[j].DropOrder {
			return dropItems[i].ID < dropItems[j].ID
		}
		return dropItems[i].DropOrder < dropItems[j].DropOrder
	})
	restoreItems := append([]model.SQLViewDependencyItem(nil), items...)
	sort.SliceStable(restoreItems, func(i, j int) bool {
		if restoreItems[i].RestoreOrder == restoreItems[j].RestoreOrder {
			return restoreItems[i].ID < restoreItems[j].ID
		}
		return restoreItems[i].RestoreOrder < restoreItems[j].RestoreOrder
	})

	var b strings.Builder
	b.WriteString("-- View dependency change plan\n")
	b.WriteString(fmt.Sprintf("-- Target: %s.%s.%s\n\n", task.SchemaName, task.TableName, task.ColumnName))
	b.WriteString("-- Drop dependent views\n")
	for _, item := range dropItems {
		writeSQLLine(&b, item.DropSQL)
	}
	b.WriteString("\n-- Original ALTER SQL\n")
	writeSQLLine(&b, task.AlterSQL)
	b.WriteString("\n-- Restore dependent views\n")
	for _, item := range restoreItems {
		writeSQLLine(&b, item.CreateSQL)
		writeSQLLine(&b, item.RestoreOwnerSQL)
		writeSQLLine(&b, item.RestoreGrantsSQL)
		writeSQLLine(&b, item.RestoreCommentsSQL)
		writeSQLLine(&b, item.RestoreIndexesSQL)
	}
	b.WriteString("\n-- Verify\n")
	for _, item := range restoreItems {
		writeSQLLine(&b, item.VerifySQL)
	}
	return strings.TrimSpace(b.String())
}

func BuildSQLViewDependencyRestorePlan(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) string {
	restoreItems := orderedViewDependencyRestoreItems(items)
	var b strings.Builder
	b.WriteString("-- View dependency restore plan\n")
	b.WriteString(fmt.Sprintf("-- Target: %s.%s.%s\n\n", task.SchemaName, task.TableName, task.ColumnName))
	b.WriteString("-- Restore dependent views\n")
	for _, item := range restoreItems {
		writeSQLLine(&b, item.CreateSQL)
		writeSQLLine(&b, item.RestoreOwnerSQL)
		writeSQLLine(&b, item.RestoreGrantsSQL)
		writeSQLLine(&b, item.RestoreCommentsSQL)
		writeSQLLine(&b, item.RestoreIndexesSQL)
	}
	b.WriteString("\n-- Verify\n")
	for _, item := range restoreItems {
		writeSQLLine(&b, item.VerifySQL)
	}
	return strings.TrimSpace(b.String())
}

func BuildSQLViewDependencyTransactionalSteps(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) []string {
	steps := buildSQLViewDependencyTransactionPrefix(task)
	for _, item := range orderedViewDependencyDropItems(items) {
		appendSQLStep(&steps, item.DropSQL)
	}
	appendSQLStep(&steps, task.AlterSQL)
	appendSQLViewDependencyRestoreSteps(&steps, items)
	steps = append(steps, "COMMIT;")
	return steps
}

func BuildSQLViewDependencyRestoreTransactionalSteps(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) []string {
	steps := buildSQLViewDependencyTransactionPrefix(task)
	appendSQLViewDependencyRestoreSteps(&steps, items)
	steps = append(steps, "COMMIT;")
	return steps
}

func buildSQLViewDependencyTransactionPrefix(task model.SQLViewDependencyTask) []string {
	lockTimeout := strings.TrimSpace(task.LockTimeout)
	if lockTimeout == "" {
		lockTimeout = "3s"
	}
	statementTimeout := strings.TrimSpace(task.StatementTimeout)
	if statementTimeout == "" {
		statementTimeout = "10min"
	}
	steps := []string{
		"BEGIN;",
		fmt.Sprintf("SET LOCAL lock_timeout = '%s';", escapeSQLString(lockTimeout)),
		fmt.Sprintf("SET LOCAL statement_timeout = '%s';", escapeSQLString(statementTimeout)),
		"SET LOCAL idle_in_transaction_session_timeout = '60s';",
	}
	return steps
}

func appendSQLViewDependencyRestoreSteps(steps *[]string, items []model.SQLViewDependencyItem) {
	for _, item := range orderedViewDependencyRestoreItems(items) {
		appendSQLStep(steps, item.CreateSQL)
		appendSQLStep(steps, item.RestoreOwnerSQL)
		appendSQLStep(steps, item.RestoreGrantsSQL)
		appendSQLStep(steps, item.RestoreCommentsSQL)
		appendSQLStep(steps, item.RestoreIndexesSQL)
		appendSQLStep(steps, item.VerifySQL)
	}
}

func orderedViewDependencyDropItems(items []model.SQLViewDependencyItem) []model.SQLViewDependencyItem {
	ordered := append([]model.SQLViewDependencyItem(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].DropOrder == ordered[j].DropOrder {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].DropOrder < ordered[j].DropOrder
	})
	return ordered
}

func orderedViewDependencyRestoreItems(items []model.SQLViewDependencyItem) []model.SQLViewDependencyItem {
	ordered := append([]model.SQLViewDependencyItem(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].RestoreOrder == ordered[j].RestoreOrder {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].RestoreOrder < ordered[j].RestoreOrder
	})
	return ordered
}

func appendSQLStep(steps *[]string, sqlText string) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return
	}
	if !strings.HasSuffix(sqlText, ";") {
		sqlText += ";"
	}
	*steps = append(*steps, sqlText)
}

type sqlViewDependencyExecutor interface {
	Exec(sqlText string) error
}

func runSQLViewDependencySteps(executor sqlViewDependencyExecutor, steps []string) error {
	inTx := false
	for _, step := range steps {
		if err := executor.Exec(step); err != nil {
			if inTx {
				_ = executor.Exec("ROLLBACK;")
			}
			return err
		}
		switch strings.ToUpper(strings.TrimSpace(step)) {
		case "BEGIN;":
			inTx = true
		case "COMMIT;", "ROLLBACK;":
			inTx = false
		}
	}
	return nil
}

func writeSQLLine(b *strings.Builder, sqlText string) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return
	}
	b.WriteString(sqlText)
	if !strings.HasSuffix(sqlText, ";") {
		b.WriteString(";")
	}
	b.WriteString("\n")
}

func buildSQLViewDependencyItemFromSnapshot(taskID uint, snapshot viewDependencySnapshot) model.SQLViewDependencyItem {
	kind := strings.TrimSpace(snapshot.Kind)
	if kind == "" {
		if snapshot.Materialized {
			kind = "m"
		} else {
			kind = "v"
		}
	}
	plan := BuildViewRebuildPlan(ViewDependency{
		Schema:     snapshot.Schema,
		View:       snapshot.Name,
		Kind:       kind,
		Definition: snapshot.Definition,
	})
	objectType := "VIEW"
	if kind == "m" {
		objectType = "MATERIALIZED VIEW"
	}
	qualifiedName := quoteQualifiedName(snapshot.Schema, snapshot.Name)
	restoreOwnerSQL := ""
	if strings.TrimSpace(snapshot.Owner) != "" {
		restoreOwnerSQL = fmt.Sprintf("ALTER %s %s OWNER TO %s;", objectType, qualifiedName, quoteRoleIdent(snapshot.Owner))
	}
	grantsJSON := mustMarshalStringSlice(snapshot.GrantSQL)
	commentsJSON := mustMarshalStringSlice(snapshot.CommentSQL)
	indexesJSON := mustMarshalStringSlice(snapshot.IndexSQL)
	optionsJSON := strings.TrimSpace(snapshot.OptionsJSON)
	if optionsJSON == "" && len(snapshot.AdditionalSQL) > 0 {
		optionsJSON = mustMarshalStringSlice(snapshot.AdditionalSQL)
	}
	return model.SQLViewDependencyItem{
		TaskID:             taskID,
		ObjectSchema:       snapshot.Schema,
		ObjectName:         snapshot.Name,
		ObjectKind:         kind,
		Depth:              snapshot.Depth,
		DropOrder:          snapshot.DropOrder,
		RestoreOrder:       snapshot.RestoreOrder,
		Definition:         snapshot.Definition,
		OwnerName:          snapshot.Owner,
		GrantsJSON:         grantsJSON,
		CommentsJSON:       commentsJSON,
		IndexesJSON:        indexesJSON,
		OptionsJSON:        optionsJSON,
		DropSQL:            plan.DropSQL,
		CreateSQL:          strings.TrimSpace(plan.CreateSQL + "\n" + strings.Join(snapshot.AdditionalSQL, "\n")),
		RestoreOwnerSQL:    restoreOwnerSQL,
		RestoreGrantsSQL:   strings.Join(snapshot.GrantSQL, "\n"),
		RestoreCommentsSQL: strings.Join(snapshot.CommentSQL, "\n"),
		RestoreIndexesSQL:  strings.Join(snapshot.IndexSQL, "\n"),
		VerifySQL:          buildViewDependencyVerifySQL(qualifiedName),
		Status:             "PLANNED",
	}
}

func buildViewDependencyVerifySQL(qualifiedName string) string {
	escapedName := strings.ReplaceAll(qualifiedName, "'", "''")
	escapedMessage := strings.ReplaceAll(fmt.Sprintf("视图依赖恢复校验失败: %s 不存在", qualifiedName), "'", "''")
	return fmt.Sprintf("DO $$ BEGIN IF to_regclass('%s') IS NULL THEN RAISE EXCEPTION '%s'; END IF; END $$;", escapedName, escapedMessage)
}

func quoteRoleIdent(name string) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "public") {
		return "PUBLIC"
	}
	return quoteIdent(name)
}

func mustMarshalStringSlice(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(data)
}
