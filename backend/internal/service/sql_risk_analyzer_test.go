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
			sql:        "ALTER TABLE patient ALTER COLUMN payload TYPE jsonb",
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
