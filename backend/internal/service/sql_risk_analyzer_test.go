package service

import (
	"strings"
	"testing"
)

func TestAnalyzeSQLRiskKeepsExistingBlockedRules(t *testing.T) {
	tests := []struct {
		sql string
		typ string
	}{
		{"DROP DATABASE his_prod", "DROP_DATABASE"},
		{"ALTER SYSTEM SET work_mem = '64MB'", "ALTER_SYSTEM"},
		{"VACUUM FULL his.patient", "VACUUM_FULL"},
	}

	for _, tc := range tests {
		got := AnalyzeSQLRisk(tc.sql)
		if got.SQLType != tc.typ || got.RiskLevel != "BLOCKED" {
			t.Fatalf("expected %s BLOCKED, got %+v", tc.typ, got)
		}
	}
}

func TestAnalyzeSQLRiskBlocksBusinessSQLWithoutExplicitSchema(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{name: "create table", sql: "CREATE TABLE patient(id int)"},
		{name: "alter table", sql: "ALTER TABLE patient ADD COLUMN code text"},
		{name: "insert", sql: "INSERT INTO patient(id) VALUES (1)"},
		{name: "update", sql: "UPDATE patient SET id = 2 WHERE id = 1"},
		{name: "delete", sql: "DELETE FROM patient WHERE id = 1"},
		{name: "create index", sql: "CREATE INDEX idx_patient_id ON patient(id)"},
		{name: "create view", sql: "CREATE VIEW v_patient AS SELECT id FROM his.patient"},
		{name: "view source table", sql: "CREATE VIEW his.v_patient AS SELECT id FROM patient"},
		{name: "truncate", sql: "TRUNCATE TABLE patient"},
		{name: "refresh materialized view", sql: "REFRESH MATERIALIZED VIEW patient_summary"},
		{name: "create function", sql: "CREATE OR REPLACE FUNCTION f_patient() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != "MISSING_SCHEMA" {
				t.Fatalf("expected MISSING_SCHEMA, got %+v", got)
			}
			if got.RiskLevel != "BLOCKED" {
				t.Fatalf("expected BLOCKED, got %+v", got)
			}
			if !strings.Contains(got.RiskReason, "未显式指定 schema") {
				t.Fatalf("expected missing schema reason, got %q", got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskAllowsExplicitSchemaBusinessSQL(t *testing.T) {
	tests := []string{
		"CREATE TABLE his.patient(id int)",
		"ALTER TABLE his.patient ADD COLUMN code text",
		"INSERT INTO his.patient(id) VALUES (1)",
		"UPDATE his.patient SET id = 2 WHERE id = 1",
		"DELETE FROM his.patient WHERE id = 1",
		"CREATE INDEX idx_patient_id ON his.patient(id)",
		"CREATE VIEW his.v_patient AS SELECT id FROM his.patient",
		"CREATE VIEW his.v_patient AS WITH recent_patient AS (SELECT id FROM his.patient) SELECT id FROM recent_patient",
		"TRUNCATE TABLE his.patient",
	}

	for _, sqlText := range tests {
		t.Run(sqlText, func(t *testing.T) {
			got := AnalyzeSQLRisk(sqlText)
			if got.SQLType == "MISSING_SCHEMA" {
				t.Fatalf("expected explicit schema SQL to pass schema guard, got %+v", got)
			}
		})
	}
}

func TestAnalyzeSQLRiskStillBlocksMissingSchemaInsideCTE(t *testing.T) {
	got := AnalyzeSQLRisk("CREATE VIEW his.v_patient AS WITH recent_patient AS (SELECT id FROM patient) SELECT id FROM recent_patient")
	if got.SQLType != "MISSING_SCHEMA" {
		t.Fatalf("expected MISSING_SCHEMA for unqualified table inside CTE, got %+v", got)
	}
	if !strings.Contains(got.RiskReason, "patient") {
		t.Fatalf("expected reason to mention unqualified table, got %q", got.RiskReason)
	}
}

func TestAnalyzeSQLRiskAllowsNonBusinessSQLWithoutSchema(t *testing.T) {
	tests := []string{
		"SELECT 1",
		"CREATE EXTENSION IF NOT EXISTS pgcrypto",
		"VACUUM",
	}

	for _, sqlText := range tests {
		t.Run(sqlText, func(t *testing.T) {
			got := AnalyzeSQLRisk(sqlText)
			if got.SQLType == "MISSING_SCHEMA" {
				t.Fatalf("expected non-business SQL to pass schema guard, got %+v", got)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesColumnTypeChanges(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantLevel  string
		wantReason string
	}{
		{
			name:       "varchar change requires metadata check",
			sql:        "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(20)",
			wantLevel:  "WARN",
			wantReason: "varchar 类型变更需要结合原字段长度判断",
		},
		{
			name:       "using conversion warns",
			sql:        "ALTER TABLE his.patient ALTER COLUMN id TYPE uuid USING id::uuid",
			wantLevel:  "WARN",
			wantReason: "USING",
		},
		{
			name:       "generic type change warns",
			sql:        "ALTER TABLE his.patient ALTER COLUMN payload TYPE bytea",
			wantLevel:  "WARN",
			wantReason: "字段类型变更",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != "ALTER_COLUMN_TYPE" {
				t.Fatalf("expected ALTER_COLUMN_TYPE, got %+v", got)
			}
			if got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s, got %+v", tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesAdditionalColumnTypeRisks(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantReason string
	}{
		{
			name:       "numeric precision change",
			sql:        "ALTER TABLE his.patient ALTER COLUMN amount TYPE numeric(8,2)",
			wantReason: "numeric 精度",
		},
		{
			name:       "storage format change",
			sql:        "ALTER TABLE his.patient ALTER COLUMN payload TYPE jsonb",
			wantReason: "存储格式",
		},
		{
			name:       "timestamp timezone conversion",
			sql:        "ALTER TABLE his.patient ALTER COLUMN created_at TYPE timestamptz",
			wantReason: "时区语义",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != "ALTER_COLUMN_TYPE" || got.RiskLevel != "WARN" {
				t.Fatalf("expected ALTER_COLUMN_TYPE WARN, got %+v", got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesAddColumnDefaultRisks(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantType   string
		wantLevel  string
		wantReason string
	}{
		{
			name:       "volatile default warns",
			sql:        "ALTER TABLE his.patient ADD COLUMN trace_id uuid DEFAULT uuid_generate_v4()",
			wantType:   "ADD_COLUMN_DEFAULT_VOLATILE",
			wantLevel:  "WARN",
			wantReason: "volatile",
		},
		{
			name:       "schema qualified volatile default warns",
			sql:        "ALTER TABLE his.patient ADD COLUMN trace_id uuid DEFAULT public.uuid_generate_v4()",
			wantType:   "ADD_COLUMN_DEFAULT_VOLATILE",
			wantLevel:  "WARN",
			wantReason: "volatile",
		},
		{
			name:      "constant default stays low",
			sql:       "ALTER TABLE his.patient ADD COLUMN status int DEFAULT 0",
			wantType:  "ADD_COLUMN",
			wantLevel: "LOW",
		},
		{
			name:      "function name in literal is not volatile default",
			sql:       "ALTER TABLE his.patient ADD COLUMN note text DEFAULT 'now()'",
			wantType:  "ADD_COLUMN",
			wantLevel: "LOW",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s %s, got %+v", tc.wantType, tc.wantLevel, got)
			}
			if tc.wantReason != "" && !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesDropIndexConcurrency(t *testing.T) {
	tests := []struct {
		sql        string
		wantType   string
		wantLevel  string
		wantReason string
	}{
		{
			sql:        "DROP INDEX his.idx_patient_name",
			wantType:   "DROP_INDEX",
			wantLevel:  "WARN",
			wantReason: "非 CONCURRENTLY",
		},
		{
			sql:        "DROP INDEX CONCURRENTLY his.idx_patient_name",
			wantType:   "DROP_INDEX_CONCURRENTLY",
			wantLevel:  "WARN",
			wantReason: "并发删除索引",
		},
	}

	for _, tc := range tests {
		t.Run(tc.sql, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s %s, got %+v", tc.wantType, tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskBlocksDMLWithoutWhere(t *testing.T) {
	tests := []struct {
		sql        string
		wantType   string
		wantReason string
	}{
		{
			sql:        "UPDATE his.patient SET status = 1",
			wantType:   "UPDATE_WITHOUT_WHERE",
			wantReason: "缺少 WHERE",
		},
		{
			sql:        "DELETE FROM his.patient",
			wantType:   "DELETE_WITHOUT_WHERE",
			wantReason: "缺少 WHERE",
		},
	}

	for _, tc := range tests {
		t.Run(tc.sql, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != "BLOCKED" {
				t.Fatalf("expected %s BLOCKED, got %+v", tc.wantType, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskBlocksDMLWithTrivialWhere(t *testing.T) {
	tests := []struct {
		sql      string
		wantType string
	}{
		{sql: "UPDATE his.patient SET status = 1 WHERE true", wantType: "UPDATE_TRIVIAL_WHERE"},
		{sql: "UPDATE his.patient SET status = 1 WHERE 1 = 1", wantType: "UPDATE_TRIVIAL_WHERE"},
		{sql: "DELETE FROM his.patient WHERE TRUE", wantType: "DELETE_TRIVIAL_WHERE"},
		{sql: "DELETE FROM his.patient WHERE 1=1", wantType: "DELETE_TRIVIAL_WHERE"},
	}

	for _, tc := range tests {
		t.Run(tc.sql, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != "BLOCKED" {
				t.Fatalf("expected %s BLOCKED, got %+v", tc.wantType, got)
			}
			if !strings.Contains(got.RiskReason, "无有效过滤条件") {
				t.Fatalf("expected trivial where reason, got %q", got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesWeakDMLWhere(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantType   string
		wantLevel  string
		wantReason string
	}{
		{
			name:       "update with not null filter blocks",
			sql:        "UPDATE his.patient SET status = 1 WHERE id IS NOT NULL",
			wantType:   "UPDATE_WEAK_WHERE",
			wantLevel:  "BLOCKED",
			wantReason: "IS NOT NULL",
		},
		{
			name:       "delete with not null filter blocks",
			sql:        "DELETE FROM his.patient WHERE id IS NOT NULL",
			wantType:   "DELETE_WEAK_WHERE",
			wantLevel:  "BLOCKED",
			wantReason: "IS NOT NULL",
		},
		{
			name:       "boolean flag filter warns",
			sql:        "UPDATE his.patient SET status = 1 WHERE deleted = false",
			wantType:   "UPDATE_WEAK_WHERE",
			wantLevel:  "WARN",
			wantReason: "布尔条件",
		},
		{
			name:       "leading wildcard like warns",
			sql:        "DELETE FROM his.patient WHERE name LIKE '%test%'",
			wantType:   "DELETE_WEAK_WHERE",
			wantLevel:  "WARN",
			wantReason: "LIKE 前缀通配符",
		},
		{
			name:       "unbounded now time filter warns",
			sql:        "UPDATE his.patient SET status = 1 WHERE created_at < now()",
			wantType:   "UPDATE_WEAK_WHERE",
			wantLevel:  "WARN",
			wantReason: "时间条件",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s %s, got %+v", tc.wantType, tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesRiskyUpdateSetExpressions(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantReason string
	}{
		{
			name:       "set column null",
			sql:        "UPDATE his.patient SET mobile = NULL WHERE id = 1",
			wantReason: "置空",
		},
		{
			name:       "arithmetic update",
			sql:        "UPDATE his.orders SET amount = amount * 10 WHERE id = 1",
			wantReason: "算术表达式",
		},
		{
			name:       "empty json assignment",
			sql:        "UPDATE his.patient SET ext = '{}' WHERE id = 1",
			wantReason: "JSON",
		},
		{
			name:       "deleted status assignment",
			sql:        "UPDATE his.patient SET status = 'DELETED' WHERE id = 1",
			wantReason: "删除状态",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != "UPDATE_RISKY_SET" || got.RiskLevel != "WARN" {
				t.Fatalf("expected UPDATE_RISKY_SET WARN, got %+v", got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesAdditionalBytebaseStyleRisks(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantType   string
		wantLevel  string
		wantReason string
	}{
		{
			name:       "foreign key scans existing data",
			sql:        "ALTER TABLE his.orders ADD CONSTRAINT fk_patient FOREIGN KEY (patient_id) REFERENCES his.patient(id)",
			wantType:   "ADD_FOREIGN_KEY",
			wantLevel:  "WARN",
			wantReason: "外键",
		},
		{
			name:       "primary key creates index",
			sql:        "ALTER TABLE his.patient ADD CONSTRAINT patient_pk PRIMARY KEY (id)",
			wantType:   "ADD_PRIMARY_KEY",
			wantLevel:  "WARN",
			wantReason: "索引",
		},
		{
			name:       "validate constraint scans data",
			sql:        "ALTER TABLE his.patient VALIDATE CONSTRAINT chk_patient_code",
			wantType:   "VALIDATE_CONSTRAINT",
			wantLevel:  "WARN",
			wantReason: "扫描",
		},
		{
			name:       "attach partition",
			sql:        "ALTER TABLE his.patient ATTACH PARTITION his.patient_2026 FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
			wantType:   "ATTACH_PARTITION",
			wantLevel:  "WARN",
			wantReason: "分区",
		},
		{
			name:       "refresh materialized view",
			sql:        "REFRESH MATERIALIZED VIEW his.patient_summary",
			wantType:   "REFRESH_MATERIALIZED_VIEW",
			wantLevel:  "WARN",
			wantReason: "物化视图",
		},
		{
			name:       "create or replace function",
			sql:        "CREATE OR REPLACE FUNCTION his.f_patient() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$",
			wantType:   "CREATE_FUNCTION",
			wantLevel:  "WARN",
			wantReason: "函数",
		},
		{
			name:       "drop function",
			sql:        "DROP FUNCTION his.f_patient()",
			wantType:   "DROP_FUNCTION",
			wantLevel:  "WARN",
			wantReason: "函数",
		},
		{
			name:       "create extension",
			sql:        "CREATE EXTENSION IF NOT EXISTS pgcrypto",
			wantType:   "CREATE_EXTENSION",
			wantLevel:  "WARN",
			wantReason: "扩展",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s %s, got %+v", tc.wantType, tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesObjectLevelRisks(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantType   string
		wantLevel  string
		wantReason string
	}{
		{
			name:       "drop column warns",
			sql:        "ALTER TABLE his.patient DROP COLUMN mobile",
			wantType:   "DROP_COLUMN",
			wantLevel:  "WARN",
			wantReason: "删除字段",
		},
		{
			name:       "drop constraint warns",
			sql:        "ALTER TABLE his.patient DROP CONSTRAINT patient_code_key",
			wantType:   "DROP_CONSTRAINT",
			wantLevel:  "WARN",
			wantReason: "删除约束",
		},
		{
			name:       "set schema warns",
			sql:        "ALTER TABLE his.patient SET SCHEMA archive",
			wantType:   "ALTER_SET_SCHEMA",
			wantLevel:  "WARN",
			wantReason: "迁移 schema",
		},
		{
			name:       "change owner warns",
			sql:        "ALTER TABLE his.patient OWNER TO app_user",
			wantType:   "ALTER_OWNER",
			wantLevel:  "WARN",
			wantReason: "OWNER",
		},
		{
			name:       "rename column warns",
			sql:        "ALTER TABLE his.patient RENAME COLUMN name TO full_name",
			wantType:   "RENAME_OBJECT",
			wantLevel:  "WARN",
			wantReason: "重命名",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s %s, got %+v", tc.wantType, tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesTriggerRisks(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantType   string
		wantLevel  string
		wantReason string
	}{
		{
			name:       "create trigger warns",
			sql:        "CREATE TRIGGER trg_patient_audit AFTER UPDATE ON his.patient FOR EACH ROW EXECUTE FUNCTION his.audit_patient()",
			wantType:   "CREATE_TRIGGER",
			wantLevel:  "WARN",
			wantReason: "触发器",
		},
		{
			name:       "drop trigger warns",
			sql:        "DROP TRIGGER trg_patient_audit ON his.patient",
			wantType:   "DROP_TRIGGER",
			wantLevel:  "WARN",
			wantReason: "触发器",
		},
		{
			name:       "disable trigger warns",
			sql:        "ALTER TABLE his.patient DISABLE TRIGGER ALL",
			wantType:   "DISABLE_TRIGGER",
			wantLevel:  "WARN",
			wantReason: "禁用触发器",
		},
		{
			name:       "enable trigger warns",
			sql:        "ALTER TABLE his.patient ENABLE TRIGGER trg_patient_audit",
			wantType:   "ENABLE_TRIGGER",
			wantLevel:  "WARN",
			wantReason: "启用触发器",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s %s, got %+v", tc.wantType, tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesIndexExpressionRisks(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantType   string
		wantReason string
	}{
		{
			name:       "unique index",
			sql:        "CREATE UNIQUE INDEX idx_patient_code ON his.patient(code)",
			wantType:   "CREATE_UNIQUE_INDEX",
			wantReason: "唯一",
		},
		{
			name:       "expression index",
			sql:        "CREATE INDEX idx_patient_lower_name ON his.patient((lower(name)))",
			wantType:   "CREATE_INDEX_EXPRESSION",
			wantReason: "表达式索引",
		},
		{
			name:       "partial index",
			sql:        "CREATE INDEX idx_patient_active ON his.patient(name) WHERE deleted = false",
			wantType:   "CREATE_INDEX_PARTIAL",
			wantReason: "部分索引",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != "WARN" {
				t.Fatalf("expected %s WARN, got %+v", tc.wantType, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestAnalyzeSQLRiskClassifiesNotValidCheckAsLow(t *testing.T) {
	got := AnalyzeSQLRisk("ALTER TABLE his.patient ADD CONSTRAINT chk_code CHECK (code <> '') NOT VALID")

	if got.SQLType != "ADD_CHECK_NOT_VALID" || got.RiskLevel != "LOW" {
		t.Fatalf("expected ADD_CHECK_NOT_VALID LOW, got %+v", got)
	}
}

func TestDropAndTruncatePolicyRecognizesTemporaryNames(t *testing.T) {
	tests := []string{
		"DROP TABLE his.temp_patient",
		"DROP TABLE his.patient_tmp",
		"TRUNCATE TABLE his.bak_patient",
		"TRUNCATE TABLE his.patient_bak",
	}

	for _, sqlText := range tests {
		got := AnalyzeSQLRisk(sqlText)
		if got.RiskLevel != "LOW" {
			t.Fatalf("expected LOW for %s, got %+v", sqlText, got)
		}
		if !strings.Contains(got.RiskReason, "临时/备份表命名规则命中") {
			t.Fatalf("expected temp/backup reason for %s, got %q", sqlText, got.RiskReason)
		}
	}
}

func TestDropAndTruncateDefaultBlockedWithoutMetadata(t *testing.T) {
	tests := []struct {
		sql        string
		wantType   string
		wantReason string
	}{
		{sql: "DROP TABLE his.patient", wantType: "DROP_TABLE", wantReason: "默认禁止"},
		{sql: "TRUNCATE TABLE his.patient", wantType: "TRUNCATE", wantReason: "默认禁止"},
	}

	for _, tc := range tests {
		t.Run(tc.sql, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != "BLOCKED" {
				t.Fatalf("expected %s BLOCKED, got %+v", tc.wantType, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}

func TestSmallDropOrTruncateWithMetadataWarns(t *testing.T) {
	stats := TableStats{SchemaName: "his", TableName: "patient", TotalBytes: 1024, EstimatedRows: 3}

	for _, sqlType := range []string{"DROP_TABLE", "TRUNCATE"} {
		t.Run(sqlType, func(t *testing.T) {
			got := classifyDestructiveTableOperation(sqlType, stats)
			if got.RiskLevel != "WARN" {
				t.Fatalf("expected WARN for metadata-checked small table, got %+v", got)
			}
			if !strings.Contains(got.RiskReason, "请确认对象范围") {
				t.Fatalf("expected confirmation reason, got %q", got.RiskReason)
			}
		})
	}
}

func TestLargeDropOrTruncateIsBlocked(t *testing.T) {
	stats := TableStats{SchemaName: "public", TableName: "patient", TotalBytes: largeTableBytes + 1, EstimatedRows: 1}

	got := classifyDestructiveTableOperation("DROP_TABLE", stats)

	if got.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED, got %+v", got)
	}
	if !strings.Contains(got.RiskReason, "大表") {
		t.Fatalf("expected large table reason, got %q", got.RiskReason)
	}
}

func TestLargeTableSensitiveOperationIsBlocked(t *testing.T) {
	stats := TableStats{SchemaName: "public", TableName: "patient", TotalBytes: largeTableBytes + 1, EstimatedRows: largeTableRows + 1}
	base := RiskAnalysis{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入"}

	got := classifyLargeTableSensitiveOperation(base, stats)

	if got.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED, got %+v", got)
	}
	if !strings.Contains(got.RiskReason, "大表") {
		t.Fatalf("expected large table reason, got %q", got.RiskReason)
	}
}

func TestIndexVariantsAreLargeTableSensitive(t *testing.T) {
	tests := []string{"CREATE_INDEX", "CREATE_UNIQUE_INDEX", "CREATE_INDEX_EXPRESSION", "CREATE_INDEX_PARTIAL"}
	for _, sqlType := range tests {
		t.Run(sqlType, func(t *testing.T) {
			if !isLargeTableSensitiveSQLType(sqlType) {
				t.Fatalf("expected %s to be large-table sensitive", sqlType)
			}
		})
	}
}

func TestExecutionStrategyForConcurrentIndex(t *testing.T) {
	strategy := DetermineExecutionStrategy(AnalyzeSQLRisk("CREATE INDEX CONCURRENTLY idx_patient_name ON his.patient(name)"))

	if strategy.CanRunInTransaction {
		t.Fatalf("concurrent index must not run in transaction")
	}
	if strategy.Name != "DIRECT_NO_TRANSACTION" {
		t.Fatalf("unexpected strategy: %+v", strategy)
	}
}

func TestExecutionStrategyForConcurrentUniqueIndex(t *testing.T) {
	risk := AnalyzeSQLRisk("CREATE UNIQUE INDEX CONCURRENTLY idx_patient_code ON his.patient(code)")
	strategy := DetermineExecutionStrategy(risk)

	if risk.SQLType != "CREATE_INDEX_CONCURRENTLY" {
		t.Fatalf("expected concurrent index type to preserve execution strategy, got %+v", risk)
	}
	if strategy.CanRunInTransaction {
		t.Fatalf("concurrent unique index must not run in transaction")
	}
	if !strings.Contains(risk.RiskReason, "唯一") {
		t.Fatalf("expected unique risk reason, got %q", risk.RiskReason)
	}
}

func TestExecutionStrategyForDropIndexConcurrently(t *testing.T) {
	strategy := DetermineExecutionStrategy(AnalyzeSQLRisk("DROP INDEX CONCURRENTLY his.idx_patient_name"))

	if strategy.CanRunInTransaction {
		t.Fatalf("drop index concurrently must not run in transaction")
	}
	if strategy.Name != "DIRECT_NO_TRANSACTION" {
		t.Fatalf("unexpected strategy: %+v", strategy)
	}
}

func TestExecutionStrategyForBlockedSQL(t *testing.T) {
	strategy := DetermineExecutionStrategy(AnalyzeSQLRisk("ALTER SYSTEM SET work_mem = '64MB'"))

	if strategy.CanRunInTransaction {
		t.Fatalf("blocked sql should not be transaction runnable")
	}
	if strategy.Name != "MANUAL_EXPORT" {
		t.Fatalf("unexpected strategy: %+v", strategy)
	}
}
