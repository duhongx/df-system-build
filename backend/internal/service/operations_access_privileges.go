package service

import (
	"context"
	"fmt"

	"df-build-server/internal/opsprobe"
)

type AccessSchemaPrivilege struct {
	Schema      string `json:"schema"`
	Owner       string `json:"owner"`
	Usage       bool   `json:"usage"`
	Create      bool   `json:"create"`
	Tables      int    `json:"tables"`
	Readable    int    `json:"readable"`
	Writable    int    `json:"writable"`
	Sequences   int    `json:"sequences"`
	Advanceable int    `json:"advanceable"`
}
type AccessDefaultPrivilege struct {
	Creator    string `json:"creator"`
	Schema     string `json:"schema"`
	Kind       string `json:"kind"`
	Source     string `json:"source"`
	Privileges string `json:"privileges"`
}
type AccessPrivileges struct {
	Role     string                   `json:"role"`
	Schemas  []AccessSchemaPrivilege  `json:"schemas"`
	Defaults []AccessDefaultPrivilege `json:"defaults"`
}

func (s *OperationsService) DatabaseRolePrivileges(ctx context.Context, id uint, role string) (*AccessPrivileges, error) {
	if role == "" {
		return nil, fmt.Errorf("请选择用户／角色")
	}
	_, cfg, err := s.workbenchConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	conn, err := opsprobe.OpenDatabase(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var exists bool
	if err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("用户／角色不存在")
	}
	out := &AccessPrivileges{Role: role, Schemas: []AccessSchemaPrivilege{}, Defaults: []AccessDefaultPrivilege{}}
	rows, err := conn.Query(ctx, `SELECT n.nspname,pg_get_userbyid(n.nspowner),has_schema_privilege($1,n.oid,'USAGE'),has_schema_privilege($1,n.oid,'CREATE'),
 count(c.oid) FILTER(WHERE c.relkind<>'S')::int,
 count(c.oid) FILTER(WHERE CASE WHEN c.relkind IN ('r','p','v','m','f') THEN has_table_privilege($1,c.oid,'SELECT') ELSE false END)::int,
 count(c.oid) FILTER(WHERE CASE WHEN c.relkind IN ('r','p','v','m','f') THEN (has_table_privilege($1,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR has_any_column_privilege($1,c.oid,'INSERT,UPDATE,REFERENCES')) ELSE false END)::int,
 count(c.oid) FILTER(WHERE c.relkind='S')::int,
 count(c.oid) FILTER(WHERE CASE WHEN c.relkind='S' THEN has_sequence_privilege($1,c.oid,'USAGE,UPDATE') ELSE false END)::int
 FROM pg_namespace n LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relkind IN ('r','p','v','m','f','S')
 WHERE left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema' GROUP BY n.oid,n.nspname,n.nspowner ORDER BY n.nspname`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AccessSchemaPrivilege
		if err = rows.Scan(&item.Schema, &item.Owner, &item.Usage, &item.Create, &item.Tables, &item.Readable, &item.Writable, &item.Sequences, &item.Advanceable); err != nil {
			return nil, err
		}
		out.Schemas = append(out.Schemas, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	rows, err = conn.Query(ctx, `SELECT pg_get_userbyid(d.defaclrole),COALESCE(n.nspname,'全部 Schema'),d.defaclobjtype::text,
 CASE WHEN a.grantee=0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END,string_agg(a.privilege_type,', ' ORDER BY a.privilege_type)
 FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) a
 WHERE CASE WHEN a.grantee=0 THEN true ELSE pg_has_role($1,a.grantee,'MEMBER') END
 GROUP BY d.defaclrole,n.nspname,d.defaclobjtype,a.grantee ORDER BY 1,2,3,4`, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AccessDefaultPrivilege
		if err = rows.Scan(&item.Creator, &item.Schema, &item.Kind, &item.Source, &item.Privileges); err != nil {
			return nil, err
		}
		out.Defaults = append(out.Defaults, item)
	}
	return out, rows.Err()
}
