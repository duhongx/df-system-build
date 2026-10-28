package opsprobe

import (
	"context"
	"fmt"
)

type ConstraintInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Definition string `json:"definition"`
	Validated  bool   `json:"validated"`
	Deferrable bool   `json:"deferrable"`
}
type TriggerInfo struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Enabled    string `json:"enabled"`
}
type SequenceInfo struct {
	Type      string  `json:"type"`
	Start     string  `json:"start"`
	Min       string  `json:"min"`
	Max       string  `json:"max"`
	Increment string  `json:"increment"`
	Cycle     bool    `json:"cycle"`
	Cache     string  `json:"cache"`
	Last      *string `json:"last"`
}
type RoutineInfo struct {
	OID       int64  `json:"oid"`
	Schema    string `json:"schema"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
	Comment   string `json:"comment"`
}
type RoutineDetail struct {
	Routine         RoutineInfo `json:"routine"`
	Definition      string      `json:"definition"`
	Language        string      `json:"language"`
	Owner           string      `json:"owner"`
	SecurityDefiner bool        `json:"securityDefiner"`
	Volatility      string      `json:"volatility"`
}

func Routines(ctx context.Context, c DatabaseConfig, schema string) ([]RoutineInfo, error) {
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	rows, err := conn.Query(ctx, `SELECT p.oid::bigint,n.nspname,p.proname,p.prokind::text,pg_get_function_identity_arguments(p.oid),COALESCE(pg_get_function_result(p.oid),''),COALESCE(obj_description(p.oid,'pg_proc'),'') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.prokind IN ('f','p') AND has_schema_privilege(n.oid,'USAGE') ORDER BY p.proname,pg_get_function_identity_arguments(p.oid)`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoutineInfo{}
	for rows.Next() {
		var x RoutineInfo
		if err = rows.Scan(&x.OID, &x.Schema, &x.Name, &x.Kind, &x.Arguments, &x.Result, &x.Comment); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func DescribeRoutine(ctx context.Context, c DatabaseConfig, oid int64) (*RoutineDetail, error) {
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	out := &RoutineDetail{}
	err = conn.QueryRow(ctx, `SELECT p.oid::bigint,n.nspname,p.proname,p.prokind::text,pg_get_function_identity_arguments(p.oid),COALESCE(pg_get_function_result(p.oid),''),COALESCE(obj_description(p.oid,'pg_proc'),''),pg_get_functiondef(p.oid),l.lanname,pg_get_userbyid(p.proowner),p.prosecdef,p.provolatile::text FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace JOIN pg_language l ON l.oid=p.prolang WHERE p.oid=$1::oid AND p.prokind IN ('f','p') AND has_schema_privilege(n.oid,'USAGE')`, oid).Scan(&out.Routine.OID, &out.Routine.Schema, &out.Routine.Name, &out.Routine.Kind, &out.Routine.Arguments, &out.Routine.Result, &out.Routine.Comment, &out.Definition, &out.Language, &out.Owner, &out.SecurityDefiner, &out.Volatility)
	if err != nil {
		return nil, fmt.Errorf("函数或过程不存在、已变化或不可见: %w", err)
	}
	return out, nil
}
