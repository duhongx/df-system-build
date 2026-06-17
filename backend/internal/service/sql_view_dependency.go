package service

import (
	"crypto/sha256"
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
	ExecutionMode    string `json:"executionMode"`
	LockTimeout      string `json:"lockTimeout"`
	StatementTimeout string `json:"statementTimeout"`
}

const (
	SQLViewDependencyExecutionModeStep        = "STEP"
	SQLViewDependencyExecutionModeTransaction = "TRANSACTION"
)

func normalizeSQLViewDependencyExecutionMode(mode string) (string, error) {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	if mode == "" {
		return SQLViewDependencyExecutionModeStep, nil
	}
	switch mode {
	case SQLViewDependencyExecutionModeStep, SQLViewDependencyExecutionModeTransaction:
		return mode, nil
	default:
		return "", fmt.Errorf("不支持的执行方式: %s", mode)
	}
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
	RuleSQL       []string
	TriggerSQL    []string
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
	ref, err := parseSingleAlterColumnTypeRef(req.AlterSQL)
	if err != nil {
		return alterColumnRef{}, err
	}
	if ref.schema == "" {
		return alterColumnRef{}, fmt.Errorf("ALTER SQL 必须显式指定 schema.table")
	}
	if !strings.EqualFold(ref.schema, req.SchemaName) || !strings.EqualFold(ref.table, req.TableName) || !strings.EqualFold(ref.column, req.ColumnName) {
		return alterColumnRef{}, fmt.Errorf("ALTER SQL 目标字段与入参不一致")
	}
	return ref, nil
}

func parseSingleAlterColumnTypeRef(sqlText string) (alterColumnRef, error) {
	tree, err := pg_query.Parse(sqlText)
	if err != nil {
		return alterColumnRef{}, fmt.Errorf("ALTER SQL 解析失败: %w", err)
	}
	if len(tree.GetStmts()) != 1 {
		return alterColumnRef{}, fmt.Errorf("只允许一条 ALTER TABLE 语句")
	}
	stmt := tree.GetStmts()[0].GetStmt()
	alterStmt := stmt.GetAlterTableStmt()
	if alterStmt == nil || alterStmt.GetRelation() == nil {
		return alterColumnRef{}, fmt.Errorf("只支持 ALTER TABLE 修改字段类型语句")
	}
	cmds := alterStmt.GetCmds()
	if len(cmds) != 1 {
		return alterColumnRef{}, fmt.Errorf("视图依赖变更只允许单个字段类型变更，不允许在同一 ALTER TABLE 中包含其他操作")
	}
	cmd := cmds[0].GetAlterTableCmd()
	if cmd == nil || cmd.GetSubtype() != pg_query.AlterTableType_AT_AlterColumnType {
		return alterColumnRef{}, fmt.Errorf("只支持 ALTER TABLE 修改字段类型语句")
	}
	relation := alterStmt.GetRelation()
	return alterColumnRef{
		schema: strings.TrimSpace(relation.GetSchemaname()),
		table:  strings.TrimSpace(relation.GetRelname()),
		column: strings.TrimSpace(cmd.GetName()),
	}, nil
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
		writeSQLLine(&b, item.RestoreRefreshSQL)
		writeSQLLine(&b, item.RestoreRulesSQL)
		writeSQLLine(&b, item.RestoreTriggersSQL)
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
		writeSQLLine(&b, item.RestoreRefreshSQL)
		writeSQLLine(&b, item.RestoreRulesSQL)
		writeSQLLine(&b, item.RestoreTriggersSQL)
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

type SQLViewDependencyStepExecutionPlan struct {
	DropSteps    []string
	AlterSteps   []string
	RestoreSteps []string
}

func (p SQLViewDependencyStepExecutionPlan) AllSteps() []string {
	steps := make([]string, 0, len(p.DropSteps)+len(p.AlterSteps)+len(p.RestoreSteps))
	steps = append(steps, p.DropSteps...)
	steps = append(steps, p.AlterSteps...)
	steps = append(steps, p.RestoreSteps...)
	return steps
}

func BuildSQLViewDependencyStepExecutionPlan(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) SQLViewDependencyStepExecutionPlan {
	plan := SQLViewDependencyStepExecutionPlan{}
	for _, item := range orderedViewDependencyDropItems(items) {
		appendSQLStep(&plan.DropSteps, item.DropSQL)
	}
	appendSQLStep(&plan.AlterSteps, task.AlterSQL)
	appendSQLViewDependencyRestoreSteps(&plan.RestoreSteps, items)
	return plan
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
		appendSQLStep(steps, item.RestoreRefreshSQL)
		appendSQLStep(steps, item.RestoreRulesSQL)
		appendSQLStep(steps, item.RestoreTriggersSQL)
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

type SQLViewDependencyStepExecutionError struct {
	Stage            string
	Err              error
	RestoreAttempted bool
	RestoreSucceeded bool
	RestoreErr       error
}

func (e SQLViewDependencyStepExecutionError) Error() string {
	if e.RestoreAttempted {
		if e.RestoreSucceeded {
			return fmt.Sprintf("%s失败，已按备份恢复视图: %v", e.Stage, e.Err)
		}
		return fmt.Sprintf("%s失败，且按备份恢复视图失败: %v; restore: %v", e.Stage, e.Err, e.RestoreErr)
	}
	return fmt.Sprintf("%s失败: %v", e.Stage, e.Err)
}

func (e SQLViewDependencyStepExecutionError) Unwrap() error {
	return e.Err
}

func runSQLViewDependencyStepExecution(executor sqlViewDependencyExecutor, plan SQLViewDependencyStepExecutionPlan) error {
	for _, step := range plan.DropSteps {
		if err := executor.Exec(step); err != nil {
			return SQLViewDependencyStepExecutionError{Stage: "删除依赖视图", Err: err}
		}
	}
	for _, step := range plan.AlterSteps {
		if err := executor.Exec(step); err != nil {
			restoreErr := runSQLViewDependencySteps(executor, plan.RestoreSteps)
			return SQLViewDependencyStepExecutionError{
				Stage:            "字段变更",
				Err:              err,
				RestoreAttempted: true,
				RestoreSucceeded: restoreErr == nil,
				RestoreErr:       restoreErr,
			}
		}
	}
	for _, step := range plan.RestoreSteps {
		if err := executor.Exec(step); err != nil {
			return SQLViewDependencyStepExecutionError{Stage: "恢复依赖视图", Err: err, RestoreAttempted: true, RestoreSucceeded: false, RestoreErr: err}
		}
	}
	return nil
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
	rulesJSON := mustMarshalStringSlice(snapshot.RuleSQL)
	triggersJSON := mustMarshalStringSlice(snapshot.TriggerSQL)
	optionsJSON := strings.TrimSpace(snapshot.OptionsJSON)
	if optionsJSON == "" && len(snapshot.AdditionalSQL) > 0 {
		optionsJSON = mustMarshalStringSlice(snapshot.AdditionalSQL)
	}
	restoreRefreshSQL := ""
	if kind == "m" {
		restoreRefreshSQL = fmt.Sprintf("REFRESH MATERIALIZED VIEW %s;", qualifiedName)
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
		RulesJSON:          rulesJSON,
		TriggersJSON:       triggersJSON,
		OptionsJSON:        optionsJSON,
		BackupHash:         viewDependencySnapshotHash(snapshot),
		DropSQL:            plan.DropSQL,
		CreateSQL:          strings.TrimSpace(plan.CreateSQL + "\n" + strings.Join(snapshot.AdditionalSQL, "\n")),
		RestoreRefreshSQL:  restoreRefreshSQL,
		RestoreOwnerSQL:    restoreOwnerSQL,
		RestoreGrantsSQL:   strings.Join(snapshot.GrantSQL, "\n"),
		RestoreCommentsSQL: strings.Join(snapshot.CommentSQL, "\n"),
		RestoreIndexesSQL:  strings.Join(snapshot.IndexSQL, "\n"),
		RestoreRulesSQL:    strings.Join(snapshot.RuleSQL, "\n"),
		RestoreTriggersSQL: strings.Join(snapshot.TriggerSQL, "\n"),
		VerifySQL:          buildViewDependencyVerifySQL(qualifiedName),
		Status:             "PLANNED",
	}
}

func viewDependencySnapshotHash(snapshot viewDependencySnapshot) string {
	fingerprint := struct {
		Schema        string   `json:"schema"`
		Name          string   `json:"name"`
		Kind          string   `json:"kind"`
		Definition    string   `json:"definition"`
		Owner         string   `json:"owner"`
		GrantSQL      []string `json:"grantSql"`
		CommentSQL    []string `json:"commentSql"`
		IndexSQL      []string `json:"indexSql"`
		RuleSQL       []string `json:"ruleSql"`
		TriggerSQL    []string `json:"triggerSql"`
		OptionsJSON   string   `json:"optionsJson"`
		AdditionalSQL []string `json:"additionalSql"`
	}{
		Schema:        strings.TrimSpace(snapshot.Schema),
		Name:          strings.TrimSpace(snapshot.Name),
		Kind:          strings.TrimSpace(snapshot.Kind),
		Definition:    normalizeSQL(snapshot.Definition),
		Owner:         strings.TrimSpace(snapshot.Owner),
		GrantSQL:      sortedStrings(snapshot.GrantSQL),
		CommentSQL:    sortedStrings(snapshot.CommentSQL),
		IndexSQL:      sortedStrings(snapshot.IndexSQL),
		RuleSQL:       sortedStrings(snapshot.RuleSQL),
		TriggerSQL:    sortedStrings(snapshot.TriggerSQL),
		OptionsJSON:   strings.TrimSpace(snapshot.OptionsJSON),
		AdditionalSQL: sortedStrings(snapshot.AdditionalSQL),
	}
	data, _ := json.Marshal(fingerprint)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func sortedStrings(items []string) []string {
	copied := append([]string(nil), items...)
	sort.Strings(copied)
	return copied
}

func validateViewDependencyBackupFresh(items []model.SQLViewDependencyItem, current []viewDependencySnapshot) error {
	currentByName := make(map[string]viewDependencySnapshot, len(current))
	for _, snapshot := range current {
		currentByName[viewDependencyObjectKey(snapshot.Schema, snapshot.Name, snapshot.Kind)] = snapshot
	}
	for _, item := range items {
		if strings.TrimSpace(item.BackupHash) == "" {
			return fmt.Errorf("视图 %s.%s 缺少备份指纹，请重新分析依赖", item.ObjectSchema, item.ObjectName)
		}
		snapshot, ok := currentByName[viewDependencyObjectKey(item.ObjectSchema, item.ObjectName, item.ObjectKind)]
		if !ok {
			return fmt.Errorf("视图 %s.%s 当前不存在或不再依赖目标字段，请重新分析依赖", item.ObjectSchema, item.ObjectName)
		}
		if item.BackupHash != viewDependencySnapshotHash(snapshot) {
			return fmt.Errorf("视图 %s.%s 自上次分析后已变化，请重新分析依赖", item.ObjectSchema, item.ObjectName)
		}
	}
	return nil
}

func viewDependencyObjectKey(schemaName, objectName, kind string) string {
	return strings.ToLower(strings.TrimSpace(schemaName)) + "." + strings.ToLower(strings.TrimSpace(objectName)) + ":" + strings.ToLower(strings.TrimSpace(kind))
}

func ensureSQLViewDependencyTaskHasPlan(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) error {
	if task.Status == "CREATED" || len(items) == 0 {
		return fmt.Errorf("请先分析依赖并生成备份计划")
	}
	return nil
}

func ensureSQLViewDependencyTaskCanExecute(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) error {
	if err := ensureSQLViewDependencyTaskHasPlan(task, items); err != nil {
		return err
	}
	if task.Status != "PRECHECK_PASSED" {
		return fmt.Errorf("请先执行预检，预检通过后再执行变更")
	}
	return nil
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
