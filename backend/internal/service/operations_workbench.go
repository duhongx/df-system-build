package service

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type WorkbenchQuery struct {
	Revision       uint   `json:"revision"`
	Parameters     []any  `json:"parameters"`
	SQL            string `json:"sql"`
	Schema         string `json:"schema"`
	Mode           string `json:"mode"`
	MaxRows        int    `json:"maxRows"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
type QueryColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type WorkbenchResult struct {
	RecordID   uint            `json:"recordId"`
	DatabaseID uint            `json:"databaseId"`
	Target     string          `json:"target"`
	Schema     string          `json:"schema"`
	Status     string          `json:"status"`
	Error      string          `json:"error"`
	SQLState   string          `json:"sqlState"`
	Columns    []QueryColumn   `json:"columns"`
	Rows       [][]any         `json:"rows"`
	RowCount   int64           `json:"rowCount"`
	HasMore    bool            `json:"hasMore"`
	DurationMs float64         `json:"durationMs"`
	StartedAt  time.Time       `json:"startedAt"`
	FinishedAt time.Time       `json:"finishedAt"`
	Plan       json.RawMessage `json:"plan,omitempty"`
	Warning    string          `json:"warning"`
}

func (s *OperationsService) workbenchConfig(ctx context.Context, id uint) (model.OperationsDatabase, opsprobe.DatabaseConfig, error) {
	var row model.OperationsDatabase
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return row, opsprobe.DatabaseConfig{}, err
	}
	cfg, err := s.databaseConfig(row)
	return row, cfg, err
}

func (s *OperationsService) WorkbenchConfig(ctx context.Context, id uint) (opsprobe.DatabaseConfig, error) {
	_, cfg, err := s.workbenchConfig(ctx, id)
	return cfg, err
}

// Validate the entire AST, including data-modifying CTEs and SELECT INTO.
// The server also enforces READ ONLY for the actual execution transaction.
func validateWorkbenchSQL(text string) (string, bool, error) {
	return validateWorkbenchStatement(text, false)
}

func validateWorkbenchStatement(text string, explain bool) (string, bool, error) {
	tree, err := pg_query.Parse(text)
	if err != nil {
		return "", false, fmt.Errorf("SQL 语法错误: %w", err)
	}
	if len(tree.Stmts) != 1 {
		return "", false, fmt.Errorf("每次执行一条查询；数据和结构变更请使用 SQL 变更发布")
	}
	node := tree.Stmts[0].Stmt
	show := node.GetVariableShowStmt() != nil
	dml := node.GetInsertStmt() != nil || node.GetUpdateStmt() != nil || node.GetDeleteStmt() != nil || node.GetMergeStmt() != nil
	if node.GetSelectStmt() == nil && !show && !(explain && dml) {
		if explain {
			return "", false, fmt.Errorf("预估计划支持 SELECT／WITH、INSERT、UPDATE、DELETE 和 MERGE，不执行 ANALYZE")
		}
		return "", false, fmt.Errorf("工作台支持 SELECT／WITH 查询和 SHOW；变更请使用 SQL 变更发布")
	}
	var invalid bool
	var walk func(protoreflect.Message)
	walk = func(m protoreflect.Message) {
		if !m.IsValid() {
			return
		}
		name := string(m.Descriptor().Name())
		switch name {
		case "InsertStmt", "UpdateStmt", "DeleteStmt", "MergeStmt", "LockingClause":
			if !explain {
				invalid = true
			}
		case "IntoClause":
			invalid = true
		}
		m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			if f.IsList() && f.Kind() == protoreflect.MessageKind {
				xs := v.List()
				for i := 0; i < xs.Len(); i++ {
					walk(xs.Get(i).Message())
				}
			} else if f.Kind() == protoreflect.MessageKind {
				walk(v.Message())
			}
			return true
		})
	}
	walk(node.ProtoReflect())
	if invalid {
		return "", false, fmt.Errorf("查询包含写入、建表或行锁操作，请转到 SQL 变更发布")
	}
	normalized, err := pg_query.Deparse(tree)
	return strings.TrimSuffix(strings.TrimSpace(normalized), ";"), show, err
}

func (s *OperationsService) WorkbenchQuery(ctx context.Context, id uint, req WorkbenchQuery, operator string) (result *WorkbenchResult, exportFile string, err error) {
	row, cfg, err := s.workbenchConfig(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if req.Revision != row.Revision {
		return nil, "", fmt.Errorf("连接配置已变更，请刷新并新建 SQL 窗口")
	}
	result = &WorkbenchResult{DatabaseID: id, Target: fmt.Sprintf("%s@%s:%d/%s", row.Username, row.Host, row.Port, row.Database), Schema: req.Schema, StartedAt: time.Now(), Columns: []QueryColumn{}, Rows: [][]any{}, Status: "SUCCESS"}
	if req.Mode == "" {
		req.Mode = "query"
	}
	if req.MaxRows <= 0 {
		req.MaxRows = 1000
	}
	if req.MaxRows > 10000 {
		return nil, "", fmt.Errorf("结果预览最多 10000 行，完整结果请使用 CSV 导出")
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = 15
	}
	if req.TimeoutSeconds > 30 {
		return nil, "", fmt.Errorf("单次查询超时请选择 1～30 秒")
	}
	defer func() {
		result.FinishedAt = time.Now()
		result.DurationMs = float64(time.Since(result.StartedAt).Microseconds()) / 1000
		if err != nil {
			result.Status = "FAILED"
			result.Error = SQLExecutionErrorMessage(err)
			if e, ok := err.(*pgconn.PgError); ok {
				result.SQLState = e.Code
			}
			err = nil
		}
		record := model.DatabaseQueryRecord{DatabaseID: id, Revision: row.Revision, Target: result.Target, Schema: req.Schema, SQL: req.SQL, ParametersJSON: mustJSON(req.Parameters), Kind: req.Mode, Status: result.Status, Error: result.Error, SQLState: result.SQLState, Rows: result.RowCount, HasMore: result.HasMore, DurationMs: result.DurationMs, Operator: operator, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt}
		historyCtx, historyCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer historyCancel()
		if e := s.db.WithContext(historyCtx).Create(&record).Error; e != nil {
			result.Warning = "本次结果已返回，但执行记录保存失败"
		} else {
			result.RecordID = record.ID
		}
	}()
	if req.Mode != "query" && req.Mode != "explain" && req.Mode != "export" {
		return result, "", fmt.Errorf("无效的查询操作")
	}
	sql, show, err := validateWorkbenchStatement(req.SQL, req.Mode == "explain")
	if err != nil {
		return result, "", err
	}
	if req.Mode == "explain" && show {
		return result, "", fmt.Errorf("SHOW 命令没有查询执行计划")
	}
	queryCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()
	conn, err := opsprobe.OpenDatabase(queryCtx, cfg)
	if err != nil {
		return result, "", err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	tx, err := conn.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(queryCtx, `SELECT set_config('statement_timeout',$1,true),set_config('lock_timeout',$1,true),set_config('application_name','dfops-workbench',true)`, strconv.Itoa(req.TimeoutSeconds*1000)); err != nil {
		return result, "", err
	}
	if req.Schema != "" {
		var exists bool
		if err = tx.QueryRow(queryCtx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1 AND has_schema_privilege(oid,'USAGE'))`, req.Schema).Scan(&exists); err != nil {
			return result, "", err
		}
		if !exists {
			return result, "", fmt.Errorf("Schema 不存在或当前连接账号无访问权限")
		}
		if _, err = tx.Exec(queryCtx, `SELECT set_config('search_path',$1,true)`, pgx.Identifier{req.Schema}.Sanitize()); err != nil {
			return result, "", err
		}
	}
	if req.Mode == "explain" {
		var data []byte
		err = tx.QueryRow(queryCtx, "EXPLAIN (FORMAT JSON, VERBOSE true) "+sql, req.Parameters...).Scan(&data)
		if err == nil {
			result.Plan = json.RawMessage(data)
		}
		return result, "", err
	}
	executed := sql
	if req.Mode == "query" && !show {
		executed = fmt.Sprintf("SELECT * FROM (%s) AS df_workbench_preview LIMIT %d", sql, req.MaxRows+1)
	}
	rows, err := tx.Query(queryCtx, executed, req.Parameters...)
	if err != nil {
		return result, "", err
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	for _, field := range fields {
		name := strconv.FormatUint(uint64(field.DataTypeOID), 10)
		if t, ok := conn.TypeMap().TypeForOID(field.DataTypeOID); ok {
			name = t.Name
		}
		result.Columns = append(result.Columns, QueryColumn{Name: string(field.Name), Type: name})
	}
	var file *os.File
	var writer *csv.Writer
	if req.Mode == "export" {
		dir := filepath.Join(s.workspace, "query-exports")
		if err = os.MkdirAll(dir, 0700); err != nil {
			return result, "", err
		}
		file, err = os.CreateTemp(dir, "query-*.csv")
		if err != nil {
			return result, "", err
		}
		exportFile = file.Name()
		writer = csv.NewWriter(file)
		header := make([]string, len(fields))
		for i, f := range fields {
			header[i] = string(f.Name)
		}
		if err = writer.Write(header); err != nil {
			file.Close()
			os.Remove(exportFile)
			return result, "", err
		}
		defer func() {
			writer.Flush()
			if e := writer.Error(); err == nil {
				err = e
			}
			if e := file.Close(); err == nil {
				err = e
			}
			if err != nil {
				os.Remove(exportFile)
				exportFile = ""
			}
		}()
	}
	for rows.Next() {
		values, e := rows.Values()
		if e != nil {
			return result, exportFile, e
		}
		if req.Mode == "query" && result.RowCount >= int64(req.MaxRows) {
			result.HasMore = true
			break
		}
		line := make([]any, len(values))
		for i, v := range values {
			line[i] = opsprobe.CellText(conn, fields[i].DataTypeOID, v)
		}
		if writer != nil {
			record := make([]string, len(line))
			for i, v := range line {
				if v == nil {
					record[i] = "\\N"
				} else {
					record[i] = fmt.Sprint(v)
				}
			}
			if e := writer.Write(record); e != nil {
				return result, exportFile, e
			}
		} else {
			result.Rows = append(result.Rows, line)
		}
		result.RowCount++
	}
	err = rows.Err()
	return result, exportFile, err
}
