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
