package opsprobe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"df-build-server/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestDiagnosticThreeLevelChainBeyondOldLimit(t *testing.T) {
	dsn, ok := testutil.PostgresDSNFromEnv()
	if !ok {
		t.Skip("PostgreSQL integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connect := func() *pgx.Conn {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close(context.Background()) })
		return conn
	}
	admin := connect()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	table := pgx.Identifier{fmt.Sprintf("chain_fixture_%d", admin.PgConn().PID())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE TABLE "+table+"(id int primary key,v int);INSERT INTO "+table+" VALUES(1,0),(2,0)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })
	// The old LIMIT 301 could drop idle root blockers. Include enough real idle sessions to catch that regression.
	var max int
	if err := admin.QueryRow(ctx, "SELECT current_setting('max_connections')::int").Scan(&max); err != nil {
		t.Fatal(err)
	}
	decoys := 0
	t.Logf("server connection limit %d", max)
	if max >= 340 {
		decoys = 305
	}
	for i := 0; i < decoys; i++ {
		connect()
	}
	a, b, c := connect(), connect(), connect()
	if _, err = a.Exec(ctx, "BEGIN;UPDATE "+table+" SET v=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Exec(ctx, "BEGIN;UPDATE "+table+" SET v=2 WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	bctx, bcancel := context.WithCancel(ctx)
	bdone := make(chan error, 1)
	go func() { _, e := b.Exec(bctx, "UPDATE "+table+" SET v=2 WHERE id=1"); bdone <- e }()
	defer func() {
		bcancel()
		<-bdone
		b.Exec(context.Background(), "ROLLBACK")
		a.Exec(context.Background(), "ROLLBACK")
	}()
	waitFor := func(conn *pgx.Conn) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			var pids []int
			if err = admin.QueryRow(ctx, "SELECT pg_blocking_pids($1)", conn.PgConn().PID()).Scan(&pids); err != nil {
				t.Fatal(err)
			}
			if len(pids) > 0 {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("fixture did not block")
	}
	waitFor(b)
	cctx, ccancel := context.WithCancel(ctx)
	cdone := make(chan error, 1)
	go func() { _, e := c.Exec(cctx, "UPDATE "+table+" SET v=3 WHERE id=2"); cdone <- e }()
	defer func() { ccancel(); <-cdone }()
	waitFor(c)
	probe := DatabaseConfig{Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, Password: cfg.Password, SSLMode: "disable"}
	snapshot, err := Diagnose(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Truncated || len(snapshot.Activities) < decoys+4 {
		t.Fatalf("truncated real sessions: %d", len(snapshot.Activities))
	}
	found := false
	for _, g := range snapshot.BlockingGroups {
		if g.RootPID == int(a.PgConn().PID()) && len(g.WaitingPIDs) == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing root chain %+v", snapshot.BlockingGroups)
	}
	for _, activity := range snapshot.Activities {
		if activity.PID == int(c.PgConn().PID()) && (len(activity.RootBlockingPIDs) != 1 || activity.RootBlockingPIDs[0] != int(a.PgConn().PID()) || activity.LockWaitSeconds == nil) {
			t.Fatalf("wrong waiting evidence %+v", activity)
		}
	}
}

func TestDiagnosticRestrictedRoleReturnsCapabilityWarning(t *testing.T) {
	dsn, ok := testutil.PostgresDSNFromEnv()
	if !ok {
		t.Skip("PostgreSQL integration")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	role := fmt.Sprintf("workbench_restricted_%d", admin.PgConn().PID())
	q := pgx.Identifier{role}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE ROLE "+q+" LOGIN"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP ROLE "+q)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Diagnose(ctx, DatabaseConfig{Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: role, SSLMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	if data.StatsVisible || len(data.Warnings) == 0 {
		t.Fatalf("permission reported as healthy %+v", data)
	}
}
