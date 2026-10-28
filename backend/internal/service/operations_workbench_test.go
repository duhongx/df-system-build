package service

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/crypto"
	"github.com/jackc/pgx/v5"
)

func TestWorkbenchSQLValidation(t *testing.T) {
	for _, sql := range []string{"select 1", "with x as (select 1) select * from x", "show search_path", "select ';' as data", "select $1::int"} {
		if _, _, err := validateWorkbenchSQL(sql); err != nil {
			t.Errorf("valid %q: %v", sql, err)
		}
	}
	for _, sql := range []string{"select 1; select 2", "delete from x", "with x as (delete from x returning *) select * from x", "select * into x from y", "select * from x for update", "explain analyze select 1", "begin", "create table x(id int)", ""} {
		if _, _, err := validateWorkbenchSQL(sql); err == nil {
			t.Errorf("unsafe accepted: %q", sql)
		}
	}
}

func workbenchFixture(t *testing.T) (*OperationsService, model.OperationsDatabase, opsprobe.DatabaseConfig, *pgx.Conn) {
	t.Helper()
	db := operationsTestDB(t)
	dsn, _ := testutil.PostgresDSNFromEnv()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	secret, err := crypto.Encrypt(cfg.Password)
	if err != nil {
		t.Fatal(err)
	}
	row := model.OperationsDatabase{Name: t.Name(), Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, PasswordEncrypted: secret, SSLMode: "disable", Revision: 1}
	if err = db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return NewOperationsService(db, t.TempDir()), row, opsprobe.DatabaseConfig{Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, Password: cfg.Password, SSLMode: "disable"}, conn
}

func TestWorkbenchQueryPreviewExportAndHistory(t *testing.T) {
	svc, row, _, conn := workbenchFixture(t)
	ctx := context.Background()
	schema := fmt.Sprintf("workbench_%d", row.ID)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoted+`;CREATE TABLE `+quoted+`."odd table"("a""b" int primary key, value text);INSERT INTO `+quoted+`."odd table" SELECT i,CASE WHEN i=1 THEN NULL ELSE '值,'||i END FROM generate_series(1,1307) i`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	req := WorkbenchQuery{Revision: 1, SQL: `SELECT "a""b",value FROM "odd table" WHERE "a""b">=$1 ORDER BY 1`, Schema: schema, Parameters: []any{1}, MaxRows: 1000}
	result, _, err := svc.WorkbenchQuery(ctx, row.ID, req, "tester")
	if err != nil || result.Status != "SUCCESS" || result.RowCount != 1000 || !result.HasMore || result.Rows[0][1] != nil {
		t.Fatalf("preview %+v %v", result, err)
	}
	if result.Rows[0][0] != "1" {
		t.Fatalf("incorrect cell format %v", result.Rows[0])
	}
	var history model.DatabaseQueryRecord
	if err := svc.db.First(&history, result.RecordID).Error; err != nil || history.Revision != 1 || history.Target != result.Target || history.ParametersJSON != "[1]" || !history.HasMore {
		t.Fatalf("history %+v %v", history, err)
	}
	req.Mode = "export"
	result, file, err := svc.WorkbenchQuery(ctx, row.ID, req, "tester")
	if err != nil || result.Status != "SUCCESS" || file == "" {
		t.Fatalf("export %+v %v", result, err)
	}
	defer os.Remove(file)
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil || len(records) != 1308 || records[1][1] != `\N` || records[2][1] != "值,2" {
		t.Fatalf("CSV len %d %v", len(records), err)
	}
	req.Mode = "explain"
	result, _, err = svc.WorkbenchQuery(ctx, row.ID, req, "tester")
	if err != nil || result.Status != "SUCCESS" || !strings.Contains(string(result.Plan), "Plan Rows") {
		t.Fatalf("plan %+v %v", result, err)
	}
	req.SQL = "UPDATE " + quoted + `."odd table" SET value='must not execute' WHERE "a""b"=1`
	req.Parameters = nil
	result, _, err = svc.WorkbenchQuery(ctx, row.ID, req, "tester")
	if err != nil || result.Status != "SUCCESS" || !strings.Contains(string(result.Plan), "ModifyTable") {
		t.Fatalf("DML estimated plan %+v %v", result, err)
	}
	var unchanged bool
	if err = conn.QueryRow(ctx, "SELECT value IS NULL FROM "+quoted+`."odd table" WHERE "a""b"=1`).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("EXPLAIN executed DML: %v", err)
	}
	req.Mode = "query"
	req.SQL = "with x as (delete from " + quoted + `."odd table" returning *) select * from x`
	result, _, err = svc.WorkbenchQuery(ctx, row.ID, req, "tester")
	if err != nil || result.Status != "FAILED" {
		t.Fatalf("write accepted %+v %v", result, err)
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+quoted+`."odd table"`).Scan(&count); err != nil || count != 1307 {
		t.Fatalf("write affected data %d %v", count, err)
	}
	req.SQL = "SELECT i, pg_sleep(0.005) FROM generate_series(1,1000) i"
	req.Parameters = nil
	started := time.Now()
	req.Mode = "export"
	req.TimeoutSeconds = 1
	result, file, err = svc.WorkbenchQuery(ctx, row.ID, req, "tester")
	if err != nil || result.Status != "FAILED" || file != "" || time.Since(started) < 900*time.Millisecond {
		t.Fatalf("partial export accepted %+v %s %v", result, file, err)
	}
	req.Revision = 2
	if _, _, err = svc.WorkbenchQuery(ctx, row.ID, req, "tester"); err == nil {
		t.Fatal("changed connection accepted")
	}
}

func TestWorkbenchMetadataAndStatements(t *testing.T) {
	_, _, cfg, conn := workbenchFixture(t)
	ctx := context.Background()
	schema := fmt.Sprintf("metadata_%d", conn.PgConn().PID())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+q+`;CREATE TABLE `+q+`."order"("a""b" int primary key, label text default 'x');CREATE VIEW `+q+`.v AS SELECT * FROM `+q+`."order"`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP SCHEMA "+q+" CASCADE")
	schemas, err := opsprobe.Schemas(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range schemas {
		if s.Name == schema && s.Objects == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("schema missing %v", schemas)
	}
	relations, err := opsprobe.Relations(ctx, cfg, schema)
	if err != nil || len(relations) != 2 {
		t.Fatalf("relations %v %v", relations, err)
	}
	for _, relation := range relations {
		detail, err := opsprobe.DescribeRelation(ctx, cfg, relation.OID)
		if err != nil || len(detail.Columns) != 2 || detail.Columns[0].Name != `a"b` {
			t.Fatalf("detail %+v %v", detail, err)
		}
		if relation.Kind == "v" && detail.Definition == "" {
			t.Fatal("missing view SQL")
		}
		if relation.Kind == "r" && len(detail.Indexes) != 1 {
			t.Fatal("missing primary key")
		}
	}
	report, err := opsprobe.StatementStatistics(ctx, cfg, "total")
	if err != nil {
		t.Fatal(err)
	}
	// A missing extension is a capability result, not an empty successful ranking.
	if !report.Available && report.Reason == "" {
		t.Fatal("missing unavailable reason")
	}
}

func TestSlowAnalysisGroupsLiteralsAndKeepsPhases(t *testing.T) {
	svc, row, _, _ := workbenchFixture(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 8; i++ {
		e := model.DatabaseLogEvent{DatabaseID: row.ID, SourceKey: fmt.Sprintf("wbslow-%d-%d", row.ID, i), Type: "slow_sql", EventTime: now.Add(-time.Duration(i) * time.Minute), DurationMs: float64(100 + i), Phase: "execute", DBName: "business", Username: "app", Client: "10.0.0.1", SQL: fmt.Sprintf("select * from orders where id=%d", i+1)}
		if err := svc.db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc.db.Create(&model.DatabaseLogEvent{DatabaseID: row.ID, SourceKey: fmt.Sprintf("wbslow-%d-parse", row.ID), Type: "slow_sql", EventTime: now, DurationMs: 250, Phase: "parse", DBName: "business", SQL: "select * from orders where id=99"})
	svc.db.Create(&model.DatabaseLogEvent{DatabaseID: row.ID, SourceKey: fmt.Sprintf("wbslow-%d-old", row.ID), Type: "slow_sql", EventTime: now.Add(-48 * time.Hour), DurationMs: 9999, Phase: "execute", DBName: "business", SQL: "select 1"})
	filter := OperationsEventFilter{From: now.Add(-time.Hour).Format(time.RFC3339), To: now.Add(time.Second).Format(time.RFC3339)}
	report, err := svc.AnalyzeSlowLogs(ctx, row.ID, filter, "")
	if err != nil || report.LogCount != 9 || report.TemplateCount != 2 {
		t.Fatalf("report %+v %v", report, err)
	}
	var group *SlowTemplate
	for i := range report.Templates {
		if report.Templates[i].Phase == "execute" {
			group = &report.Templates[i]
		}
	}
	if group == nil || group.LogCount != 8 || len(group.Samples) != 5 || !strings.Contains(group.Template, "$1") || !group.Normalized || group.AverageMs != 103.5 {
		t.Fatalf("group %+v", group)
	}
	for _, sample := range group.Samples {
		if sample.ID == 0 || sample.Phase != "execute" || sample.DBName != "business" {
			t.Fatalf("fabricated sample %+v", sample)
		}
	}
	filtered, err := svc.AnalyzeSlowLogs(ctx, row.ID, filter, "parse")
	if err != nil || filtered.LogCount != 1 || filtered.TotalMs != 250 {
		t.Fatalf("phase filter %+v %v", filtered, err)
	}
	filter.MinDuration = 105
	filtered, err = svc.AnalyzeSlowLogs(ctx, row.ID, filter, "execute")
	if err != nil || filtered.LogCount != 3 {
		t.Fatalf("threshold filter %+v %v", filtered, err)
	}
}
