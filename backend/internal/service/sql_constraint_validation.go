package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A duplicate-object error alone is not enough: ALTER can contain several
// actions, and constraint names are unique only within their owning table.
func releaseSQLAddedConstraint(text string) (*pg_query.AlterTableStmt, *pg_query.Constraint) {
	node := releaseSQLNode(text)
	if node == nil {
		return nil, nil
	}
	alter := node.GetAlterTableStmt()
	if alter == nil || alter.Objtype != pg_query.ObjectType_OBJECT_TABLE || alter.Relation == nil || len(alter.Cmds) != 1 {
		return nil, nil
	}
	cmd := alter.Cmds[0].GetAlterTableCmd()
	if cmd == nil || cmd.Subtype != pg_query.AlterTableType_AT_AddConstraint {
		return nil, nil
	}
	constraint := cmd.Def.GetConstraint()
	if constraint == nil || constraint.Conname == "" {
		return nil, nil
	}
	return alter, constraint
}

func releaseSQLRelationName(relation *pg_query.RangeVar) string {
	name := quoteIdent(relation.Relname)
	if relation.Schemaname != "" {
		name = quoteIdent(relation.Schemaname) + "." + name
	}
	if relation.Catalogname != "" {
		name = quoteIdent(relation.Catalogname) + "." + name
	}
	return name
}

// Locations describe source spelling, not the SQL definition. Clear them in
// cloned ASTs only; compare all remaining semantic fields conservatively.
func clearReleaseSQLLocations(message protoreflect.Message) {
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Name() == "location" || field.Name() == "stmt_location" || field.Name() == "stmt_len" {
			message.Clear(field)
		} else if field.IsList() && field.Message() != nil {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				clearReleaseSQLLocations(list.Get(i).Message())
			}
		} else if field.Message() != nil && !field.IsMap() {
			clearReleaseSQLLocations(value.Message())
		}
		return true
	})
}

func normalizeReleaseSQLForeignKey(ctx context.Context, conn *sql.Conn, constraint *pg_query.Constraint) error {
	if constraint.Pktable == nil {
		return fmt.Errorf("外键缺少引用表，无法确认定义")
	}
	var oid int64
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(pg_catalog.to_regclass($1)::bigint, 0)", releaseSQLRelationName(constraint.Pktable)).Scan(&oid); err != nil {
		return err
	}
	if oid == 0 {
		return fmt.Errorf("无法解析外键引用表 %s", releaseSQLRelationName(constraint.Pktable))
	}
	// Compare resolved identities, so search_path and explicit qualification
	// cannot make a different referenced table look equivalent.
	constraint.Pktable = &pg_query.RangeVar{Relname: strconv.FormatInt(oid, 10)}
	if len(constraint.PkAttrs) == 0 {
		var columns string
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(json_agg(a.attname ORDER BY k.ord)::text, '[]')
		 FROM pg_catalog.pg_constraint c
		 CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		 JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		 WHERE c.conrelid = $1::oid AND c.contype = 'p'`, oid).Scan(&columns); err != nil {
			return err
		}
		var names []string
		if err := json.Unmarshal([]byte(columns), &names); err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("引用表没有可确认的主键，无法核实省略引用字段的外键")
		}
		for _, name := range names {
			constraint.PkAttrs = append(constraint.PkAttrs, &pg_query.Node{Node: &pg_query.Node_String_{String_: &pg_query.String{Sval: name}}})
		}
	}
	return nil
}

// Called only after PostgreSQL returned 42710 and (inside BEGIN/COMMIT) after
// ROLLBACK TO SAVEPOINT restored the transaction. Never modifies user objects.
func validateReleaseSQLExistingConstraint(ctx context.Context, conn *sql.Conn, text string) error {
	alter, expected := releaseSQLAddedConstraint(text)
	if expected == nil {
		return fmt.Errorf("当前 SQL 不是单项命名 ADD CONSTRAINT，不能自动跳过")
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	table := releaseSQLRelationName(alter.Relation)
	var definition string
	var validated bool
	err := conn.QueryRowContext(probe, `SELECT pg_catalog.pg_get_constraintdef(c.oid, false), c.convalidated
	 FROM pg_catalog.pg_constraint c
	 WHERE c.conrelid = pg_catalog.to_regclass($1) AND c.conname = $2`, table, expected.Conname).Scan(&definition, &validated)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("数据库报告约束 %s 已存在，但校验时未能在目标表 %s 上确认该约束；请核对 schema 或对象是否发生变化", quoteIdent(expected.Conname), table)
	}
	if err != nil {
		return fmt.Errorf("读取约束 %s 的定义失败: %w", quoteIdent(expected.Conname), err)
	}
	_, actual := releaseSQLAddedConstraint("ALTER TABLE " + table + " ADD CONSTRAINT " + quoteIdent(expected.Conname) + " " + definition)
	if actual == nil {
		return fmt.Errorf("无法解析已有约束 %s 的定义，未自动跳过", quoteIdent(expected.Conname))
	}
	if !expected.SkipValidation && !validated {
		return fmt.Errorf("约束 %s 已存在，但尚未完成数据校验（NOT VALID），不满足当前 SQL 的要求", quoteIdent(expected.Conname))
	}
	want, have := proto.Clone(expected).(*pg_query.Constraint), proto.Clone(actual).(*pg_query.Constraint)
	for _, constraint := range []*pg_query.Constraint{want, have} {
		constraint.Conname = ""
		// Accept an already validated constraint for an incoming NOT VALID
		// clause, but never the reverse (checked above).
		constraint.SkipValidation, constraint.InitiallyValid = false, false
		clearReleaseSQLLocations(constraint.ProtoReflect())
		if constraint.Contype == pg_query.ConstrType_CONSTR_FOREIGN {
			if err := normalizeReleaseSQLForeignKey(probe, conn, constraint); err != nil {
				return fmt.Errorf("核实外键 %s 的引用关系失败: %w", quoteIdent(expected.Conname), err)
			}
		}
	}
	if !proto.Equal(want, have) {
		return fmt.Errorf("约束 %s 已存在于目标表 %s，但已有定义与本次 SQL 不一致或无法确认等价；已有定义：%s", quoteIdent(expected.Conname), table, definition)
	}
	return nil
}

func validateReleaseSQLSkip(ctx context.Context, conn *sql.Conn, text, state string) error {
	switch state {
	case "42P07":
		return validateReleaseSQLIndex(ctx, conn, text)
	case "42710":
		return validateReleaseSQLExistingConstraint(ctx, conn, text)
	}
	return nil
}
