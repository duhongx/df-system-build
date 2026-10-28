package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/text/secure/precis"
	"gorm.io/gorm/clause"
)

type AccessRole struct {
	Name        string   `json:"name"`
	Login       bool     `json:"login"`
	Superuser   bool     `json:"superuser"`
	CreateDB    bool     `json:"createDB"`
	CreateRole  bool     `json:"createRole"`
	Replication bool     `json:"replication"`
	BypassRLS   bool     `json:"bypassRLS"`
	Inherit     bool     `json:"inherit"`
	Limit       int      `json:"limit"`
	ValidUntil  string   `json:"validUntil"`
	MemberOf    []string `json:"memberOf"`
}
type AccessSchema struct {
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	Objects int    `json:"objects"`
}
type AccessCatalog struct {
	Database        string         `json:"database"`
	Actor           string         `json:"actor"`
	Version         int            `json:"version"`
	Superuser       bool           `json:"superuser"`
	CanCreateRoles  bool           `json:"canCreateRoles"`
	CanCreateSchema bool           `json:"canCreateSchema"`
	Roles           []AccessRole   `json:"roles"`
	Schemas         []AccessSchema `json:"schemas"`
}
type AccessRequest struct {
	Revision    uint     `json:"revision"`
	Action      string   `json:"action"`
	Role        string   `json:"role"`
	Password    string   `json:"password"`
	Login       bool     `json:"login"`
	Schema      string   `json:"schema"`
	Owner       string   `json:"owner"`
	Mode        string   `json:"mode"`
	AllSchemas  bool     `json:"allSchemas"`
	Schemas     []string `json:"schemas"`
	Future      bool     `json:"future"`
	Creators    []string `json:"creators"`
	MemberRoles []string `json:"memberRoles"`
	Remove      bool     `json:"remove"`
	PreviewHash string   `json:"previewHash"`
}
type AccessPlan struct {
	Hash     string   `json:"hash"`
	Target   string   `json:"target"`
	Summary  string   `json:"summary"`
	SQL      []string `json:"sql"`
	Blockers []string `json:"blockers"`
	Notes    []string `json:"notes"`
	Schemas  []string `json:"schemas"`
}
type AccessResult struct {
	RecordID uint   `json:"recordId"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Warning  string `json:"warning"`
}

type accessQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func accessIdentifier(name string) string { return pgx.Identifier{name}.Sanitize() }

func validateAccessName(name string) error {
	if name == "" || !utf8.ValidString(name) || len(name) > 63 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("名称需为有效文本，不能超过 63 字节或包含换行、首尾空格")
	}
	if strings.HasPrefix(strings.ToLower(name), "pg_") || strings.EqualFold(name, "information_schema") || strings.EqualFold(name, "public") {
		return fmt.Errorf("不能创建或修改系统保留角色／Schema")
	}
	return nil
}

func readAccessCatalog(ctx context.Context, db accessQuery) (*AccessCatalog, error) {
	c := &AccessCatalog{Roles: []AccessRole{}, Schemas: []AccessSchema{}}
	err := db.QueryRow(ctx, `SELECT current_database(),current_user,current_setting('server_version_num')::int,
 r.rolsuper,r.rolsuper OR r.rolcreaterole,has_database_privilege(current_database(),'CREATE') FROM pg_roles r WHERE rolname=current_user`).Scan(&c.Database, &c.Actor, &c.Version, &c.Superuser, &c.CanCreateRoles, &c.CanCreateSchema)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT r.rolname,r.rolcanlogin,r.rolsuper,r.rolcreatedb,r.rolcreaterole,r.rolreplication,r.rolbypassrls,r.rolinherit,r.rolconnlimit,COALESCE(r.rolvaliduntil::text,''),
 ARRAY(SELECT DISTINCT p.rolname FROM pg_auth_members m JOIN pg_roles p ON p.oid=m.roleid WHERE m.member=r.oid ORDER BY p.rolname)
 FROM pg_roles r ORDER BY r.rolcanlogin DESC,r.rolname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r AccessRole
		if err = rows.Scan(&r.Name, &r.Login, &r.Superuser, &r.CreateDB, &r.CreateRole, &r.Replication, &r.BypassRLS, &r.Inherit, &r.Limit, &r.ValidUntil, &r.MemberOf); err != nil {
			return nil, err
		}
		c.Roles = append(c.Roles, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	rows, err = db.Query(ctx, `SELECT n.nspname,pg_get_userbyid(n.nspowner),count(c.oid)::int FROM pg_namespace n LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relkind IN ('r','p','v','m','f','S')
 WHERE left(n.nspname,3)<>'pg_' AND n.nspname<>'information_schema' GROUP BY n.oid,n.nspname,n.nspowner ORDER BY n.nspname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s AccessSchema
		if err = rows.Scan(&s.Name, &s.Owner, &s.Objects); err != nil {
			return nil, err
		}
		c.Schemas = append(c.Schemas, s)
	}
	return c, rows.Err()
}

func (s *OperationsService) DatabaseAccess(ctx context.Context, id uint) (*AccessCatalog, error) {
	_, cfg, err := s.workbenchConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	conn, err := opsprobe.OpenDatabase(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	return readAccessCatalog(ctx, conn)
}

func accessMessages(ctx context.Context, db accessQuery, sql string, args ...any) ([]string, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var message string
		if err = rows.Scan(&message); err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

// A verifier, rather than a plaintext password, is sent in CREATE/ALTER ROLE.
// This follows pgx's password normalisation and PostgreSQL's SCRAM format.
func accessPasswordVerifier(password string) (string, error) {
	normalized, err := precis.OpaqueString.String(password)
	if err != nil {
		normalized = password
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2.Key([]byte(normalized), salt, 4096, 32, sha256.New)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("Client Key"))
	stored := sha256.Sum256(mac.Sum(nil))
	mac = hmac.New(sha256.New, key)
	mac.Write([]byte("Server Key"))
	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$4096:" + b64(salt) + "$" + b64(stored[:]) + ":" + b64(mac.Sum(nil)), nil
}

func accessRequestValid(req AccessRequest) error {
	if req.Revision == 0 {
		return fmt.Errorf("缺少连接版本，请刷新连接")
	}
	switch req.Action {
	case "create_role", "permissions", "password", "membership":
		if err := validateAccessName(req.Role); err != nil {
			return err
		}
	case "create_schema":
		if err := validateAccessName(req.Schema); err != nil {
			return err
		}
	default:
		return fmt.Errorf("不支持的用户权限操作")
	}
	if req.Action == "password" || req.Action == "create_role" && req.Login {
		if len(req.Password) < 8 || len(req.Password) > 1024 || !utf8.ValidString(req.Password) || strings.ContainsRune(req.Password, 0) {
			return fmt.Errorf("密码需为 8～1024 字节的有效文本")
		}
	}
	if req.Action != "password" && req.Action != "create_role" && req.Password != "" {
		return fmt.Errorf("此操作不接受密码")
	}
	if req.Mode != "" && req.Mode != "readOnly" && req.Mode != "readWrite" {
		return fmt.Errorf("请选择只读或读写权限")
	}
	if req.Action == "permissions" && req.Mode == "" {
		return fmt.Errorf("请选择权限模式")
	}
	if len(req.Schemas) > 200 || len(req.Creators) > 20 || len(req.MemberRoles) > 50 {
		return fmt.Errorf("单次最多 200 个 Schema、20 个对象创建者、50 个成员角色")
	}
	return nil
}

func buildAccessPlan(ctx context.Context, db accessQuery, req AccessRequest) (*AccessPlan, error) {
	if err := accessRequestValid(req); err != nil {
		return nil, err
	}
	c, err := readAccessCatalog(ctx, db)
	if err != nil {
		return nil, err
	}
	p := &AccessPlan{Target: c.Database + " / " + c.Actor, SQL: []string{}, Blockers: []string{}, Notes: []string{}, Schemas: []string{}}
	roles := map[string]AccessRole{}
	for _, r := range c.Roles {
		roles[r.Name] = r
	}
	schemas := map[string]AccessSchema{}
	for _, s := range c.Schemas {
		schemas[s.Name] = s
	}
	_, roleExists := roles[req.Role]
	if req.Action == "create_role" {
		if !c.CanCreateRoles {
			p.Blockers = append(p.Blockers, "当前连接账号没有创建角色的权限")
		}
		if roleExists {
			p.Blockers = append(p.Blockers, "用户／角色已存在："+req.Role)
		}
		login := "NOLOGIN"
		if req.Login {
			login = "LOGIN"
		}
		command := "CREATE ROLE " + accessIdentifier(req.Role) + " " + login + " NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT"
		if req.Login {
			command += " PASSWORD '[REDACTED]'"
		}
		p.SQL = append(p.SQL, command)
		p.Summary = "创建用户／角色 " + req.Role
		p.Notes = append(p.Notes, "用户和角色属于当前 PostgreSQL 实例，其他数据库也能看到该角色；本次对象授权只作用于当前数据库。")
	} else if req.Action != "create_schema" {
		if !roleExists {
			p.Blockers = append(p.Blockers, "用户／角色不存在："+req.Role)
		}
		if req.Role == c.Actor {
			p.Blockers = append(p.Blockers, "请使用另一个管理连接修改当前连接账号，避免连接或管理权限失效")
		}
		if roles[req.Role].Superuser && !c.Superuser {
			p.Blockers = append(p.Blockers, "仅超级用户可修改超级用户角色")
		}
	}
	if req.Action == "create_schema" {
		if !c.CanCreateSchema {
			p.Blockers = append(p.Blockers, "当前连接账号没有在此数据库创建 Schema 的权限")
		}
		if _, ok := schemas[req.Schema]; ok {
			p.Blockers = append(p.Blockers, "Schema 已存在："+req.Schema)
		}
		if _, ok := roles[req.Owner]; !ok {
			p.Blockers = append(p.Blockers, "Schema 所有者不存在")
		}
		if req.Owner != "" && !c.Superuser {
			var member bool
			err = db.QueryRow(ctx, `SELECT pg_has_role(current_user,$1,'MEMBER')`, req.Owner).Scan(&member)
			if err != nil {
				return nil, err
			}
			if !member {
				p.Blockers = append(p.Blockers, "当前连接账号不能切换到所选所有者角色")
			}
		}
		p.SQL = append(p.SQL, "CREATE SCHEMA "+accessIdentifier(req.Schema)+" AUTHORIZATION "+accessIdentifier(req.Owner))
		p.Summary = "创建 Schema " + req.Schema + "，所有者 " + req.Owner
	} else if req.Action == "password" {
		p.SQL = append(p.SQL, "ALTER ROLE "+accessIdentifier(req.Role)+" PASSWORD '[REDACTED]'")
		p.Summary = "重置用户 " + req.Role + " 的密码"
		p.Notes = append(p.Notes, "密码变更作用于整个实例；已有连接不会因此自动断开，保存过此账号密码的连接需要同步更新。")
	} else if req.Action == "membership" {
		if len(req.MemberRoles) == 0 {
			return nil, fmt.Errorf("请选择成员角色")
		}
		for _, name := range req.MemberRoles {
			if _, ok := roles[name]; !ok {
				return nil, fmt.Errorf("成员角色不存在：%s", name)
			}
			if name == req.Role {
				return nil, fmt.Errorf("不能将角色加入自身")
			}
			command := "GRANT " + accessIdentifier(name) + " TO " + accessIdentifier(req.Role)
			if req.Remove {
				command = "REVOKE " + accessIdentifier(name) + " FROM " + accessIdentifier(req.Role)
			}
			p.SQL = append(p.SQL, command)
		}
		p.Summary = "调整 " + req.Role + " 的角色成员关系"
		p.Notes = append(p.Notes, "角色成员关系影响整个实例，可能改变该用户在其他数据库的有效权限；不会授予 ADMIN OPTION。")
	}
	if req.Action == "permissions" || req.Action == "create_role" && req.Mode != "" {
		if req.AllSchemas {
			for name := range schemas {
				p.Schemas = append(p.Schemas, name)
			}
		} else {
			seen := map[string]bool{}
			for _, name := range req.Schemas {
				if _, ok := schemas[name]; !ok {
					return nil, fmt.Errorf("Schema 不存在：%s", name)
				}
				if !seen[name] {
					p.Schemas = append(p.Schemas, name)
					seen[name] = true
				}
			}
		}
		sort.Strings(p.Schemas)
		if len(p.Schemas) == 0 {
			return nil, fmt.Errorf("请选择授权的 Schema")
		}
		p.Notes = append(p.Notes, "只读／读写针对所选 Schema 的业务对象；未选 Schema、其他数据库、其他账号及 PUBLIC 的权限不会被修改。")
		if req.Mode == "readOnly" {
			p.Summary += "；将 " + req.Role + " 在所选 Schema 的权限设为只读"
			if req.Action == "permissions" {
				p.Summary = strings.TrimPrefix(p.Summary, "；")
			}
			messages, e := accessReadOnlyConflicts(ctx, db, req.Role, p.Schemas, req.AllSchemas)
			if e != nil {
				return nil, e
			}
			p.Blockers = append(p.Blockers, messages...)
			if roleExists {
				// Column-level grants survive table-level REVOKE and must be removed too.
				columns, e := accessMessages(ctx, db, `SELECT 'REVOKE ALL PRIVILEGES ('||string_agg(quote_ident(a.attname),',' ORDER BY a.attnum)||') ON TABLE '||quote_ident(n.nspname)||'.'||quote_ident(c.relname)||' FROM '||quote_ident($1)
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=ANY($2) AND a.attnum>0 AND NOT a.attisdropped AND EXISTS(SELECT 1 FROM aclexplode(a.attacl) x WHERE x.grantee=(SELECT oid FROM pg_roles WHERE rolname=$1)) GROUP BY n.nspname,c.relname ORDER BY n.nspname,c.relname`, req.Role, p.Schemas)
				if e != nil {
					return nil, e
				}
				p.SQL = append(p.SQL, columns...)
			}
			if req.AllSchemas {
				p.SQL = append(p.SQL, "REVOKE CREATE ON DATABASE "+accessIdentifier(c.Database)+" FROM "+accessIdentifier(req.Role))
			}
		} else {
			p.Summary += "；为 " + req.Role + " 授予所选 Schema 的数据读写权限"
			p.Summary = strings.TrimPrefix(p.Summary, "；")
		}
		p.SQL = append(p.SQL, "GRANT CONNECT ON DATABASE "+accessIdentifier(c.Database)+" TO "+accessIdentifier(req.Role))
		for _, schema := range p.Schemas {
			q, r := accessIdentifier(schema), accessIdentifier(req.Role)
			if req.Mode == "readOnly" {
				p.SQL = append(p.SQL, "REVOKE ALL PRIVILEGES ON SCHEMA "+q+" FROM "+r, "REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA "+q+" FROM "+r, "REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA "+q+" FROM "+r)
			}
			p.SQL = append(p.SQL, "GRANT USAGE ON SCHEMA "+q+" TO "+r)
			table, sequence := "SELECT", "SELECT"
			if req.Mode == "readWrite" {
				table = "SELECT, INSERT, UPDATE, DELETE"
				sequence = "USAGE, SELECT, UPDATE"
			}
			p.SQL = append(p.SQL, "GRANT "+table+" ON ALL TABLES IN SCHEMA "+q+" TO "+r, "GRANT "+sequence+" ON ALL SEQUENCES IN SCHEMA "+q+" TO "+r)
		}
		if req.Future {
			if len(req.Creators) == 0 {
				return nil, fmt.Errorf("请指定未来对象的创建者")
			}
			creators := map[string]bool{}
			for _, owner := range req.Creators {
				if req.Mode == "readOnly" && owner == req.Role {
					p.Blockers = append(p.Blockers, "只读目标不能作为未来对象的创建者：对象所有权允许重新授予写权限")
				}
				if _, ok := roles[owner]; !ok {
					return nil, fmt.Errorf("对象创建者不存在：%s", owner)
				}
				if creators[owner] {
					continue
				}
				creators[owner] = true
				if !c.Superuser {
					var member bool
					if err = db.QueryRow(ctx, `SELECT pg_has_role(current_user,$1,'MEMBER')`, owner).Scan(&member); err != nil {
						return nil, err
					}
					if !member {
						p.Blockers = append(p.Blockers, "当前连接账号不能修改对象创建者 "+owner+" 的默认权限")
					}
				}
				if req.Mode == "readOnly" {
					messages, e := accessDefaultConflicts(ctx, db, req.Role, owner, p.Schemas, req.AllSchemas)
					if e != nil {
						return nil, e
					}
					p.Blockers = append(p.Blockers, messages...)
					if req.AllSchemas {
						for _, kind := range []string{"TABLES", "SEQUENCES"} {
							p.SQL = append(p.SQL, "ALTER DEFAULT PRIVILEGES FOR ROLE "+accessIdentifier(owner)+" REVOKE ALL PRIVILEGES ON "+kind+" FROM "+accessIdentifier(req.Role))
						}
					}
				}
				for _, schema := range p.Schemas {
					prefix := "ALTER DEFAULT PRIVILEGES FOR ROLE " + accessIdentifier(owner) + " IN SCHEMA " + accessIdentifier(schema)
					table, sequence := "SELECT", "SELECT"
					if req.Mode == "readWrite" {
						table = "SELECT, INSERT, UPDATE, DELETE"
						sequence = "USAGE, SELECT, UPDATE"
					} else {
						for _, kind := range []string{"TABLES", "SEQUENCES"} {
							p.SQL = append(p.SQL, prefix+" REVOKE ALL PRIVILEGES ON "+kind+" FROM "+accessIdentifier(req.Role))
						}
					}
					p.SQL = append(p.SQL, prefix+" GRANT "+table+" ON TABLES TO "+accessIdentifier(req.Role), prefix+" GRANT "+sequence+" ON SEQUENCES TO "+accessIdentifier(req.Role))
				}
			}
			p.Notes = append(p.Notes, "未来对象授权仅覆盖选定创建者在所选 Schema 新建的表／视图和序列；其他创建者不在本次默认授权范围。")
			p.Notes = append(p.Notes, "本次默认权限的对象创建者："+strings.Join(req.Creators, "、"))
		} else {
			p.Notes = append(p.Notes, "本次只调整现有对象，未修改默认权限；新对象仍按已有默认授权分配权限。")
		}
	}
	if len(p.SQL) > 20000 {
		return nil, fmt.Errorf("本次变更过大，请减少 Schema 或创建者后分批执行")
	}
	// Include current ownership, ACLs and memberships so a changed preview cannot execute silently.
	fingerprint, err := accessFingerprint(ctx, db)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(struct {
		Revision    uint
		Request     AccessRequest
		Catalog     *AccessCatalog
		Plan        *AccessPlan
		Fingerprint string
	}{req.Revision, accessSanitizedRequest(req), c, p, fingerprint})
	sum := sha256.Sum256(data)
	p.Hash = hex.EncodeToString(sum[:])
	return p, nil
}

func accessSanitizedRequest(req AccessRequest) AccessRequest {
	req.Password = ""
	req.PreviewHash = ""
	return req
}

func accessFingerprint(ctx context.Context, db accessQuery) (string, error) {
	var value string
	err := db.QueryRow(ctx, `SELECT md5(concat(
 (SELECT string_agg(oid::text||':'||relowner||':'||COALESCE(relacl::text,''),'|' ORDER BY oid) FROM pg_class WHERE relnamespace IN (SELECT oid FROM pg_namespace WHERE left(nspname,3)<>'pg_' AND nspname<>'information_schema')),
 (SELECT string_agg(attrelid::text||':'||attnum||':'||attacl::text,'|' ORDER BY attrelid,attnum) FROM pg_attribute WHERE attacl IS NOT NULL),
 (SELECT string_agg(oid::text||':'||nspowner||':'||COALESCE(nspacl::text,''),'|' ORDER BY oid) FROM pg_namespace),
 (SELECT string_agg(oid::text||':'||defaclrole||':'||defaclnamespace||':'||defaclobjtype::text||':'||defaclacl::text,'|' ORDER BY oid) FROM pg_default_acl),
 (SELECT string_agg(to_jsonb(m)::text,'|' ORDER BY roleid,member,grantor) FROM pg_auth_members m),
 (SELECT string_agg(oid::text||':'||proowner||':'||COALESCE(proacl::text,''),'|' ORDER BY oid) FROM pg_proc WHERE prosecdef),
 (SELECT datdba::text||':'||COALESCE(datacl::text,'') FROM pg_database WHERE datname=current_database()) ))`).Scan(&value)
	return value, err
}

func accessReadOnlyConflicts(ctx context.Context, db accessQuery, role string, schemas []string, all bool) ([]string, error) {
	return accessMessages(ctx, db, `WITH target AS (SELECT oid FROM pg_roles WHERE rolname=$1), usable AS (SELECT r.oid FROM pg_roles r,target t WHERE CASE WHEN current_setting('server_version_num')::int>=160000 THEN pg_has_role(t.oid,r.oid,'USAGE') OR pg_has_role(t.oid,r.oid,'SET') ELSE pg_has_role(t.oid,r.oid,'MEMBER') END), messages AS (
 SELECT '角色具备管理或写入能力：'||r.rolname AS message FROM pg_roles r,target t WHERE r.oid IN (SELECT oid FROM usable)
 AND (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls OR r.rolname IN ('pg_write_all_data','pg_write_server_files','pg_execute_server_program'))
 UNION ALL SELECT '用户或其成员角色拥有 Schema：'||n.nspname FROM pg_namespace n,target t WHERE n.nspname=ANY($2) AND n.nspowner IN (SELECT oid FROM usable)
 UNION ALL SELECT '用户或其成员角色拥有对象：'||quote_ident(n.nspname)||'.'||quote_ident(c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace,target t
 WHERE n.nspname=ANY($2) AND c.relkind IN ('r','p','v','m','f','S') AND c.relowner IN (SELECT oid FROM usable)
 UNION ALL SELECT '用户或其成员角色拥有当前数据库' FROM pg_database d,target t WHERE d.datname=current_database() AND d.datdba IN (SELECT oid FROM usable)
 UNION ALL SELECT 'PUBLIC 或成员角色允许在 Schema 中创建对象：'||n.nspname FROM pg_namespace n CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) a
 WHERE n.nspname=ANY($2) AND a.privilege_type='CREATE' AND a.grantee IS DISTINCT FROM (SELECT oid FROM target)
 AND CASE WHEN a.grantee=0 THEN true ELSE a.grantee IN (SELECT oid FROM usable) END
 UNION ALL SELECT 'PUBLIC 或成员角色允许创建 Schema' FROM pg_database d CROSS JOIN LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) a
 WHERE $3 AND d.datname=current_database() AND a.privilege_type='CREATE' AND a.grantee IS DISTINCT FROM (SELECT oid FROM target)
 AND CASE WHEN a.grantee=0 THEN true ELSE a.grantee IN (SELECT oid FROM usable) END
 UNION ALL SELECT 'PUBLIC 或成员角色允许写入：'||quote_ident(n.nspname)||'.'||quote_ident(c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a
 WHERE n.nspname=ANY($2) AND c.relkind IN ('r','p','v','m','f','S') AND a.privilege_type IN ('INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER','MAINTAIN','USAGE')
 AND a.grantee IS DISTINCT FROM (SELECT oid FROM target) AND CASE WHEN a.grantee=0 THEN true ELSE a.grantee IN (SELECT oid FROM usable) END
 UNION ALL SELECT 'PUBLIC 或成员角色具有列写权限：'||quote_ident(n.nspname)||'.'||quote_ident(c.relname)||'.'||quote_ident(col.attname)
 FROM pg_attribute col JOIN pg_class c ON c.oid=col.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(col.attacl) a
 WHERE n.nspname=ANY($2) AND a.privilege_type IN ('INSERT','UPDATE','REFERENCES') AND a.grantee IS DISTINCT FROM (SELECT oid FROM target)
 AND CASE WHEN a.grantee=0 THEN true ELSE a.grantee IN (SELECT oid FROM usable) END
 UNION ALL SELECT '可执行高权限函数，需先核实写入能力：'||quote_ident(n.nspname)||'.'||quote_ident(p.proname)||'('||pg_get_function_identity_arguments(p.oid)||')'
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a
 WHERE p.prosecdef AND p.oid>=16384 AND a.privilege_type='EXECUTE'
 AND (n.nspname=ANY($2) OR EXISTS(SELECT 1 FROM usable u WHERE has_schema_privilege(u.oid,n.oid,'USAGE'))
 OR EXISTS(SELECT 1 FROM aclexplode(COALESCE(n.nspacl,acldefault('n',n.nspowner))) sa WHERE sa.grantee=0 AND sa.privilege_type='USAGE'))
 AND CASE WHEN a.grantee=0 THEN true ELSE a.grantee IN (SELECT oid FROM usable) END
 ) SELECT DISTINCT message FROM messages ORDER BY message`, role, schemas, all)
}

func accessDefaultConflicts(ctx context.Context, db accessQuery, role, creator string, schemas []string, all bool) ([]string, error) {
	return accessMessages(ctx, db, `WITH target AS (SELECT oid FROM pg_roles WHERE rolname=$1), usable AS (SELECT r.oid FROM pg_roles r,target t WHERE CASE WHEN current_setting('server_version_num')::int>=160000 THEN pg_has_role(t.oid,r.oid,'USAGE') OR pg_has_role(t.oid,r.oid,'SET') ELSE pg_has_role(t.oid,r.oid,'MEMBER') END)
 SELECT DISTINCT '创建者 '||$2||' 的默认写权限无法在本次范围内移除：'||CASE WHEN d.defaclnamespace=0 THEN '全部 Schema' ELSE n.nspname END||' / '||CASE WHEN a.grantee=0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END
 FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid=d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) a
 WHERE d.defaclrole=(SELECT oid FROM pg_roles WHERE rolname=$2) AND d.defaclobjtype IN ('r','S')
 AND (d.defaclnamespace=0 OR n.nspname=ANY($3)) AND a.privilege_type IN ('INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER','MAINTAIN','USAGE')
 AND CASE WHEN a.grantee=(SELECT oid FROM target) THEN d.defaclnamespace=0 AND NOT $4
 WHEN a.grantee=0 THEN true ELSE a.grantee IN (SELECT oid FROM usable) END ORDER BY 1`, role, creator, schemas, all)
}

// Verify actual effective privileges after GRANT/REVOKE. PostgreSQL can emit
// warnings instead of failing when an actor lacks a grant option.
func verifyAccessPermissions(ctx context.Context, db accessQuery, req AccessRequest, schemas []string) error {
	writes := "INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER"
	var version int
	if err := db.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		return err
	}
	if version >= 170000 {
		writes += ",MAINTAIN"
	}
	var missing bool
	err := db.QueryRow(ctx, `SELECT NOT has_database_privilege($1,current_database(),'CONNECT') OR EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=ANY($2) AND NOT has_schema_privilege($1,oid,'USAGE'))
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($2) AND CASE WHEN c.relkind IN ('r','p','v','m','f') THEN NOT has_table_privilege($1,c.oid,'SELECT') ELSE false END)
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($2) AND CASE WHEN c.relkind='S' THEN NOT has_sequence_privilege($1,c.oid,'SELECT') ELSE false END)`, req.Role, schemas).Scan(&missing)
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("授权后核验未通过：部分对象仍无读取权限，所有变更已回滚")
	}
	if req.Mode == "readOnly" {
		messages, e := accessReadOnlyConflicts(ctx, db, req.Role, schemas, req.AllSchemas)
		if e != nil {
			return e
		}
		if len(messages) > 0 {
			return fmt.Errorf("只读核验未通过：%s", strings.Join(messages, "；"))
		}
		err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=ANY($2) AND has_schema_privilege($1,oid,'CREATE'))
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($2) AND CASE WHEN c.relkind IN ('r','p','v','m','f') THEN (has_table_privilege($1,c.oid,$3) OR has_any_column_privilege($1,c.oid,'INSERT,UPDATE,REFERENCES')) ELSE false END)
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($2) AND CASE WHEN c.relkind='S' THEN has_sequence_privilege($1,c.oid,'USAGE,UPDATE') ELSE false END)
 OR ($4 AND has_database_privilege($1,current_database(),'CREATE'))`, req.Role, schemas, writes, req.AllSchemas).Scan(&missing)
		if err != nil {
			return err
		}
		if missing {
			return fmt.Errorf("只读核验未通过：仍存在写入或创建权限，所有变更已回滚")
		}
	} else {
		err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($2) AND CASE WHEN c.relkind IN ('r','p','v','m','f') THEN (NOT has_table_privilege($1,c.oid,'INSERT') OR NOT has_table_privilege($1,c.oid,'UPDATE') OR NOT has_table_privilege($1,c.oid,'DELETE')) ELSE false END)
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($2) AND CASE WHEN c.relkind='S' THEN (NOT has_sequence_privilege($1,c.oid,'USAGE') OR NOT has_sequence_privilege($1,c.oid,'UPDATE')) ELSE false END)`, req.Role, schemas).Scan(&missing)
		if err != nil {
			return err
		}
		if missing {
			return fmt.Errorf("授权后核验未通过：部分对象仍无读写权限，所有变更已回滚")
		}
	}
	return nil
}

func (s *OperationsService) PreviewDatabaseAccess(ctx context.Context, id uint, req AccessRequest) (*AccessPlan, error) {
	row, cfg, err := s.workbenchConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Revision != row.Revision {
		return nil, fmt.Errorf("连接配置已变更，请刷新后重新预览")
	}
	conn, err := opsprobe.OpenDatabase(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	return buildAccessPlan(ctx, tx, req)
}

func (s *OperationsService) ApplyDatabaseAccess(ctx context.Context, id uint, req AccessRequest, operator string) (result *AccessResult, err error) {
	if err = accessRequestValid(req); err != nil {
		return nil, err
	}
	if req.PreviewHash == "" {
		return nil, fmt.Errorf("请先预览本次变更")
	}
	// Hold the registered connection stable while making an external change.
	guard := s.db.WithContext(ctx).Begin()
	if guard.Error != nil {
		return nil, guard.Error
	}
	defer guard.Rollback()
	var row model.OperationsDatabase
	if err = guard.Clauses(clause.Locking{Strength: "SHARE"}).First(&row, id).Error; err != nil {
		return nil, err
	}
	if req.Revision != row.Revision {
		return nil, fmt.Errorf("连接配置已变更，请刷新后重新预览")
	}
	cfg, err := s.databaseConfig(row)
	if err != nil {
		return nil, err
	}
	conn, err := opsprobe.OpenDatabase(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT set_config('statement_timeout','15000',true),set_config('lock_timeout','2000',true),set_config('application_name','dfops-access-management',true)`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('dfops-access-management'),hashtext($1))`, req.Role+":"+req.Schema); err != nil {
		return nil, err
	}
	plan, err := buildAccessPlan(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	if plan.Hash != req.PreviewHash {
		return nil, fmt.Errorf("连接、对象或权限已变化，请重新预览后执行")
	}
	if len(plan.Blockers) > 0 {
		return nil, fmt.Errorf("本次变更存在阻断项：%s", strings.Join(plan.Blockers, "；"))
	}
	record := model.DatabaseQueryRecord{DatabaseID: id, Revision: row.Revision, Target: fmt.Sprintf("%s@%s:%d/%s", row.Username, row.Host, row.Port, row.Database), SQL: strings.Join(plan.SQL, ";\n"), ParametersJSON: mustJSON(accessSanitizedRequest(req)), Kind: "access", Status: "RUNNING", Operator: operator, StartedAt: time.Now()}
	if err = s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("操作记录保存失败，未执行权限变更")
	}
	result = &AccessResult{RecordID: record.ID, Status: "FAILED"}
	verifier := ""
	cleanError := func(e error) string {
		message := SQLExecutionErrorMessage(e)
		for _, secret := range []string{req.Password, cfg.Password, verifier} {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[REDACTED]")
			}
		}
		return message
	}
	defer func() {
		if err != nil {
			result.Message = cleanError(err)
			err = nil
		}
		end := time.Now()
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		auditError := result.Message
		if result.Status == "SUCCESS" {
			auditError = ""
		}
		if e := s.db.WithContext(auditCtx).Model(&record).Updates(map[string]any{"status": result.Status, "error": auditError, "finished_at": end, "duration_ms": float64(end.Sub(record.StartedAt).Microseconds()) / 1000}).Error; e != nil {
			result.Warning = "操作结果已返回，但审计状态更新失败，请先核对实际权限再重试"
		}
	}()
	if req.Action == "password" || req.Action == "create_role" && req.Login {
		verifier, err = accessPasswordVerifier(req.Password)
		if err != nil {
			return result, err
		}
	}
	for _, display := range plan.SQL {
		command := display
		if strings.Contains(display, " PASSWORD '[REDACTED]'") {
			command = strings.Replace(display, " PASSWORD '[REDACTED]'", " PASSWORD '"+verifier+"'", 1)
		}
		if _, err = tx.Exec(ctx, command); err != nil {
			return result, err
		}
	}
	if req.Action == "membership" {
		for _, memberRole := range req.MemberRoles {
			var present bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_auth_members WHERE roleid=(SELECT oid FROM pg_roles WHERE rolname=$1) AND member=(SELECT oid FROM pg_roles WHERE rolname=$2))`, memberRole, req.Role).Scan(&present); err != nil {
				return result, err
			}
			if present == req.Remove {
				return result, fmt.Errorf("成员关系核验未通过，所有变更已回滚")
			}
		}
	}
	if req.Action == "permissions" || req.Action == "create_role" && req.Mode != "" {
		if err = verifyAccessPermissions(ctx, tx, req, plan.Schemas); err != nil {
			return result, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		result.Status = "UNKNOWN"
		return result, fmt.Errorf("提交结果待核实，请刷新用户／权限后核对，不要直接重复执行：%s", cleanError(err))
	}
	result.Status = "SUCCESS"
	result.Message = "操作已完成，实际权限核验通过"
	if req.Action != "permissions" && !(req.Action == "create_role" && req.Mode != "") {
		result.Message = "操作已完成"
	}
	return result, nil
}
