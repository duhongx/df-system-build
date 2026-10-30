package opsprobe

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
	"github.com/jackc/pgx/v5"
)

func pgMonitorFailure(code, name string, err error) inspection.Observation {
	return inspection.Observation{Code: code, Name: name, Status: "UNKNOWN", Reason: inspection.Redact(err.Error()), CollectedAt: time.Now()}
}

func InspectionPostgreSQLMonitoring(parent context.Context, c DatabaseConfig) []inspection.Observation {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return []inspection.Observation{pgMonitorFailure("postgresql-slots", "复制槽与 WAL 保留", err), pgMonitorFailure("postgresql-table-health", "表空间与 VACUUM", err)}
	}
	defer conn.Close(context.WithoutCancel(ctx))
	return []inspection.Observation{postgresSlots(ctx, conn), postgresTableHealth(ctx, conn)}
}

const postgresSlotsSQL = `WITH wal_position AS (
 SELECT CASE WHEN pg_is_in_recovery() THEN GREATEST(pg_last_wal_receive_lsn(),pg_last_wal_replay_lsn()) ELSE pg_current_wal_lsn() END AS lsn
), slots AS (
 SELECT s.slot_name,s.slot_type,COALESCE(s.database,'') AS database,s.active,s.active_pid,
 COALESCE(s.restart_lsn::text,'') AS restart_lsn,COALESCE(s.confirmed_flush_lsn::text,'') AS confirmed_flush_lsn,
 COALESCE(s.wal_status,'') AS wal_status,s.safe_wal_size::float8 AS safe_bytes,
 CASE WHEN p.lsn IS NOT NULL AND s.restart_lsn IS NOT NULL THEN GREATEST(0,pg_wal_lsn_diff(p.lsn,s.restart_lsn))::float8 END AS retained_bytes,
 CASE WHEN s.slot_type='logical' AND s.confirmed_flush_lsn IS NOT NULL AND p.lsn IS NOT NULL THEN GREATEST(0,pg_wal_lsn_diff(p.lsn,s.confirmed_flush_lsn))::float8 END AS confirm_lag
 FROM pg_replication_slots s CROSS JOIN wal_position p
)
SELECT slot_name,slot_type,database,active,active_pid,restart_lsn,confirmed_flush_lsn,wal_status,safe_bytes,retained_bytes,confirm_lag,
 count(*) OVER(),sum(CASE WHEN active THEN 0 ELSE 1 END) OVER(),max(retained_bytes) OVER(),
 sum(CASE WHEN wal_status='lost' THEN 1 ELSE 0 END) OVER(),sum(CASE WHEN wal_status='unreserved' THEN 1 ELSE 0 END) OVER()
FROM slots ORDER BY (wal_status IN ('lost','unreserved')) DESC,retained_bytes DESC NULLS LAST,slot_name LIMIT 100`

func postgresSlots(ctx context.Context, conn *pgx.Conn) inspection.Observation {
	fail := func(err error) inspection.Observation {
		return pgMonitorFailure("postgresql-slots", "复制槽与 WAL 保留", err)
	}
	rows, err := conn.Query(ctx, postgresSlotsSQL)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	o := inspection.Observation{Code: "postgresql-slots", Name: "复制槽与 WAL 保留", Status: "NORMAL", CollectedAt: time.Now(), Measurements: map[string]float64{"slots": 0, "inactiveSlots": 0, "unsafeSlots": 0}}
	t := inspection.Table{Columns: []string{"Slot", "类型", "数据库", "活跃", "PID", "保留 WAL MiB", "确认延迟 MiB", "WAL 状态", "安全余量 MiB", "restart LSN", "confirmed LSN"}}
	var total int64
	for rows.Next() {
		var name, kind, database, restart, confirmed, wal string
		var active bool
		var pid *int
		var safe, retained, lag, maximum *float64
		var inactive, lost, unsafe int64
		if err := rows.Scan(&name, &kind, &database, &active, &pid, &restart, &confirmed, &wal, &safe, &retained, &lag, &total, &inactive, &maximum, &lost, &unsafe); err != nil {
			return fail(err)
		}
		o.Measurements["slots"], o.Measurements["inactiveSlots"], o.Measurements["unsafeSlots"] = float64(total), float64(inactive), float64(lost+unsafe)
		if maximum != nil {
			o.Measurements["maxSlotRetainedBytes"] = *maximum
			if *maximum >= 1<<30 {
				o.Status = "WARNING"
				o.Reason = "复制槽保留 WAL 达到 1 GiB，需核对消费进度与磁盘空间"
			}
		}
		if unsafe > 0 {
			o.Status = "WARNING"
			o.Reason = "存在 unreserved 复制槽，所需 WAL 有丢失风险"
		}
		if lost > 0 {
			o.Status = "CRITICAL"
			o.Reason = "存在 lost 复制槽，所需 WAL 已丢失"
		}
		pidText := "—"
		if pid != nil {
			pidText = strconv.Itoa(*pid)
		}
		state := "否"
		if active {
			state = "是"
		}
		t.Rows = append(t.Rows, []string{name, kind, emptyDash(database), state, pidText, pgMiB(retained), pgMiB(lag), emptyDash(wal), pgMiB(safe), emptyDash(restart), emptyDash(confirmed)})
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	if total == 0 {
		o.Value = "当前实例没有复制槽"
		o.Measurements["maxSlotRetainedBytes"] = 0
	} else {
		o.Value = fmt.Sprintf("复制槽 %d 个，未活跃 %.0f 个", total, o.Measurements["inactiveSlots"])
	}
	if total > int64(len(t.Rows)) {
		t.Note = fmt.Sprintf("共 %d 个复制槽，显示风险或 WAL 保留最多的 %d 个", total, len(t.Rows))
	}
	o.Tables = []inspection.Table{t}
	return o
}

func postgresTableHealth(ctx context.Context, conn *pgx.Conn) inspection.Observation {
	fail := func(err error) inspection.Observation {
		return pgMonitorFailure("postgresql-table-health", "表空间与 VACUUM", err)
	}
	var enabled bool
	if err := conn.QueryRow(ctx, `SELECT current_setting('track_counts')='on'`).Scan(&enabled); err != nil {
		return fail(err)
	}
	if !enabled {
		return fail(fmt.Errorf("track_counts 未开启，表统计不可用"))
	}
	rows, err := conn.Query(ctx, `SELECT schemaname,relname,n_live_tup,n_dead_tup,pg_relation_size(relid),pg_indexes_size(relid),GREATEST(last_vacuum,last_autovacuum),GREATEST(last_analyze,last_autoanalyze),count(*) OVER() FROM pg_stat_user_tables ORDER BY n_dead_tup DESC,schemaname,relname LIMIT 50`)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	o := inspection.Observation{Code: "postgresql-table-health", Name: "表空间与 VACUUM", Status: "RECORDED", CollectedAt: time.Now()}
	t := inspection.Table{Columns: []string{"表名", "表大小 MiB", "索引 MiB", "活元组估计", "死元组估计", "死元组占比 %", "最近 VACUUM", "最近 ANALYZE"}, Note: "元组数为统计估计，死元组占比不等同于磁盘膨胀率；显示死元组最多的 50 张表。"}
	for rows.Next() {
		var schema, name string
		var live, dead, tableBytes, indexBytes, total int64
		var vacuum, analyze *time.Time
		if err := rows.Scan(&schema, &name, &live, &dead, &tableBytes, &indexBytes, &vacuum, &analyze, &total); err != nil {
			return fail(err)
		}
		ratio := 0.0
		if live+dead > 0 {
			ratio = 100 * float64(dead) / float64(live+dead)
		}
		if dead >= 10000 && ratio >= 20 {
			o.Status = "WARNING"
			o.Reason = "存在死元组不少于 10000 且占比达到 20% 的表，需核对 VACUUM 情况"
		}
		t.Rows = append(t.Rows, []string{schema + "." + name, fmt.Sprintf("%.2f", float64(tableBytes)/(1<<20)), fmt.Sprintf("%.2f", float64(indexBytes)/(1<<20)), strconv.FormatInt(live, 10), strconv.FormatInt(dead, 10), fmt.Sprintf("%.1f", ratio), pgTime(vacuum), pgTime(analyze)})
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	if len(t.Rows) == 0 {
		o.Value = "当前数据库暂无用户表"
	}
	o.Tables = []inspection.Table{t}
	return o
}

func PostgreSQLSlowQueryTable(d *Diagnostic) inspection.Table {
	t := inspection.Table{Columns: []string{"PID", "用户", "客户端", "耗时秒（含等待）", "等待事件", "SQL 摘要"}}
	var activities []Activity
	for _, a := range d.Activities {
		if a.State == "active" && a.ElapsedSeconds >= 5 {
			activities = append(activities, a)
		}
	}
	sort.Slice(activities, func(i, j int) bool { return activities[i].ElapsedSeconds > activities[j].ElapsedSeconds })
	for i, a := range activities {
		if i >= 50 {
			break
		}
		t.Rows = append(t.Rows, []string{strconv.Itoa(a.PID), a.Username, a.Client, fmt.Sprintf("%.2f", a.ElapsedSeconds), strings.TrimSpace(a.WaitEventType + " " + a.WaitEvent), inspection.Preview(a.Query, 1000)})
	}
	if len(activities) > 50 {
		t.Note = fmt.Sprintf("当前共 %d 条，显示耗时最长的 50 条", len(activities))
	}
	return t
}
func pgMiB(value *float64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *value/(1<<20))
}
func emptyDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}
func pgTime(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return value.Format(time.RFC3339)
}
