package service

import (
	"strings"
	"testing"
)

func TestClassifyColumnTypeChangeWithMetadata(t *testing.T) {
	tests := []struct {
		name       string
		column     ColumnInfo
		targetType string
		hasUsing   bool
		wantLevel  string
		wantReason string
	}{
		{
			name: "varchar expansion is low with metadata",
			column: ColumnInfo{
				DataType:                  "character varying",
				CharacterMaximumLength:    10,
				HasCharacterMaximumLength: true,
			},
			targetType: "varchar(20)",
			wantLevel:  "LOW",
			wantReason: "varchar 长度扩容",
		},
		{
			name: "varchar shrink warns",
			column: ColumnInfo{
				DataType:                  "character varying",
				CharacterMaximumLength:    20,
				HasCharacterMaximumLength: true,
			},
			targetType: "varchar(10)",
			wantLevel:  "WARN",
			wantReason: "varchar 长度缩容",
		},
		{
			name: "using conversion warns even when target length expands",
			column: ColumnInfo{
				DataType:                  "character varying",
				CharacterMaximumLength:    10,
				HasCharacterMaximumLength: true,
			},
			targetType: "varchar(20)",
			hasUsing:   true,
			wantLevel:  "WARN",
			wantReason: "USING",
		},
		{
			name:       "generic type change warns",
			column:     ColumnInfo{DataType: "integer"},
			targetType: "uuid",
			wantLevel:  "WARN",
			wantReason: "字段类型从 integer 变更为 uuid",
		},
		{
			name:       "text to bounded varchar warns",
			column:     ColumnInfo{DataType: "text", UDTName: "text"},
			targetType: "varchar(64)",
			wantLevel:  "WARN",
			wantReason: "需要校验已有数据长度",
		},
		{
			name:       "varchar to text is low",
			column:     ColumnInfo{DataType: "character varying", UDTName: "varchar", CharacterMaximumLength: 64, HasCharacterMaximumLength: true},
			targetType: "text",
			wantLevel:  "LOW",
			wantReason: "varchar 转 text",
		},
		{
			name: "numeric precision shrink warns",
			column: ColumnInfo{
				DataType:            "numeric",
				NumericPrecision:    12,
				HasNumericPrecision: true,
				NumericScale:        2,
				HasNumericScale:     true,
			},
			targetType: "numeric(8,2)",
			wantLevel:  "WARN",
			wantReason: "numeric 精度缩小",
		},
		{
			name:       "bigint to integer blocks",
			column:     ColumnInfo{DataType: "bigint", UDTName: "int8"},
			targetType: "integer",
			wantLevel:  "BLOCKED",
			wantReason: "整数范围缩小",
		},
		{
			name:       "numeric to integer blocks",
			column:     ColumnInfo{DataType: "numeric", UDTName: "numeric"},
			targetType: "int",
			wantLevel:  "BLOCKED",
			wantReason: "numeric 转整数",
		},
		{
			name:       "text to uuid blocks",
			column:     ColumnInfo{DataType: "text", UDTName: "text"},
			targetType: "uuid",
			wantLevel:  "BLOCKED",
			wantReason: "需要数据校验",
		},
		{
			name:       "timestamp to timestamptz warns",
			column:     ColumnInfo{DataType: "timestamp without time zone", UDTName: "timestamp"},
			targetType: "timestamptz",
			wantLevel:  "WARN",
			wantReason: "时区语义",
		},
		{
			name:       "json to jsonb warns",
			column:     ColumnInfo{DataType: "json", UDTName: "json"},
			targetType: "jsonb",
			wantLevel:  "WARN",
			wantReason: "JSON 存储格式",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyColumnTypeChangeWithMetadata(tc.column, tc.targetType, tc.hasUsing)
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

func TestCompareViewColumnsDetectsIncompatibleColumnOrder(t *testing.T) {
	oldCols := []ViewColumn{
		{Name: "id", DataType: "integer", Ordinal: 1},
		{Name: "name", DataType: "text", Ordinal: 2},
	}
	newCols := []ViewColumn{
		{Name: "name", DataType: "text", Ordinal: 1},
		{Name: "id", DataType: "integer", Ordinal: 2},
	}

	got := compareViewColumns(oldCols, newCols)

	if got.Compatible {
		t.Fatalf("expected incompatible result")
	}
	if !strings.Contains(got.Reason, "列顺序") {
		t.Fatalf("expected order reason, got %q", got.Reason)
	}
}

func TestCompareViewColumnsDetectsIncompatibleColumnType(t *testing.T) {
	oldCols := []ViewColumn{{Name: "id", DataType: "integer", Ordinal: 1}}
	newCols := []ViewColumn{{Name: "id", DataType: "bigint", Ordinal: 1}}

	got := compareViewColumns(oldCols, newCols)

	if got.Compatible {
		t.Fatalf("expected incompatible result")
	}
	if !strings.Contains(got.Reason, "列类型") {
		t.Fatalf("expected type reason, got %q", got.Reason)
	}
}

func TestCompareViewColumnsTreatsPostgresTypeAliasesAsCompatible(t *testing.T) {
	oldCols := []ViewColumn{{Name: "id", DataType: "integer", Ordinal: 1}}
	newCols := []ViewColumn{{Name: "id", DataType: "INT4", Ordinal: 1}}

	got := compareViewColumns(oldCols, newCols)

	if !got.Compatible {
		t.Fatalf("expected postgres integer aliases to be compatible, got %+v", got)
	}
}

func TestBuildDMLExplainSQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "update",
			sql:  "UPDATE patient SET status = 1 WHERE id = 10;",
			want: "EXPLAIN (FORMAT JSON) UPDATE patient SET status = 1 WHERE id = 10",
		},
		{
			name: "delete",
			sql:  "DELETE FROM patient WHERE status = 9",
			want: "EXPLAIN (FORMAT JSON) DELETE FROM patient WHERE status = 9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := buildDMLExplainSQL(tc.sql)
			if !ok {
				t.Fatalf("expected explain sql to be built")
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestExtractPlanRowsFromExplainJSON(t *testing.T) {
	explainJSON := `[{"Plan":{"Node Type":"ModifyTable","Operation":"Update","Plan Rows":0,"Plans":[{"Node Type":"Seq Scan","Relation Name":"patient","Plan Rows":120000}]}}]`

	got, ok := extractPlanRowsFromExplainJSON(explainJSON)

	if !ok {
		t.Fatalf("expected plan rows to be extracted")
	}
	if got != 120000 {
		t.Fatalf("expected 120000 rows, got %d", got)
	}
}

func TestClassifyDMLAffectedRows(t *testing.T) {
	tests := []struct {
		name      string
		base      RiskAnalysis
		rows      int64
		wantLevel string
	}{
		{
			name:      "small update stays low with estimate reason",
			base:      RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"},
			rows:      10,
			wantLevel: "LOW",
		},
		{
			name:      "large delete warns",
			base:      RiskAnalysis{SQLType: "DELETE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"},
			rows:      dmlAffectedRowsWarnThreshold + 1,
			wantLevel: "WARN",
		},
		{
			name:      "huge update blocks",
			base:      RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"},
			rows:      dmlAffectedRowsBlockThreshold + 1,
			wantLevel: "BLOCKED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDMLAffectedRows(tc.base, tc.rows)
			if got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s, got %+v", tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, "EXPLAIN 估算影响行数") {
				t.Fatalf("expected estimate reason, got %q", got.RiskReason)
			}
			if got.EstimatedRows != tc.rows {
				t.Fatalf("expected estimated rows %d, got %d", tc.rows, got.EstimatedRows)
			}
		})
	}
}
