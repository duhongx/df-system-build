package service

import (
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"strings"
)

// Merge only adjacent changes with independent, simple conversions. In particular,
// USING expressions that read another column must observe the original statement order.
func mergeViewAlterStatements(statements []string) []string {
	var result []string
	var group *pg_query.ParseResult
	var original []string
	columns := map[string]bool{}
	flush := func() {
		if group == nil {
			return
		}
		if len(original) == 1 {
			result = append(result, original[0])
		} else if text, err := pg_query.Deparse(group); err == nil {
			result = append(result, strings.TrimSuffix(strings.TrimSpace(text), ";")+";")
		} else {
			result = append(result, original...)
		}
		group, original, columns = nil, nil, map[string]bool{}
	}
	for _, text := range statements {
		tree, err := pg_query.Parse(text)
		if err != nil || len(tree.GetStmts()) != 1 {
			flush()
			result = append(result, text)
			continue
		}
		stmt := tree.GetStmts()[0].GetStmt().GetAlterTableStmt()
		if stmt == nil || len(stmt.GetCmds()) != 1 {
			flush()
			result = append(result, text)
			continue
		}
		cmd := stmt.GetCmds()[0].GetAlterTableCmd()
		if cmd == nil || cmd.GetSubtype() != pg_query.AlterTableType_AT_AlterColumnType || !simpleViewAlterConversion(cmd) {
			flush()
			result = append(result, text)
			continue
		}
		if group != nil {
			first := group.GetStmts()[0].GetStmt().GetAlterTableStmt()
			a, b := first.GetRelation(), stmt.GetRelation()
			// Do not fold quoted identifiers, change ONLY/inheritance semantics, or reorder tables.
			if a.GetSchemaname() != b.GetSchemaname() || a.GetRelname() != b.GetRelname() || a.GetInh() != b.GetInh() || first.GetMissingOk() != stmt.GetMissingOk() || first.GetObjtype() != stmt.GetObjtype() || columns[cmd.GetName()] {
				flush()
			}
		}
		if group == nil {
			group = proto.Clone(tree).(*pg_query.ParseResult)
		} else {
			target := group.GetStmts()[0].GetStmt().GetAlterTableStmt()
			target.Cmds = append(target.Cmds, proto.Clone(stmt.GetCmds()[0]).(*pg_query.Node))
		}
		original = append(original, text)
		columns[cmd.GetName()] = true
	}
	flush()
	return result
}

func simpleViewAlterConversion(cmd *pg_query.AlterTableCmd) bool {
	def := cmd.GetDef().GetColumnDef()
	if def == nil {
		return false
	}
	node := def.GetRawDefault()
	if node == nil {
		return true
	}
	for node.GetTypeCast() != nil {
		node = node.GetTypeCast().GetArg()
	}
	ref := node.GetColumnRef()
	return ref != nil && len(ref.GetFields()) == 1 && ref.GetFields()[0].GetString_() != nil && ref.GetFields()[0].GetString_().GetSval() == cmd.GetName()
}
