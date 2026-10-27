package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
)

// A view definition conflict also blocks its following metadata steps.
type sqlViewRecoveryConflictError struct{ name string }

func (e sqlViewRecoveryConflictError) Error() string {
	return fmt.Sprintf("恢复冲突：%s 已存在且类型或定义与备份不同，请核实后处理；未覆盖已有对象", e.name)
}

// Recovery can encounter objects recreated before a later step failed. Compare
// their saved definitions before reusing them; never hide conflicts with IF NOT
// EXISTS or overwrite an independently changed view with CREATE OR REPLACE.
func execSQLViewRecovery(ctx context.Context, conn *sql.Conn, text string) error {
	statements, err := pg_query.SplitWithScanner(text, true)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		reuse, err := reusableSQLViewObject(ctx, conn, statement)
		if err != nil {
			return err
		}
		if !reuse {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}

func equalSQLViewNodes(a, b *pg_query.Node) bool {
	if a == nil || b == nil {
		return false
	}
	a = proto.Clone(a).(*pg_query.Node)
	b = proto.Clone(b).(*pg_query.Node)
	clearReleaseSQLLocations(a.ProtoReflect())
	clearReleaseSQLLocations(b.ProtoReflect())
	return proto.Equal(a, b)
}

func reusableSQLViewObject(ctx context.Context, conn *sql.Conn, text string) (bool, error) {
	node := releaseSQLNode(text)
	var relation *pg_query.RangeVar
	var query *pg_query.Node
	var kind, objectName, definition string
	switch {
	case node.GetViewStmt() != nil:
		view := node.GetViewStmt()
		relation, query, kind = view.View, view.Query, "v"
	case node.GetCreateTableAsStmt() != nil && node.GetCreateTableAsStmt().Objtype == pg_query.ObjectType_OBJECT_MATVIEW:
		view := node.GetCreateTableAsStmt()
		relation, query, kind = view.Into.Rel, view.Query, "m"
	case node.GetIndexStmt() != nil:
		index := node.GetIndexStmt()
		relation, objectName, kind = index.Relation, index.Idxname, "index"
	case node.GetRuleStmt() != nil:
		rule := node.GetRuleStmt()
		relation, objectName, kind = rule.Relation, rule.Rulename, "rule"
	case node.GetCreateTrigStmt() != nil:
		trigger := node.GetCreateTrigStmt()
		relation, objectName, kind = trigger.Relation, trigger.Trigname, "trigger"
	default:
		return false, nil
	}
	if relation == nil {
		return false, fmt.Errorf("恢复对象缺少目标名称")
	}
	qualified := releaseSQLRelationName(relation)
	var err error
	switch kind {
	case "v", "m":
		var actualKind string
		err = conn.QueryRowContext(ctx, `SELECT c.relkind::text, CASE WHEN c.relkind IN ('v','m') THEN pg_get_viewdef(c.oid, true) ELSE '' END
   FROM pg_class c WHERE c.oid=to_regclass($1)`, qualified).Scan(&actualKind, &definition)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if actualKind != kind || !equalSQLViewNodes(query, releaseSQLNode(definition)) {
			return false, sqlViewRecoveryConflictError{name: qualified}
		}
		return true, nil
	case "index":
		// Index names share their schema's relation namespace. Also verify that an
		// existing index belongs to this relation and is both valid and ready.
		var valid, sameParent bool
		err = conn.QueryRowContext(ctx, `SELECT CASE WHEN i.indexrelid IS NOT NULL THEN pg_get_indexdef(c.oid) ELSE '' END,
   COALESCE(i.indisvalid AND i.indisready, false), COALESCE(i.indrelid=to_regclass($1),false)
   FROM pg_class c LEFT JOIN pg_index i ON i.indexrelid=c.oid
   WHERE c.relnamespace=(SELECT relnamespace FROM pg_class WHERE oid=to_regclass($1)) AND c.relname=$2`, qualified, objectName).Scan(&definition, &valid, &sameParent)
		if err == nil && (!valid || !sameParent) {
			return false, fmt.Errorf("恢复冲突：索引 %s 已存在但目标表不同或索引无效，请核实后修复", objectName)
		}
	case "rule":
		err = conn.QueryRowContext(ctx, `SELECT pg_get_ruledef(oid, true) FROM pg_rewrite WHERE ev_class=to_regclass($1) AND rulename=$2`, qualified, objectName).Scan(&definition)
	case "trigger":
		err = conn.QueryRowContext(ctx, `SELECT pg_get_triggerdef(oid, true) FROM pg_trigger WHERE tgrelid=to_regclass($1) AND tgname=$2 AND NOT tgisinternal`, qualified, objectName).Scan(&definition)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !equalSQLViewNodes(node, releaseSQLNode(definition)) {
		return false, fmt.Errorf("恢复冲突：%s 的 %s %s 已存在且定义与备份不同，请核实后处理；未覆盖已有对象", qualified, kind, objectName)
	}
	return true, nil
}
