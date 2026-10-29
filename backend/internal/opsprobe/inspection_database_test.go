package opsprobe

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"github.com/jackc/pgx/v5"
)

func TestInspectionSQLPreservesAllRowsAndLongValues(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	// Supply a large result for the unchanged guide SQL using a temporary
	// relation. This fixture affects only this connection in the local test DB.
	if _, err := conn.Exec(ctx, `CREATE TEMP VIEW pg_file_settings AS
		SELECT i AS sourceline, 'inspection_parameter_' || i AS name,
		CASE WHEN i=1 THEN repeat('x', 40000) || '-long-value-end'
		ELSE 'row-' || i || '-end' END AS setting
		FROM generate_series(1, 205) AS i`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SET search_path=pg_temp,pg_catalog"); err != nil {
		t.Fatal(err)
	}
	executor := manualSQLExecutor{conn: conn}
	record, table := executor.Execute(ctx, "cloudhis-21")
	if record.Error != "" || record.ExitCode != 0 || record.Truncated {
		t.Fatalf("query failed or was truncated: %+v", record)
	}
	if len(table.Rows) != 205 || table.Rows[0][3] != strings.Repeat("x", 40000)+"-long-value-end" || table.Rows[204][3] != "row-205-end" {
		t.Fatal("SQL result rows or long value were lost")
	}
	for _, marker := range []string{strings.Repeat("x", 40000) + "-long-value-end", "row-205-end", "(205 rows)"} {
		if !strings.Contains(record.Stdout, marker) {
			t.Fatal("SQL output did not preserve the full result")
		}
	}
}

func TestInspectionDatabaseReadonlyEvidenceAndPermissionIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parsed, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg := DatabaseConfig{Host: parsed.Host, Port: int(parsed.Port), Database: parsed.Database, Username: parsed.User, Password: parsed.Password, SSLMode: "disable"}
	checks := InspectionDatabase(ctx, cfg)
	byCode := func(checks []inspection.Observation, code string) inspection.Observation {
		for _, c := range checks {
			if c.Code == code {
				return c
			}
		}
		t.Fatalf("missing %s", code)
		return inspection.Observation{}
	}
	if len(checks) != 16 {
		t.Fatalf("expected 16 original SQL results, got %d", len(checks))
	}
	for _, code := range []string{"cloudhis-20", "cloudhis-21", "cloudhis-27", "cloudhis-29", "cloudhis-30", "cloudhis-31", "cloudhis-32", "cloudhis-33"} {
		c := byCode(checks, code)
		if c.Status == "UNKNOWN" || c.Reason != "" {
			t.Fatalf("unexpected query/permission error %s: %+v", code, c)
		}
	}
	for n := 20; n <= 35; n++ {
		c := byCode(checks, fmt.Sprintf("cloudhis-%d", n))
		original, e := manualSQL.ReadFile(fmt.Sprintf("manual_sql/cloudhis-%d.sql", n))
		if e != nil || len(c.Executions) != 1 || c.Executions[0].Command != string(original) {
			t.Fatalf("did not execute guide SQL %d", n)
		}
	}
	if !strings.Contains(byCode(checks, "cloudhis-30").Executions[0].Command, "state_change") {
		t.Fatal("original guide SQL was silently rewritten")
	}

	readonly, e := OpenDatabase(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer readonly.Close(context.Background())
	if _, e := readonly.Exec(ctx, "CREATE TABLE should_not_be_written(id int)"); e == nil {
		t.Fatal("inspection connection allowed writes")
	}
	admin, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close(context.Background())
	role := fmt.Sprintf("inspection_limited_%d", time.Now().UnixNano())
	ident := pgx.Identifier{role}.Sanitize()
	if _, e := admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN PASSWORD 'inspection-fixture-password'"); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := admin.Exec(context.Background(), "DROP ROLE "+ident); e != nil {
			t.Error(e)
		}
	}()
	cfg.Username = role
	cfg.Password = "inspection-fixture-password"
	limited := InspectionDatabase(ctx, cfg)
	if byCode(limited, "cloudhis-20").Status != "RECORDED" {
		t.Fatal("unrelated identity evidence discarded")
	}
	if c := byCode(limited, "cloudhis-21"); c.Status != "UNKNOWN" || len(c.Executions) != 1 || c.Executions[0].Error == "" {
		t.Fatal("restricted file settings represented as successful")
	}
	for _, code := range []string{"cloudhis-28", "cloudhis-29", "cloudhis-30", "cloudhis-33"} {
		c := byCode(limited, code)
		if c.Status == "NORMAL" {
			t.Fatal("record-only SQL mislabeled healthy")
		}
		if len(c.Executions) != 1 {
			t.Fatal("SQL execution missing")
		}
	}
}
