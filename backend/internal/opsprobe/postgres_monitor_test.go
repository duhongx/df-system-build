package opsprobe

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresMonitoringSlotsHandleNullLSNAndUnsafeWAL(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	// Session-local views exercise real PostgreSQL types and SQL, without
	// creating a replication slot or modifying any shared system catalog.
	_, err = conn.Exec(ctx, `CREATE TEMP VIEW pg_replication_slots AS SELECT 'new-slot'::name AS slot_name,'physical'::text AS slot_type,NULL::name AS database,false AS active,NULL::int AS active_pid,NULL::pg_lsn AS restart_lsn,NULL::pg_lsn AS confirmed_flush_lsn,NULL::text AS wal_status,NULL::bigint AS safe_wal_size`)
	if err != nil {
		t.Fatal(err)
	}
	o := postgresSlots(ctx, conn)
	if o.Status != "NORMAL" || o.Measurements["slots"] != 1 || o.Tables[0].Rows[0][5] != "—" || o.Tables[0].Rows[0][6] != "—" {
		t.Fatalf("null LSN: %+v", o)
	}
	if _, ok := o.Measurements["maxSlotRetainedBytes"]; ok {
		t.Fatal("unknown restart LSN became zero WAL retention")
	}
	_, err = conn.Exec(ctx, `CREATE OR REPLACE TEMP VIEW pg_replication_slots AS SELECT 'lost-slot'::name AS slot_name,'logical'::text AS slot_type,current_database()::name AS database,false AS active,NULL::int AS active_pid,'0/0'::pg_lsn AS restart_lsn,'0/0'::pg_lsn AS confirmed_flush_lsn,'lost'::text AS wal_status,0::bigint AS safe_wal_size`)
	if err != nil {
		t.Fatal(err)
	}
	o = postgresSlots(ctx, conn)
	if o.Status != "CRITICAL" || o.Measurements["unsafeSlots"] != 1 || !strings.Contains(o.Reason, "lost") {
		t.Fatalf("lost slot hidden: %+v", o)
	}
	_, err = conn.Exec(ctx, `CREATE OR REPLACE TEMP VIEW pg_replication_slots AS SELECT 'lost-slot'::name AS slot_name,'logical'::text AS slot_type,current_database()::name AS database,false AS active,NULL::int AS active_pid,'0/0'::pg_lsn AS restart_lsn,'0/0'::pg_lsn AS confirmed_flush_lsn,'unreserved'::text AS wal_status,0::bigint AS safe_wal_size`)
	if err != nil {
		t.Fatal(err)
	}
	o = postgresSlots(ctx, conn)
	if o.Status != "WARNING" {
		t.Fatalf("unsafe WAL hidden: %+v", o)
	}
	_, err = conn.Exec(ctx, `DROP VIEW pg_replication_slots`)
	if err != nil {
		t.Fatal(err)
	}
	o = postgresSlots(ctx, conn)
	if o.Status == "UNKNOWN" {
		t.Fatalf("native slot SQL failed: %+v", o)
	}
	o = postgresTableHealth(ctx, conn)
	if o.Status == "UNKNOWN" {
		t.Fatalf("native table SQL failed: %+v", o)
	}
	if !strings.Contains(o.Tables[0].Note, "不等同于磁盘膨胀率") {
		t.Fatal("tuple estimates misrepresented as physical bloat")
	}
}

func TestPostgresMonitoringSlowQueriesAreActiveBoundedAndRedacted(t *testing.T) {
	d := &Diagnostic{Activities: []Activity{{PID: 1, State: "idle", ElapsedSeconds: 1000, Query: "idle SQL"}, {PID: 2, State: "active", ElapsedSeconds: 4, Query: "fast SQL"}, {PID: 3, State: "active", ElapsedSeconds: 10, Query: "SELECT 1\n" + strings.Repeat("x", 2000)}}}
	table := PostgreSQLSlowQueryTable(d)
	if len(table.Rows) != 1 || table.Rows[0][0] != "3" || len([]rune(table.Rows[0][5])) > 1001 || strings.Contains(table.Rows[0][5], "\n") {
		t.Fatalf("bad slow SQL preview: %+v", table)
	}
	for i := 0; i < 60; i++ {
		d.Activities = append(d.Activities, Activity{PID: i + 10, State: "active", ElapsedSeconds: float64(i + 20), Query: "SELECT pg_sleep(30)"})
	}
	table = PostgreSQLSlowQueryTable(d)
	if len(table.Rows) != 50 || table.Note == "" || table.Rows[0][0] != "69" {
		t.Fatalf("unbounded/unsorted slow SQL: %+v", table)
	}
}
