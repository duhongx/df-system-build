package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	SchemaName       string `json:"schemaName"`
	TableName        string `json:"tableName"`
	ColumnName       string `json:"columnName"`
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
	refs, err := validateViewDependencyAlterSQLs(req)
	if err != nil {
		return alterColumnRef{}, err
	}
	if len(refs) == 0 {
		return alterColumnRef{}, fmt.Errorf("ALTER SQL 不能为空")
	}
	return refs[0], nil
}

func validateViewDependencyAlterSQLs(req SQLViewDependencyTaskRequest) ([]alterColumnRef, error) {
	req.SchemaName = strings.TrimSpace(req.SchemaName)
	req.TableName = strings.TrimSpace(req.TableName)
	req.ColumnName = strings.TrimSpace(req.ColumnName)
	req.AlterSQL = strings.TrimSpace(req.AlterSQL)
	if req.AlterSQL == "" {
		return nil, fmt.Errorf("ALTER SQL 不能为空")
	}
	if err := validateSchemaName(req.SchemaName); err != nil {
		return nil, err
	}
	refs, err := parseAlterColumnTypeRefs(req.AlterSQL)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.schema == "" {
			return nil, fmt.Errorf("ALTER SQL 必须显式指定 schema.table")
		}
		if err := validateSchemaName(ref.schema); err != nil {
			return nil, err
		}
		if req.SchemaName != "" && !strings.EqualFold(ref.schema, req.SchemaName) {
			return nil, fmt.Errorf("ALTER SQL 目标 schema 与入参不一致")
		}
		if req.TableName != "" && !strings.EqualFold(ref.table, req.TableName) {
			return nil, fmt.Errorf("ALTER SQL 目标表与入参不一致")
		}
		if req.ColumnName != "" && !strings.EqualFold(ref.column, req.ColumnName) {
			return nil, fmt.Errorf("ALTER SQL 目标字段与入参不一致")
		}
	}
	return refs, nil
}

func parseSingleAlterColumnTypeRef(sqlText string) (alterColumnRef, error) {
	refs, err := parseAlterColumnTypeRefs(sqlText)
	if err != nil {
		return alterColumnRef{}, err
	}
	if len(refs) != 1 {
		return alterColumnRef{}, fmt.Errorf("只允许一条 ALTER TABLE 语句")
	}
	return refs[0], nil
}

func parseAlterColumnTypeRefs(sqlText string) ([]alterColumnRef, error) {
	return parseViewAlterColumnRefs(sqlText, false)
}

// Compound statements are produced internally after validation; uploads remain one column per statement.
func parseViewAlterColumnRefs(sqlText string, compound bool) ([]alterColumnRef, error) {
	tree, err := pg_query.Parse(sqlText)
	if err != nil {
		return nil, fmt.Errorf("ALTER SQL 解析失败: %w", err)
	}
	rawStmts := tree.GetStmts()
	if len(rawStmts) == 0 {
		return nil, fmt.Errorf("ALTER SQL 不能为空")
	}
	refs := make([]alterColumnRef, 0, len(rawStmts))
	for idx, raw := range rawStmts {
		stmt := raw.GetStmt()
		alterStmt := stmt.GetAlterTableStmt()
		if alterStmt == nil || alterStmt.GetRelation() == nil {
			return nil, fmt.Errorf("第 %d 条 SQL 只支持 ALTER TABLE 修改字段类型语句", idx+1)
		}
		cmds := alterStmt.GetCmds()
		if len(cmds) == 0 || (!compound && len(cmds) != 1) {
			return nil, fmt.Errorf("第 %d 条 SQL 只允许单个字段类型变更，不允许在同一 ALTER TABLE 中包含其他操作", idx+1)
		}
		for _, node := range cmds {
			cmd := node.GetAlterTableCmd()
			if cmd == nil || cmd.GetSubtype() != pg_query.AlterTableType_AT_AlterColumnType {
				return nil, fmt.Errorf("第 %d 条 SQL 只支持 ALTER TABLE 修改字段类型语句", idx+1)
			}
			relation := alterStmt.GetRelation()
			refs = append(refs, alterColumnRef{
				schema: strings.TrimSpace(relation.GetSchemaname()),
				table:  strings.TrimSpace(relation.GetRelname()),
				column: strings.TrimSpace(cmd.GetName()),
				sql:    extractRawStatementSQL(sqlText, raw),
				line:   lineNumberAtOffset(sqlText, int(raw.GetStmtLocation())),
				order:  len(refs) + 1,
			})
		}
	}
	return refs, nil
}

func extractRawStatementSQL(sqlText string, raw *pg_query.RawStmt) string {
	start := int(raw.GetStmtLocation())
	if start < 0 || start > len(sqlText) {
		start = 0
	}
	end := len(sqlText)
	if stmtLen := int(raw.GetStmtLen()); stmtLen > 0 && start+stmtLen <= len(sqlText) {
		end = start + stmtLen
	}
	statement := strings.TrimSpace(sqlText[start:end])
	if statement == "" {
		return ""
	}
	if !strings.HasSuffix(statement, ";") {
		statement += ";"
	}
	return statement
}

func lineNumberAtOffset(sqlText string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(sqlText) {
		offset = len(sqlText)
	}
	return strings.Count(sqlText[:offset], "\n") + 1
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
	b.WriteString(fmt.Sprintf("-- Target summary: %s.%s.%s\n\n", task.SchemaName, task.TableName, task.ColumnName))
	b.WriteString("-- Drop dependent views\n")
	for _, item := range dropItems {
		writeSQLLine(&b, item.DropSQL)
	}
	b.WriteString("\n-- ALTER SQL\n")
	for _, alterSQL := range alterSQLStatementsForTask(task) {
		writeSQLLine(&b, alterSQL)
	}
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
	b.WriteString(fmt.Sprintf("-- Target summary: %s.%s.%s\n\n", task.SchemaName, task.TableName, task.ColumnName))
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
	for _, alterSQL := range alterSQLStatementsForTask(task) {
		appendSQLStep(&steps, alterSQL)
	}
	appendSQLViewDependencyRestoreSteps(&steps, items)
	steps = append(steps, "COMMIT;")
	return steps
}

type SQLViewDependencyRestoreGroup struct{ DropIndex, Start, End int }
type SQLViewDependencyStepExecutionPlan struct {
	LockTimeout, StatementTimeout string
	sourceTask                    model.SQLViewDependencyTask
	RestoreGroups                 []SQLViewDependencyRestoreGroup
	DropSteps                     []string
	AlterSteps                    []string
	RestoreSteps                  []string
}

func (p SQLViewDependencyStepExecutionPlan) AllSteps() []string {
	steps := make([]string, 0, len(p.DropSteps)+len(p.AlterSteps)+len(p.RestoreSteps))
	steps = append(steps, p.DropSteps...)
	steps = append(steps, p.AlterSteps...)
	steps = append(steps, p.RestoreSteps...)
	return steps
}

func BuildSQLViewDependencyStepExecutionPlan(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) SQLViewDependencyStepExecutionPlan {
	plan := SQLViewDependencyStepExecutionPlan{LockTimeout: task.LockTimeout, StatementTimeout: task.StatementTimeout, sourceTask: task}
	dropIndices := map[string]int{}
	for _, item := range orderedViewDependencyDropItems(items) {
		if strings.TrimSpace(item.DropSQL) == "" {
			continue
		}
		dropIndices[item.DropSQL] = len(plan.DropSteps)
		appendSQLStep(&plan.DropSteps, item.DropSQL)
	}
	for _, alterSQL := range alterSQLStatementsForTask(task) {
		appendSQLStep(&plan.AlterSteps, alterSQL)
	}
	for _, item := range orderedViewDependencyRestoreItems(items) {
		start := len(plan.RestoreSteps)
		plan.RestoreSteps = append(plan.RestoreSteps, BuildSQLViewDependencyItemRestoreSteps(item)...)
		if idx, ok := dropIndices[item.DropSQL]; ok {
			plan.RestoreGroups = append(plan.RestoreGroups, SQLViewDependencyRestoreGroup{DropIndex: idx, Start: start, End: len(plan.RestoreSteps)})
		}
	}
	return plan
}

func alterSQLStatementsForTask(task model.SQLViewDependencyTask) []string {
	refs, err := parseAlterColumnTypeRefs(task.AlterSQL)
	if err != nil || len(refs) == 0 {
		sqlText := strings.TrimSpace(task.AlterSQL)
		if sqlText == "" {
			return nil
		}
		return []string{sqlText}
	}
	statements := make([]string, 0, len(refs))
	for _, ref := range refs {
		statements = append(statements, ref.sql)
	}
	return mergeViewAlterStatements(statements)
}

func BuildSQLViewDependencyRestoreTransactionalSteps(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) []string {
	steps := buildSQLViewDependencyTransactionPrefix(task)
	appendSQLViewDependencyRestoreSteps(&steps, items)
	steps = append(steps, "COMMIT;")
	return steps
}

func BuildSQLViewDependencyItemRestoreSteps(item model.SQLViewDependencyItem) []string {
	steps := make([]string, 0, 9)
	appendSQLStep(&steps, item.CreateSQL)
	appendSQLStep(&steps, item.RestoreRefreshSQL)
	appendSQLStep(&steps, item.RestoreRulesSQL)
	appendSQLStep(&steps, item.RestoreTriggersSQL)
	appendSQLStep(&steps, item.RestoreOwnerSQL)
	appendSQLStep(&steps, item.RestoreGrantsSQL)
	appendSQLStep(&steps, item.RestoreCommentsSQL)
	appendSQLStep(&steps, item.RestoreIndexesSQL)
	appendSQLStep(&steps, item.VerifySQL)
	return steps
}

func BuildSQLViewDependencyItemVerifySteps(item model.SQLViewDependencyItem) []string {
	steps := make([]string, 0, 1)
	appendSQLStep(&steps, item.VerifySQL)
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
	return errors.Join(e.Err, e.RestoreErr)
}

func partialViewRestoreIndices(plan SQLViewDependencyStepExecutionPlan, dropped int) ([]int, error) {
	if dropped == 0 {
		return nil, nil
	}
	if len(plan.RestoreGroups) == 0 {
		return nil, fmt.Errorf("缺少已删除对象与恢复步骤的对应关系，请使用备份恢复")
	}
	var indices []int
	for _, group := range plan.RestoreGroups {
		if group.DropIndex < dropped {
			for i := group.Start; i < group.End; i++ {
				indices = append(indices, i)
			}
		}
	}
	return indices, nil
}
func runSQLViewDependencyStepExecution(executor sqlViewDependencyExecutor, plan SQLViewDependencyStepExecutionPlan) error {
	return runViewStepPlan(plan, func(index int, stage, text string, recovering bool) error { return executor.Exec(text) }, func(index int) error { return nil })
}

// Fixed plan indices survive early failures; skipped ALTERs never receive restore results.
func runViewStepPlan(plan SQLViewDependencyStepExecutionPlan, run func(int, string, string, bool) error, skip func(int) error) error {
	dropEnd, alterEnd := len(plan.DropSteps), len(plan.DropSteps)+len(plan.AlterSteps)
	runRestore := func(indices []int, recovering bool) error {
		var result error
		blockedUntil := -1
		for _, idx := range indices {
			if idx < blockedUntil {
				result = errors.Join(result, skip(alterEnd+idx))
				continue
			}
			err := run(alterEnd+idx, "恢复依赖视图", plan.RestoreSteps[idx], recovering)
			result = errors.Join(result, err)
			var conflict sqlViewRecoveryConflictError
			if errors.As(err, &conflict) {
				for _, group := range plan.RestoreGroups {
					if idx == group.Start {
						blockedUntil = group.End
						break
					}
				}
			}
		}
		return result
	}
	restore := func(cause error, stage string, indices []int, mappingErr error) error {
		restoreErr := errors.Join(mappingErr, runRestore(indices, true))
		for idx := range plan.RestoreSteps {
			selected := false
			for _, used := range indices {
				if idx == used {
					selected = true
					break
				}
			}
			if !selected {
				restoreErr = errors.Join(restoreErr, skip(alterEnd+idx))
			}
		}
		return SQLViewDependencyStepExecutionError{Stage: stage, Err: cause, RestoreAttempted: len(indices) > 0 || mappingErr != nil, RestoreSucceeded: restoreErr == nil, RestoreErr: restoreErr}
	}
	for idx, step := range plan.DropSteps {
		if err := run(idx, "删除依赖视图", step, false); err != nil {
			for pending := idx + 1; pending < alterEnd; pending++ {
				err = errors.Join(err, skip(pending))
			}
			dropped := idx
			var committed sqlViewStepCommittedError
			if errors.As(err, &committed) {
				dropped++
			}
			indices, mappingErr := partialViewRestoreIndices(plan, dropped)
			return restore(err, "删除依赖视图", indices, mappingErr)
		}
	}
	for idx, step := range plan.AlterSteps {
		if err := run(dropEnd+idx, "字段变更", step, false); err != nil {
			for pending := dropEnd + idx + 1; pending < alterEnd; pending++ {
				err = errors.Join(err, skip(pending))
			}
			indices := make([]int, len(plan.RestoreSteps))
			for i := range indices {
				indices[i] = i
			}
			return restore(err, "字段变更", indices, nil)
		}
	}
	indices := make([]int, len(plan.RestoreSteps))
	for i := range indices {
		indices[i] = i
	}
	restoreErr := runRestore(indices, false)
	if restoreErr != nil {
		return SQLViewDependencyStepExecutionError{Stage: "恢复依赖视图", Err: restoreErr, RestoreAttempted: true, RestoreSucceeded: false, RestoreErr: restoreErr}
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
	if task.Status == "CREATED" || (task.AnalyzedAt == nil && len(items) == 0) {
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
