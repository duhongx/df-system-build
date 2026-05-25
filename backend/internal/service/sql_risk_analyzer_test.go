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
		{"VACUUM FULL patient", "VACUUM_FULL"},
	}

	for _, tc := range tests {
		got := AnalyzeSQLRisk(tc.sql)
		if got.SQLType != tc.typ || got.RiskLevel != "BLOCKED" {
			t.Fatalf("expected %s BLOCKED, got %+v", tc.typ, got)
		}
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
			sql:        "ALTER TABLE patient ALTER COLUMN code TYPE varchar(20)",
			wantLevel:  "WARN",
			wantReason: "varchar 类型变更需要结合原字段长度判断",
		},
		{
			name:       "using conversion warns",
			sql:        "ALTER TABLE patient ALTER COLUMN id TYPE uuid USING id::uuid",
			wantLevel:  "WARN",
			wantReason: "USING",
		},
		{
			name:       "generic type change warns",
			sql:        "ALTER TABLE patient ALTER COLUMN payload TYPE bytea",
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
			sql:        "ALTER TABLE patient ALTER COLUMN amount TYPE numeric(8,2)",
			wantReason: "numeric 精度",
		},
		{
			name:       "storage format change",
			sql:        "ALTER TABLE patient ALTER COLUMN payload TYPE jsonb",
			wantReason: "存储格式",
		},
		{
			name:       "timestamp timezone conversion",
			sql:        "ALTER TABLE patient ALTER COLUMN created_at TYPE timestamptz",
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
			sql:        "ALTER TABLE patient ADD COLUMN trace_id uuid DEFAULT uuid_generate_v4()",
			wantType:   "ADD_COLUMN_DEFAULT_VOLATILE",
			wantLevel:  "WARN",
			wantReason: "volatile",
		},
		{
			name:       "schema qualified volatile default warns",
			sql:        "ALTER TABLE patient ADD COLUMN trace_id uuid DEFAULT public.uuid_generate_v4()",
			wantType:   "ADD_COLUMN_DEFAULT_VOLATILE",
			wantLevel:  "WARN",
			wantReason: "volatile",
		},
		{
			name:      "constant default stays low",
			sql:       "ALTER TABLE patient ADD COLUMN status int DEFAULT 0",
			wantType:  "ADD_COLUMN",
			wantLevel: "LOW",
		},
		{
			name:      "function name in literal is not volatile default",
			sql:       "ALTER TABLE patient ADD COLUMN note text DEFAULT 'now()'",
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
			sql:        "DROP INDEX idx_patient_name",
			wantType:   "DROP_INDEX",
			wantLevel:  "WARN",
			wantReason: "非 CONCURRENTLY",
		},
		{
			sql:        "DROP INDEX CONCURRENTLY idx_patient_name",
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
			sql:        "ALTER TABLE orders ADD CONSTRAINT fk_patient FOREIGN KEY (patient_id) REFERENCES patient(id)",
			wantType:   "ADD_FOREIGN_KEY",
			wantLevel:  "WARN",
			wantReason: "外键",
		},
		{
			name:       "primary key creates index",
			sql:        "ALTER TABLE patient ADD CONSTRAINT patient_pk PRIMARY KEY (id)",
			wantType:   "ADD_PRIMARY_KEY",
			wantLevel:  "WARN",
			wantReason: "索引",
		},
		{
			name:       "validate constraint scans data",
			sql:        "ALTER TABLE patient VALIDATE CONSTRAINT chk_patient_code",
			wantType:   "VALIDATE_CONSTRAINT",
			wantLevel:  "WARN",
			wantReason: "扫描",
		},
		{
			name:       "attach partition",
			sql:        "ALTER TABLE patient ATTACH PARTITION patient_2026 FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
			wantType:   "ATTACH_PARTITION",
			wantLevel:  "WARN",
			wantReason: "分区",
		},
		{
			name:       "refresh materialized view",
			sql:        "REFRESH MATERIALIZED VIEW patient_summary",
			wantType:   "REFRESH_MATERIALIZED_VIEW",
			wantLevel:  "WARN",
			wantReason: "物化视图",
		},
		{
			name:       "create or replace function",
			sql:        "CREATE OR REPLACE FUNCTION f_patient() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$",
			wantType:   "CREATE_FUNCTION",
			wantLevel:  "WARN",
			wantReason: "函数",
		},
		{
			name:       "drop function",
			sql:        "DROP FUNCTION f_patient()",
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

func TestAnalyzeSQLRiskClassifiesNotValidCheckAsLow(t *testing.T) {
	got := AnalyzeSQLRisk("ALTER TABLE patient ADD CONSTRAINT chk_code CHECK (code <> '') NOT VALID")

	if got.SQLType != "ADD_CHECK_NOT_VALID" || got.RiskLevel != "LOW" {
		t.Fatalf("expected ADD_CHECK_NOT_VALID LOW, got %+v", got)
	}
}

func TestDropAndTruncatePolicyRecognizesTemporaryNames(t *testing.T) {
	tests := []string{
		"DROP TABLE temp_patient",
		"DROP TABLE patient_tmp",
		"TRUNCATE TABLE bak_patient",
		"TRUNCATE TABLE patient_bak",
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

func TestExecutionStrategyForConcurrentIndex(t *testing.T) {
	strategy := DetermineExecutionStrategy(AnalyzeSQLRisk("CREATE INDEX CONCURRENTLY idx_patient_name ON patient(name)"))

	if strategy.CanRunInTransaction {
		t.Fatalf("concurrent index must not run in transaction")
	}
	if strategy.Name != "DIRECT_NO_TRANSACTION" {
		t.Fatalf("unexpected strategy: %+v", strategy)
	}
}

func TestExecutionStrategyForDropIndexConcurrently(t *testing.T) {
	strategy := DetermineExecutionStrategy(AnalyzeSQLRisk("DROP INDEX CONCURRENTLY idx_patient_name"))

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
