package service

import (
	"strings"
	"testing"
)

func TestSplitSQLStatementsHandlesSemicolonsInStringsAndComments(t *testing.T) {
	input := `
-- comment with ;
INSERT INTO demo(name) VALUES ('a;b');
/* block ; comment */
CREATE OR REPLACE VIEW v_demo AS SELECT 'x;y' AS value;
`

	statements := SplitSQLStatements(input)

	if len(statements) != 2 {
		t.Fatalf("expected 2 statements, got %d: %#v", len(statements), statements)
	}
	if statements[0] != "INSERT INTO demo(name) VALUES ('a;b')" {
		t.Fatalf("unexpected first statement: %q", statements[0])
	}
	if statements[1] != "CREATE OR REPLACE VIEW v_demo AS SELECT 'x;y' AS value" {
		t.Fatalf("unexpected second statement: %q", statements[1])
	}
}

func TestSplitSQLStatementsHandlesDollarQuotedFunctionBodies(t *testing.T) {
	input := `
CREATE OR REPLACE FUNCTION public.touch_updated_at()
RETURNS trigger AS $$
BEGIN
  RAISE NOTICE 'updated; at';
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2);
`

	statements := SplitSQLStatements(input)

	if len(statements) != 2 {
		t.Fatalf("expected 2 statements, got %d: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "RAISE NOTICE 'updated; at';") {
		t.Fatalf("expected function body to stay intact, got %q", statements[0])
	}
	if statements[1] != `ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2)` {
		t.Fatalf("unexpected second statement: %q", statements[1])
	}
}

func TestAnalyzeSQLRiskBlocksDangerousStatements(t *testing.T) {
	result := AnalyzeSQLRisk("DROP DATABASE his_prod")

	if result.SQLType != "DROP_DATABASE" {
		t.Fatalf("expected DROP_DATABASE, got %q", result.SQLType)
	}
	if result.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED risk, got %q", result.RiskLevel)
	}
	if result.RiskReason == "" {
		t.Fatalf("expected risk reason")
	}
}

func TestAnalyzeSQLRiskUsesParserForQuotedAlterColumnType(t *testing.T) {
	result := AnalyzeSQLRisk(`ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2)`)

	if result.SQLType != "ALTER_COLUMN_TYPE" {
		t.Fatalf("expected ALTER_COLUMN_TYPE, got %q", result.SQLType)
	}
	if result.RiskLevel != "WARN" {
		t.Fatalf("expected WARN risk, got %q", result.RiskLevel)
	}
	if result.RiskReason == "" {
		t.Fatalf("expected risk reason")
	}
}

func TestAnalyzeSQLRiskBlocksInvalidSQL(t *testing.T) {
	result := AnalyzeSQLRisk("CREATE TABLE broken (")

	if result.SQLType != "SQL_PARSE_ERROR" {
		t.Fatalf("expected SQL_PARSE_ERROR, got %q", result.SQLType)
	}
	if result.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED risk, got %q", result.RiskLevel)
	}
	if result.RiskReason == "" {
		t.Fatalf("expected risk reason")
	}
}

func TestAnalyzeSQLRiskMarksDDLWarningsWithoutBlocking(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		typ  string
	}{
		{name: "alter type", sql: "ALTER TABLE patient ALTER COLUMN code TYPE varchar(20)", typ: "ALTER_COLUMN_TYPE"},
		{name: "create index", sql: "CREATE INDEX idx_patient_name ON patient(name)", typ: "CREATE_INDEX"},
		{name: "set not null", sql: "ALTER TABLE patient ALTER COLUMN code SET NOT NULL", typ: "ALTER_SET_NOT_NULL"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := AnalyzeSQLRisk(tc.sql)
			if result.SQLType != tc.typ {
				t.Fatalf("expected type %q, got %q", tc.typ, result.SQLType)
			}
			if result.RiskLevel != "WARN" {
				t.Fatalf("expected WARN risk, got %q", result.RiskLevel)
			}
			if result.RiskReason == "" {
				t.Fatalf("expected risk reason")
			}
		})
	}
}

func TestParseAlterColumnTypeRefUsesDefaultSchema(t *testing.T) {
	ref := parseAlterColumnTypeRef(`ALTER TABLE patient ALTER COLUMN code TYPE varchar(20)`, "his")

	if ref.schema != "his" || ref.table != "patient" || ref.column != "code" {
		t.Fatalf("unexpected alter ref: %+v", ref)
	}
}

func TestParseAlterColumnTypeRefHandlesQuotedIdentifiers(t *testing.T) {
	ref := parseAlterColumnTypeRef(`ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2)`, "public")

	if ref.schema != "billing" || ref.table != "Order Detail" || ref.column != "total amount" {
		t.Fatalf("unexpected alter ref: %+v", ref)
	}
}
