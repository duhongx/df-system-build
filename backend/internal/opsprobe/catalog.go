package opsprobe

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type SchemaInfo struct {
	Name    string `json:"name"`
	Objects int    `json:"objects"`
}
type RelationInfo struct {
	OID           int64   `json:"oid"`
	Schema        string  `json:"schema"`
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	EstimatedRows float64 `json:"estimatedRows"`
	Comment       string  `json:"comment"`
}
type ColumnInfo struct {
	PrimaryKey bool   `json:"primaryKey"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	Default    string `json:"default"`
	Comment    string `json:"comment"`
	Position   int    `json:"position"`
}
type IndexInfo struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Primary    bool   `json:"primary"`
	Unique     bool   `json:"unique"`
	Valid      bool   `json:"valid"`
}
type RelationDetail struct {
	Constraints []ConstraintInfo `json:"constraints"`
	Triggers    []TriggerInfo    `json:"triggers"`
	Sequence    *SequenceInfo    `json:"sequence,omitempty"`
	Relation    RelationInfo     `json:"relation"`
	Columns     []ColumnInfo     `json:"columns"`
	Indexes     []IndexInfo      `json:"indexes"`
	Definition  string           `json:"definition"`
}

func Schemas(ctx context.Context, c DatabaseConfig) ([]SchemaInfo, error) {
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	rows, err := conn.Query(ctx, `SELECT n.nspname,count(c.oid)::int FROM pg_namespace n LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relkind IN ('r','p','v','m','f','S') WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND has_schema_privilege(n.oid,'USAGE') GROUP BY n.oid,n.nspname ORDER BY n.nspname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SchemaInfo{}
	for rows.Next() {
		var x SchemaInfo
		if err := rows.Scan(&x.Name, &x.Objects); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func Relations(ctx context.Context, c DatabaseConfig, schema string) ([]RelationInfo, error) {
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	rows, err := conn.Query(ctx, `SELECT c.oid::bigint,n.nspname,c.relname,c.relkind::text,GREATEST(c.reltuples,0)::float8,COALESCE(obj_description(c.oid,'pg_class'),'') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','f','S') AND has_schema_privilege(n.oid,'USAGE') ORDER BY c.relkind,c.relname`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RelationInfo{}
	for rows.Next() {
		var x RelationInfo
		if err := rows.Scan(&x.OID, &x.Schema, &x.Name, &x.Kind, &x.EstimatedRows, &x.Comment); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func DescribeRelation(ctx context.Context, c DatabaseConfig, oid int64) (*RelationDetail, error) {
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	out := &RelationDetail{Columns: []ColumnInfo{}, Indexes: []IndexInfo{}, Constraints: []ConstraintInfo{}, Triggers: []TriggerInfo{}}
	err = conn.QueryRow(ctx, `SELECT c.oid::bigint,n.nspname,c.relname,c.relkind::text,GREATEST(c.reltuples,0)::float8,COALESCE(obj_description(c.oid,'pg_class'),'') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1::oid AND c.relkind IN ('r','p','v','m','f','S') AND has_schema_privilege(n.oid,'USAGE')`, oid).Scan(&out.Relation.OID, &out.Relation.Schema, &out.Relation.Name, &out.Relation.Kind, &out.Relation.EstimatedRows, &out.Relation.Comment)
	if err != nil {
		return nil, fmt.Errorf("对象不存在、已变化或不可见: %w", err)
	}
	if out.Relation.Kind == "S" {
		seq := &SequenceInfo{}
		err = conn.QueryRow(ctx, `SELECT data_type::text,start_value::text,min_value::text,max_value::text,increment_by::text,cycle,cache_size::text,last_value::text FROM pg_sequences WHERE schemaname=$1 AND sequencename=$2`, out.Relation.Schema, out.Relation.Name).Scan(&seq.Type, &seq.Start, &seq.Min, &seq.Max, &seq.Increment, &seq.Cycle, &seq.Cache, &seq.Last)
		out.Sequence = seq
		return out, err
	}
	rows, err := conn.Query(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i WHERE i.indrelid=a.attrelid AND i.indisprimary AND a.attnum=ANY(i.indkey)),a.attname,format_type(a.atttypid,a.atttypmod),NOT a.attnotnull,COALESCE(pg_get_expr(d.adbin,d.adrelid),''),COALESCE(col_description(a.attrelid,a.attnum),''),a.attnum::int FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=$1::oid AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x ColumnInfo
		if err := rows.Scan(&x.PrimaryKey, &x.Name, &x.Type, &x.Nullable, &x.Default, &x.Comment, &x.Position); err != nil {
			rows.Close()
			return nil, err
		}
		out.Columns = append(out.Columns, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = conn.Query(ctx, `SELECT c.relname,pg_get_indexdef(i.indexrelid),i.indisprimary,i.indisunique,i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indrelid=$1::oid ORDER BY i.indisprimary DESC,c.relname`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x IndexInfo
		if err := rows.Scan(&x.Name, &x.Definition, &x.Primary, &x.Unique, &x.Valid); err != nil {
			return nil, err
		}
		out.Indexes = append(out.Indexes, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	rows, err = conn.Query(ctx, `SELECT conname,contype::text,pg_get_constraintdef(oid,true),convalidated,condeferrable FROM pg_constraint WHERE conrelid=$1::oid ORDER BY contype,conname`, oid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x ConstraintInfo
		if err = rows.Scan(&x.Name, &x.Type, &x.Definition, &x.Validated, &x.Deferrable); err != nil {
			rows.Close()
			return nil, err
		}
		out.Constraints = append(out.Constraints, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = conn.Query(ctx, `SELECT tgname,pg_get_triggerdef(oid,true),tgenabled::text FROM pg_trigger WHERE tgrelid=$1::oid AND NOT tgisinternal ORDER BY tgname`, oid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x TriggerInfo
		if err = rows.Scan(&x.Name, &x.Definition, &x.Enabled); err != nil {
			rows.Close()
			return nil, err
		}
		out.Triggers = append(out.Triggers, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if out.Relation.Kind == "v" || out.Relation.Kind == "m" {
		err = conn.QueryRow(ctx, `SELECT pg_get_viewdef($1::oid,true)`, oid).Scan(&out.Definition)
	}
	return out, err
}

func CellText(conn *pgx.Conn, oid uint32, value any) any {
	if value == nil {
		return nil
	}
	if data, err := conn.TypeMap().Encode(oid, pgx.TextFormatCode, value, nil); err == nil {
		return string(data)
	}
	return fmt.Sprint(value)
}
