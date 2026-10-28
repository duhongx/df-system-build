package opsprobe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func OpenDatabase(ctx context.Context, c DatabaseConfig) (*pgx.Conn, error) {
	if c.Port < 1 || c.Port > 65535 || strings.TrimSpace(c.Host) == "" || strings.TrimSpace(c.Database) == "" || strings.TrimSpace(c.Username) == "" {
		return nil, fmt.Errorf("数据库连接信息不完整")
	}
	mode := c.SSLMode
	if mode == "" {
		mode = "require"
	}
	switch mode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return nil, fmt.Errorf("不支持的 SSL 模式")
	}
	cfg, err := pgx.ParseConfig("sslmode=" + mode)
	if err != nil {
		return nil, err
	}
	cfg.Host = c.Host
	cfg.Port = uint16(c.Port)
	cfg.Database = c.Database
	cfg.User = c.Username
	cfg.Password = c.Password
	cfg.ConnectTimeout = 5 * time.Second
	cfg.RuntimeParams["application_name"] = "dfops-inspection"
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["statement_timeout"] = "5000"
	cfg.RuntimeParams["lock_timeout"] = "1000"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("连接 %s/%s（%s）失败: %s", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.Database, c.Username, redactSecret(err.Error(), c.Password))
	}
	return conn, nil
}
func redactSecret(text, secret string) string {
	if secret != "" {
		return strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
func Diagnose(ctx context.Context, c DatabaseConfig) (*Diagnostic, error) {
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	return DiagnoseConnection(ctx, conn)
}
func DiagnoseConnection(ctx context.Context, conn *pgx.Conn) (*Diagnostic, error) {
	d := &Diagnostic{CheckedAt: time.Now(), Activities: []Activity{}, Locks: []Lock{}, Warnings: []string{}, Limit: 300}
	var standby, statsVisible bool
	if err := conn.QueryRow(ctx, `SELECT version(),current_database(),current_user,pg_is_in_recovery(),(SELECT rolsuper FROM pg_roles WHERE rolname=current_user) OR pg_has_role(current_user,'pg_read_all_stats','USAGE'),current_setting('max_connections')::int,current_setting('server_version_num')::int,clock_timestamp()`).Scan(&d.Version, &d.Database, &d.Username, &standby, &statsVisible, &d.MaxConnections, &d.ServerVersionNum, &d.CheckedAt); err != nil {
		return nil, err
	}
	d.StatsVisible = statsVisible
	d.Limit = 0
	d.Role = "primary"
	if standby {
		d.Role = "standby"
	}
	if !statsVisible {
		d.Warnings = append(d.Warnings, "当前账号缺少 pg_read_all_stats，其他账号的 SQL 和等待信息可能不可见；空列表不代表无异常")
	}
	if err := conn.QueryRow(ctx, `SELECT numbackends,deadlocks,stats_reset FROM pg_stat_database WHERE datname=current_database()`).Scan(&d.Connections, &d.Deadlocks, &d.StatsReset); err != nil {
		return nil, err
	}
	queryID := "''::text"
	if d.ServerVersionNum >= 140000 {
		queryID = "COALESCE(query_id::text,'')"
	}
	rows, err := conn.Query(ctx, `WITH RECURSIVE live AS MATERIALIZED (
 SELECT pid,COALESCE(datname,'') AS dbname,COALESCE(usename,'') AS username,COALESCE(application_name,'') AS application,COALESCE(client_addr::text,'') AS client,COALESCE(state,'') AS state,COALESCE(query,'') AS query,query_start,xact_start,
 CASE WHEN state='active' THEN GREATEST(0,EXTRACT(EPOCH FROM statement_timestamp()-query_start))::float8 ELSE 0 END AS elapsed,
 COALESCE(GREATEST(0,EXTRACT(EPOCH FROM statement_timestamp()-xact_start))::float8,0) AS xact_seconds,COALESCE(wait_event_type,'') AS wait_type,COALESCE(wait_event,'') AS wait_event,
 CASE WHEN wait_event_type='Lock' THEN pg_blocking_pids(pid) ELSE ARRAY[]::int[] END AS blockers,backend_start,state_change,COALESCE(backend_type,'') AS backend_type,`+queryID+` AS query_id
 FROM pg_stat_activity WHERE pid<>pg_backend_pid()
 ), needed(pid) AS (
 SELECT pid FROM live WHERE dbname=current_database() AND backend_type IN ('client backend','')
 UNION SELECT blocker FROM needed JOIN live ON live.pid=needed.pid CROSS JOIN LATERAL unnest(live.blockers) AS blocker
 )
 SELECT live.* FROM live JOIN needed ON needed.pid=live.pid
 ORDER BY CASE WHEN wait_type='Lock' THEN 0 ELSE 1 END,xact_start NULLS LAST,query_start NULLS LAST,live.pid`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.PID, &a.DBName, &a.Username, &a.Application, &a.Client, &a.State, &a.Query, &a.QueryStart, &a.TransactionStart, &a.ElapsedSeconds, &a.TransactionSeconds, &a.WaitEventType, &a.WaitEvent, &a.BlockingPIDs, &a.BackendStart, &a.StateChange, &a.BackendType, &a.QueryID); err != nil {
			rows.Close()
			return nil, err
		}
		d.Activities = append(d.Activities, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, a := range d.Activities {
		if a.WaitEventType == "Lock" || len(a.BlockingPIDs) > 0 {
			pids = append(pids, a.PID)
			pids = append(pids, a.BlockingPIDs...)
		}
	}
	if len(pids) > 0 {
		waitStart := "NULL::timestamptz"
		waitSeconds := "0::float8"
		if d.ServerVersionNum >= 140000 {
			waitStart = "l.waitstart"
			waitSeconds = "COALESCE(GREATEST(0,EXTRACT(EPOCH FROM statement_timestamp()-l.waitstart))::float8,0)"
		}
		rows, err = conn.Query(ctx, `SELECT COALESCE(l.pid,0),l.locktype,l.mode,l.granted,
  CASE WHEN l.relation IS NOT NULL AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()) THEN COALESCE(quote_ident(n.nspname)||'.'||quote_ident(c.relname),l.relation::text)
  WHEN l.transactionid IS NOT NULL THEN 'transaction '||l.transactionid::text ELSE COALESCE(l.classid::text||':'||l.objid::text,'') END,
  `+waitStart+`,`+waitSeconds+`,COALESCE(l.database,0)::bigint,COALESCE(l.relation,0)::bigint,COALESCE(l.transactionid::text,''),COALESCE(px.gid,'')
  FROM pg_locks l LEFT JOIN pg_class c ON c.oid=l.relation AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()) LEFT JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_prepared_xacts px ON l.pid IS NULL AND px.transaction=l.transactionid
  WHERE l.pid=ANY($1::int[]) OR (l.pid IS NULL AND 0=ANY($1::int[])) ORDER BY l.granted,l.pid,l.locktype`, pids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var l Lock
			if err := rows.Scan(&l.PID, &l.LockType, &l.Mode, &l.Granted, &l.Object, &l.WaitStart, &l.WaitSeconds, &l.DatabaseOID, &l.RelationOID, &l.TransactionID, &l.PreparedGID); err != nil {
				rows.Close()
				return nil, err
			}
			d.Locks = append(d.Locks, l)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	BuildBlockingGraph(d)
	return d, nil
}
func DatabaseChecks(d *Diagnostic) []Check {
	out := []Check{{Code: "database_identity", Name: "数据库身份", Status: "NORMAL", Value: d.Database + " / " + d.Role, Evidence: d.Version}, {Code: "database_deadlocks", Name: "累计死锁次数", Status: "NORMAL", Value: strconv.FormatInt(d.Deadlocks, 10), Evidence: "pg_stat_database.deadlocks，自统计重置起累计；不能代表本次发生次数或完整死锁详情"}}
	for _, a := range d.Activities {
		if a.WaitEventType == "Lock" {
			out = append(out, Check{Code: fmt.Sprintf("lock:%d", a.PID), Name: "锁等待会话", Status: "WARNING", Value: fmt.Sprintf("PID %d；阻塞者 %v", a.PID, a.BlockingPIDs), Evidence: a.Query, Advice: "先核对阻塞源事务、账号和业务用途，再决定处置"})
		}
		if a.State == "active" && a.ElapsedSeconds >= 5 {
			out = append(out, Check{Code: fmt.Sprintf("slow:%d", a.PID), Name: "当前长时间运行 SQL", Status: level(a.ElapsedSeconds, 5, 120), Value: fmt.Sprintf("%.1f 秒（包含等待时间）", a.ElapsedSeconds), Evidence: a.Query, Advice: "结合等待事件和执行计划排查"})
		}
		if a.TransactionSeconds >= 300 {
			out = append(out, Check{Code: fmt.Sprintf("transaction:%d", a.PID), Name: "长事务", Status: level(a.TransactionSeconds, 300, 1800), Value: fmt.Sprintf("PID %d / %s / %.1f 秒", a.PID, a.State, a.TransactionSeconds), Evidence: a.Query})
		}
	}
	if len(out) == 2 {
		out = append(out, Check{Code: "database_activity", Name: "当前锁与 SQL", Status: "NORMAL", Value: "本次快照未发现锁等待或运行超过 5 秒的 SQL；历史事件需结合日志"})
	}
	if d.Truncated {
		out = append(out, Check{Code: "database_limit", Name: "采集范围", Status: "UNKNOWN", Error: "会话或锁数量超过本次明细上限，报告不是完整明细"})
	}
	for _, w := range d.Warnings {
		out = append(out, Check{Code: "database_permission", Name: "采集权限", Status: "UNKNOWN", Error: w})
	}
	return out
}
func LogSettings(ctx context.Context, c DatabaseConfig) (map[string]string, error) {
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	out := map[string]string{}
	rows, err := conn.Query(ctx, `SELECT name,setting FROM pg_settings WHERE name IN ('data_directory','log_directory','log_filename','log_line_prefix','log_destination','logging_collector','log_timezone','log_min_duration_statement','log_lock_waits','track_activity_query_size')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
