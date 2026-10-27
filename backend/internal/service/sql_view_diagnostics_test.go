package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSQLViewDependencyErrorKeepsPostgresDetails(t *testing.T) {
	cause := &pgconn.PgError{Code: "53200", Message: "out of shared memory", Detail: "cannot acquire more relation locks", Hint: "You might need to increase max_locks_per_transaction.", SchemaName: "s", TableName: "t", ColumnName: "amount", Where: "SQL statement in rebuild", InternalQuery: "CREATE VIEW x AS SELECT 1", Position: 42}
	message := SQLViewDependencyErrorMessage(fmt.Errorf("恢复依赖视图失败: %w", cause))
	for _, want := range []string{"恢复依赖视图失败", "out of shared memory", "SQLSTATE: 53200", "DETAIL: cannot acquire", "HINT: You might need to increase max_locks_per_transaction", "COLUMN: amount", "TABLE: t", "CONTEXT: SQL statement", "INTERNAL QUERY: CREATE VIEW", "SQL 字符位置: 42"} {
		if !strings.Contains(message, want) {
			t.Fatalf("missing %q in %s", want, message)
		}
	}
	if SQLViewDependencyErrorMessage(nil) != "" {
		t.Fatal("nil error formatted as failure")
	}
}
