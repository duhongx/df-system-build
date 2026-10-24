package service

import (
	"df-build-server/internal/model"
	"fmt"
	"strings"
	"testing"
)

func TestViewAlterMergeSafety(t *testing.T) {
	tests := []struct {
		name, sql string
		batches   []int
	}{
		{"same table simple casts", `ALTER TABLE his.t ALTER COLUMN a TYPE numeric(18,7) USING a::numeric(18,7); ALTER TABLE his.t ALTER COLUMN b TYPE numeric(18,5);`, []int{2}},
		{"keep table order", `ALTER TABLE his.t ALTER COLUMN a TYPE int; ALTER TABLE his.u ALTER COLUMN b TYPE int; ALTER TABLE his.t ALTER COLUMN c TYPE int;`, []int{1, 1, 1}},
		{"repeat column preserves intermediate conversion", `ALTER TABLE his.t ALTER COLUMN a TYPE numeric(18,4); ALTER TABLE his.t ALTER COLUMN a TYPE numeric(18,7); ALTER TABLE his.t ALTER COLUMN b TYPE int;`, []int{1, 2}},
		{"cross column using observes prior result", `ALTER TABLE his.t ALTER COLUMN a TYPE int; ALTER TABLE his.t ALTER COLUMN b TYPE int USING a+1; ALTER TABLE his.t ALTER COLUMN c TYPE int;`, []int{1, 1, 1}},
		{"function not merged", `ALTER TABLE his.t ALTER COLUMN a TYPE int; ALTER TABLE his.t ALTER COLUMN b TYPE int USING length(b);`, []int{1, 1}},
		{"quoted table case matters", `ALTER TABLE his."T" ALTER COLUMN a TYPE int; ALTER TABLE his.t ALTER COLUMN b TYPE int;`, []int{1, 1}},
		{"only inheritance matters", `ALTER TABLE ONLY his.t ALTER COLUMN a TYPE int; ALTER TABLE his.t ALTER COLUMN b TYPE int;`, []int{1, 1}},
		{"nested casts and quoted names", `ALTER TABLE "His"."T" ALTER COLUMN "A" TYPE bigint USING "A"::text::bigint; ALTER TABLE "His"."T" ALTER COLUMN "B" TYPE text;`, []int{2}},
		{"semicolon in string kept", `ALTER TABLE his.t ALTER COLUMN a TYPE text; ALTER TABLE his.t ALTER COLUMN b TYPE text USING replace(b,';','')`, []int{1, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := parseAlterColumnTypeRefs(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			var original []string
			for _, r := range refs {
				original = append(original, r.sql)
			}
			result := mergeViewAlterStatements(original)
			if len(result) != len(tc.batches) {
				t.Fatalf("batches %v: %v", tc.batches, result)
			}
			for i, text := range result {
				r, err := parseViewAlterColumnRefs(text, true)
				if err != nil || len(r) != tc.batches[i] {
					t.Fatalf("%s: %v %+v", text, err, r)
				}
			}
			if strings.Contains(tc.name, "semicolon") && !strings.Contains(result[1], "replace(b,';','')") {
				t.Fatal("truncated original SQL")
			}
		})
	}
}

func TestViewAlterMerge48ColumnsIntoThreeBatches(t *testing.T) {
	var text strings.Builder
	for i, n := range []int{2, 10, 36} {
		for j := 0; j < n; j++ {
			fmt.Fprintf(&text, "ALTER TABLE his.table%d ALTER COLUMN col%d TYPE numeric(18,7) USING col%d::numeric(18,7);\n", i, j, j)
		}
	}
	task := model.SQLViewDependencyTask{AlterSQL: text.String()}
	plan := BuildSQLViewDependencyStepExecutionPlan(task, nil)
	if len(plan.AlterSteps) != 3 {
		t.Fatalf("48 changes should become 3 physical statements: %v", plan.AlterSteps)
	}
	tx := BuildSQLViewDependencyTransactionalSteps(task, nil)
	if len(tx) != 8 {
		t.Fatalf("transaction should contain 4 setup + 3 ALTER + commit: %v", tx)
	}
	manual := BuildSQLViewDependencyManualPlan(task, nil)
	if strings.Count(manual, "ALTER TABLE") != 3 {
		t.Fatal("export uses a different plan")
	}
}
