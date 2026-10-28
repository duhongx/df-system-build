package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/crypto"
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

type accessFixture struct {
	svc            *OperationsService
	conn           *pgx.Conn
	row            model.OperationsDatabase
	cfg            *pgx.ConnConfig
	ctx            context.Context
	roles, schemas []string
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	db := operationsTestDB(t)
	dsn, _ := testutil.PostgresDSNFromEnv()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	password, err := crypto.Encrypt(cfg.Password)
	if err != nil {
		t.Fatal(err)
	}
	row := model.OperationsDatabase{Name: "access integration", Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, PasswordEncrypted: password, SSLMode: "disable", Revision: 1}
	if err = db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	f := &accessFixture{svc: NewOperationsService(db, t.TempDir()), conn: conn, row: row, cfg: cfg, ctx: ctx}
	t.Cleanup(func() {
		for _, schema := range f.schemas {
			_, _ = conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+accessIdentifier(schema)+" CASCADE")
		}
		for _, role := range f.roles {
			_, _ = conn.Exec(ctx, "DROP OWNED BY "+accessIdentifier(role)+" CASCADE")
			_, _ = conn.Exec(ctx, "DROP ROLE IF EXISTS "+accessIdentifier(role))
		}
		db.Delete(&model.DatabaseQueryRecord{}, "database_id=?", row.ID)
		db.Delete(&row)
		conn.Close(ctx)
	})
	return f
}
func (f *accessFixture) exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := f.conn.Exec(f.ctx, sql); err != nil {
		t.Fatalf("fixture SQL failed: %s: %v", sql, err)
	}
}
func (f *accessFixture) schema(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("access_%d", time.Now().UnixNano())
	f.schemas = append(f.schemas, name)
	f.exec(t, "CREATE SCHEMA "+accessIdentifier(name))
	return name
}
func (f *accessFixture) role(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("access_role_%d", time.Now().UnixNano())
	f.roles = append(f.roles, name)
	f.exec(t, "CREATE ROLE "+accessIdentifier(name))
	return name
}
func (f *accessFixture) apply(t *testing.T, req AccessRequest) *AccessResult {
	t.Helper()
	req.Revision = f.row.Revision
	plan, err := f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) > 0 {
		t.Fatalf("unexpected blockers: %v", plan.Blockers)
	}
	req.PreviewHash = plan.Hash
	result, err := f.svc.ApplyDatabaseAccess(f.ctx, f.row.ID, req, "access-test-operator")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "SUCCESS" {
		t.Fatalf("permission operation failed: %+v", result)
	}
	return result
}
func (f *accessFixture) login(t *testing.T, name, password string) *pgx.Conn {
	t.Helper()
	cfg := f.cfg.Copy()
	cfg.User = name
	cfg.Password = password
	cfg.RuntimeParams = map[string]string{"default_transaction_read_only": "off"}
	conn, err := pgx.ConnectConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(f.ctx) })
	return conn
}

func TestDatabaseAccessCreatesUserAndSchemaAndEnforcesRealReadOnlyIncludingFutureObjects(t *testing.T) {
	f := newAccessFixture(t)
	schema := f.schema(t)
	q := accessIdentifier(schema)
	f.exec(t, "CREATE TABLE "+q+".items(id serial PRIMARY KEY,value text); INSERT INTO "+q+".items(value) VALUES('original')")
	user := fmt.Sprintf("access_login_%d", time.Now().UnixNano())
	f.roles = append(f.roles, user)
	password := "测试密码'quoted\\$value-2026"
	req := AccessRequest{Action: "create_role", Role: user, Password: password, Login: true, Mode: "readOnly", Schemas: []string{schema}, Future: true, Creators: []string{f.cfg.User}}
	result := f.apply(t, req)
	conn := f.login(t, user, password)
	var value string
	if err := conn.QueryRow(f.ctx, "SELECT value FROM "+q+".items").Scan(&value); err != nil || value != "original" {
		t.Fatalf("readonly SELECT failed: %q %v", value, err)
	}
	for _, sql := range []string{"INSERT INTO " + q + ".items(value) VALUES('bad')", "UPDATE " + q + ".items SET value='bad'", "DELETE FROM " + q + ".items", "TRUNCATE " + q + ".items", "SELECT nextval('" + schema + ".items_id_seq')", "CREATE TABLE " + q + ".bad(id int)"} {
		if _, err := conn.Exec(f.ctx, sql); err == nil {
			t.Fatalf("readonly login could write with default_transaction_read_only=off: %s", sql)
		}
	}
	f.exec(t, "CREATE TABLE "+q+".future(id serial,value text); INSERT INTO "+q+".future(value) VALUES('future')")
	if err := conn.QueryRow(f.ctx, "SELECT value FROM "+q+".future").Scan(&value); err != nil || value != "future" {
		t.Fatalf("future SELECT failed: %v", err)
	}
	if _, err := conn.Exec(f.ctx, "INSERT INTO "+q+".future(value) VALUES('bad')"); err == nil {
		t.Fatal("future table remained writable")
	}
	var audit model.DatabaseQueryRecord
	if err := f.svc.db.First(&audit, result.RecordID).Error; err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(audit)
	if strings.Contains(string(serialized), password) || strings.Contains(string(serialized), "SCRAM-SHA-256$") || audit.Kind != "access" || audit.Operator != "access-test-operator" || audit.Status != "SUCCESS" {
		t.Fatalf("unsafe or missing permission audit: %+v", audit)
	}
	owned := fmt.Sprintf("access_owned_%d", time.Now().UnixNano())
	f.schemas = append(f.schemas, owned)
	f.apply(t, AccessRequest{Action: "create_schema", Schema: owned, Owner: user})
	if _, err := conn.Exec(f.ctx, "CREATE TABLE "+accessIdentifier(owned)+".own_table(id int)"); err != nil {
		t.Fatalf("new Schema owner cannot create objects: %v", err)
	}
}

func TestDatabaseAccessReadOnlyRevokesTableAndColumnWritesAndReadWriteRestoresDataAccess(t *testing.T) {
	f := newAccessFixture(t)
	schema := f.schema(t)
	q := accessIdentifier(schema)
	role := f.role(t)
	f.exec(t, "CREATE TABLE "+q+".items(value text); INSERT INTO "+q+".items VALUES('old'); GRANT USAGE,CREATE ON SCHEMA "+q+" TO "+accessIdentifier(role)+"; GRANT INSERT(value),UPDATE(value) ON "+q+".items TO "+accessIdentifier(role))
	f.apply(t, AccessRequest{Action: "permissions", Role: role, Mode: "readOnly", Schemas: []string{schema}})
	var columnWrite, create bool
	if err := f.conn.QueryRow(f.ctx, `SELECT has_column_privilege($1,$2,'value','UPDATE'),has_schema_privilege($1,$3,'CREATE')`, role, schema+".items", schema).Scan(&columnWrite, &create); err != nil {
		t.Fatal(err)
	}
	if columnWrite || create {
		t.Fatal("column or Schema CREATE grant survived readonly conversion")
	}
	f.apply(t, AccessRequest{Action: "permissions", Role: role, Mode: "readWrite", Schemas: []string{schema}, Future: true, Creators: []string{f.cfg.User}})
	var write bool
	if err := f.conn.QueryRow(f.ctx, `SELECT has_table_privilege($1,$2,'INSERT') AND has_table_privilege($1,$2,'UPDATE') AND has_table_privilege($1,$2,'DELETE')`, role, schema+".items").Scan(&write); err != nil || !write {
		t.Fatalf("readwrite not actually effective: %v", err)
	}
	privileges, err := f.svc.DatabaseRolePrivileges(f.ctx, f.row.ID, role)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range privileges.Schemas {
		if s.Schema == schema {
			found = s.Writable == 1 && s.Readable == 1 && s.Usage && !s.Create
		}
	}
	if !found {
		t.Fatal("effective privilege view does not reflect actual grants")
	}
}

func TestDatabaseAccessReadonlyBlocksInheritedPublicOwnershipAndSecurityDefinerPaths(t *testing.T) {
	f := newAccessFixture(t)
	schema := f.schema(t)
	q := accessIdentifier(schema)
	role := f.role(t)
	group := f.role(t)
	f.exec(t, "CREATE TABLE "+q+".items(value text); GRANT INSERT ON "+q+".items TO "+accessIdentifier(group)+"; GRANT "+accessIdentifier(group)+" TO "+accessIdentifier(role))
	req := AccessRequest{Revision: 1, Action: "permissions", Role: role, Mode: "readOnly", Schemas: []string{schema}}
	checkBlocked := func(needle string) {
		t.Helper()
		plan, err := f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, req)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(plan.Blockers, ";"), needle) {
			t.Fatalf("unsafe readonly preview omitted %q: %v", needle, plan.Blockers)
		}
		req.PreviewHash = plan.Hash
		if _, err = f.svc.ApplyDatabaseAccess(f.ctx, f.row.ID, req, "tester"); err == nil {
			t.Fatal("blocked plan executed")
		}
	}
	checkBlocked("成员角色允许写入")
	f.exec(t, "REVOKE "+accessIdentifier(group)+" FROM "+accessIdentifier(role))
	outside := f.schema(t)
	f.exec(t, "CREATE FUNCTION "+accessIdentifier(outside)+".unsafe() RETURNS void LANGUAGE SQL SECURITY DEFINER AS 'INSERT INTO "+q+".items VALUES(''bad'')'")
	// A function in an inaccessible Schema cannot currently bypass the policy.
	f.apply(t, req)
	f.exec(t, "GRANT USAGE ON SCHEMA "+accessIdentifier(outside)+" TO "+accessIdentifier(role))
	checkBlocked("高权限函数")
	f.exec(t, "REVOKE "+accessIdentifier(group)+" FROM "+accessIdentifier(role)+"; GRANT INSERT ON "+q+".items TO PUBLIC")
	checkBlocked("PUBLIC 或成员角色允许写入")
	f.exec(t, "REVOKE INSERT ON "+q+".items FROM PUBLIC; ALTER TABLE "+q+".items OWNER TO "+accessIdentifier(role))
	checkBlocked("拥有对象")
	f.exec(t, "ALTER TABLE "+q+".items OWNER TO "+accessIdentifier(f.cfg.User)+"; CREATE FUNCTION "+q+".unsafe() RETURNS void LANGUAGE SQL SECURITY DEFINER AS 'INSERT INTO "+q+".items VALUES(''bad'')'")
	checkBlocked("高权限函数")
	f.exec(t, "DROP FUNCTION "+q+".unsafe()")
	// A NOINHERIT membership still permits SET ROLE on supported older servers.
	f.exec(t, "ALTER ROLE "+accessIdentifier(role)+" NOINHERIT; GRANT "+accessIdentifier(group)+" TO "+accessIdentifier(role))
	checkBlocked("成员角色允许写入")
}

func TestDatabaseAccessPreviewTracksACLChangesAndNoPlaintextPasswordOrInjectedSQL(t *testing.T) {
	f := newAccessFixture(t)
	schema := f.schema(t)
	q := accessIdentifier(schema)
	role := f.role(t)
	f.exec(t, "CREATE TABLE "+q+".items(id int)")
	req := AccessRequest{Revision: 1, Action: "permissions", Role: role, Mode: "readOnly", Schemas: []string{schema}}
	plan, err := f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "GRANT INSERT ON "+q+".items TO "+accessIdentifier(role))
	req.PreviewHash = plan.Hash
	if _, err = f.svc.ApplyDatabaseAccess(f.ctx, f.row.ID, req, "tester"); err == nil || !strings.Contains(err.Error(), "重新预览") {
		t.Fatalf("stale plan executed: %v", err)
	}
	name := fmt.Sprintf("access\";--%d", time.Now().UnixNano())
	f.roles = append(f.roles, name)
	password := "never-store-this-secret-'"
	request := AccessRequest{Revision: 1, Action: "create_role", Role: name, Login: true, Password: password}
	plan, err = f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(plan)
	if strings.Contains(string(data), password) {
		t.Fatal("password leaked in preview")
	}
	f.apply(t, request)
	login := f.login(t, name, password)
	if _, err = login.Exec(f.ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = f.conn.QueryRow(f.ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".items").Scan(&exists); err != nil || !exists {
		t.Fatal("quoted identifier altered an unrelated object")
	}
	request.Password = "reset-secret-2026"
	request.Action = "password"
	f.apply(t, request)
	f.login(t, name, request.Password)
}

func TestDatabaseAccessDefaultPermissionsCannotOverrideGlobalOrPublicWritesSilently(t *testing.T) {
	f := newAccessFixture(t)
	schema := f.schema(t)
	role := f.role(t)
	f.exec(t, "ALTER DEFAULT PRIVILEGES FOR ROLE "+accessIdentifier(f.cfg.User)+" GRANT INSERT ON TABLES TO "+accessIdentifier(role))
	req := AccessRequest{Revision: 1, Action: "permissions", Role: role, Mode: "readOnly", Schemas: []string{schema}, Future: true, Creators: []string{f.cfg.User}}
	plan, err := f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Blockers, ";"), "全部 Schema") {
		t.Fatal("global default grant was presented as removable in a single Schema")
	}
	req.AllSchemas = true
	req.Schemas = nil
	f.apply(t, req)
	f.exec(t, "CREATE TABLE "+accessIdentifier(schema)+".future(id int)")
	var write bool
	if err = f.conn.QueryRow(f.ctx, `SELECT has_table_privilege($1,$2,'INSERT')`, role, schema+".future").Scan(&write); err != nil || write {
		t.Fatalf("global future write remained: %v", err)
	}
}

func TestAccessNameValidationDoesNotTruncateNamesOrAcceptReservedTargets(t *testing.T) {
	for _, name := range []string{"", strings.Repeat("x", 64), strings.Repeat("中", 22), "pg_read_all_data", "PUBLIC", " a", "bad\nname"} {
		if validateAccessName(name) == nil {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
	for _, name := range []string{"业务用户", `quoted"name`, "normal_readonly"} {
		if err := validateAccessName(name); err != nil {
			t.Fatalf("valid quoted identifier rejected: %q", name)
		}
	}
}

func TestDatabaseAccessManagesMembershipAndRefusesMutationWithoutAudit(t *testing.T) {
	f := newAccessFixture(t)
	role, group := f.role(t), f.role(t)
	f.apply(t, AccessRequest{Action: "membership", Role: role, MemberRoles: []string{group}})
	catalog, err := f.svc.DatabaseAccess(f.ctx, f.row.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range catalog.Roles {
		if r.Name == role && len(r.MemberOf) == 1 && r.MemberOf[0] == group {
			found = true
		}
	}
	if !found {
		t.Fatal("role membership did not take effect")
	}
	f.apply(t, AccessRequest{Action: "membership", Role: role, MemberRoles: []string{group}, Remove: true})
	var member bool
	if err = f.conn.QueryRow(f.ctx, `SELECT pg_has_role($1,$2,'MEMBER')`, role, group).Scan(&member); err != nil || member {
		t.Fatalf("membership removal failed: %v", err)
	}
	name := fmt.Sprintf("access_noaudit_%d", time.Now().UnixNano())
	req := AccessRequest{Revision: 1, Action: "create_role", Role: name}
	plan, err := f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	req.PreviewHash = plan.Hash
	callback := "access_test_reject_audit"
	if err = f.svc.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "database_query_records" {
			tx.AddError(fmt.Errorf("injected audit storage failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.svc.db.Callback().Create().Remove(callback)
	if _, err = f.svc.ApplyDatabaseAccess(f.ctx, f.row.ID, req, "tester"); err == nil {
		t.Fatal("role was changed without a saved audit record")
	}
	var exists bool
	if err = f.conn.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, name).Scan(&exists); err != nil || exists {
		t.Fatal("audit failure did not prevent the external mutation")
	}
}

func TestDatabaseAccessRollsBackWhenGrantWarningsLeaveIncompletePermissions(t *testing.T) {
	f := newAccessFixture(t)
	schema, target, manager := f.schema(t), f.role(t), f.role(t)
	q, m := accessIdentifier(schema), accessIdentifier(manager)
	f.exec(t, "CREATE TABLE "+q+".items(id int); ALTER ROLE "+m+" LOGIN PASSWORD 'manager-fixture-password'; GRANT USAGE ON SCHEMA "+q+" TO "+m+" WITH GRANT OPTION; GRANT SELECT ON "+q+".items TO "+m+" WITH GRANT OPTION; GRANT CONNECT ON DATABASE "+accessIdentifier(f.cfg.Database)+" TO "+m+" WITH GRANT OPTION")
	password, err := crypto.Encrypt("manager-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	row := f.row
	row.ID = 0
	row.Username = manager
	row.PasswordEncrypted = password
	if err = f.svc.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	defer f.svc.db.Delete(&row)
	req := AccessRequest{Revision: 1, Action: "permissions", Role: target, Mode: "readWrite", Schemas: []string{schema}}
	plan, err := f.svc.PreviewDatabaseAccess(f.ctx, row.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	req.PreviewHash = plan.Hash
	result, err := f.svc.ApplyDatabaseAccess(f.ctx, row.ID, req, "limited-manager")
	if err != nil || result.Status != "FAILED" || !strings.Contains(result.Message, "核验未通过") {
		t.Fatalf("partial GRANT was presented as a success: %+v %v", result, err)
	}
	var readable bool
	if err = f.conn.QueryRow(f.ctx, `SELECT has_table_privilege($1,$2,'SELECT')`, target, schema+".items").Scan(&readable); err != nil || readable {
		t.Fatal("partial grants were not rolled back")
	}
	f.svc.db.Delete(&model.DatabaseQueryRecord{}, "database_id=?", row.ID)
}

func TestDatabaseAccessHonorsDisabledSetAndInheritOptionsOnModernPostgreSQL(t *testing.T) {
	f := newAccessFixture(t)
	var version int
	if err := f.conn.QueryRow(f.ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 160000 {
		t.Skip("membership SET/INHERIT options require PostgreSQL 16+")
	}
	schema, role, group := f.schema(t), f.role(t), f.role(t)
	f.exec(t, "CREATE TABLE "+accessIdentifier(schema)+".items(id int); GRANT INSERT ON "+accessIdentifier(schema)+".items TO "+accessIdentifier(group)+"; GRANT "+accessIdentifier(group)+" TO "+accessIdentifier(role)+" WITH INHERIT FALSE, SET FALSE")
	f.apply(t, AccessRequest{Action: "permissions", Role: role, Mode: "readOnly", Schemas: []string{schema}})
	// Enabling SET on the same membership must invalidate a previous preview.
	req := AccessRequest{Revision: 1, Action: "permissions", Role: role, Mode: "readOnly", Schemas: []string{schema}}
	plan, err := f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "GRANT "+accessIdentifier(group)+" TO "+accessIdentifier(role)+" WITH SET TRUE")
	req.PreviewHash = plan.Hash
	if _, err = f.svc.ApplyDatabaseAccess(f.ctx, f.row.ID, req, "tester"); err == nil {
		t.Fatal("changed membership options did not invalidate preview")
	}
	plan, err = f.svc.PreviewDatabaseAccess(f.ctx, f.row.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Blockers, ";"), "成员角色允许写入") {
		t.Fatal("SET ROLE path was missed")
	}
}
