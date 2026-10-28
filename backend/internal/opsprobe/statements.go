package opsprobe

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type StatementStat struct {
	QueryID     string  `json:"queryId"`
	Username    string  `json:"username"`
	SQL         string  `json:"sql"`
	Calls       int64   `json:"calls"`
	TotalMs     float64 `json:"totalMs"`
	AverageMs   float64 `json:"averageMs"`
	MaxMs       float64 `json:"maxMs"`
	MinMs       float64 `json:"minMs"`
	Rows        int64   `json:"rows"`
	SharedHits  int64   `json:"sharedHits"`
	SharedReads int64   `json:"sharedReads"`
	TempWrites  int64   `json:"tempWrites"`
	TimeShare   float64 `json:"timeShare"`
}
type StatementReport struct {
	Available      bool            `json:"available"`
	Reason         string          `json:"reason"`
	CheckedAt      time.Time       `json:"checkedAt"`
	StatsReset     *time.Time      `json:"statsReset"`
	Items          []StatementStat `json:"items"`
	TotalTemplates int64           `json:"totalTemplates"`
	TotalCalls     int64           `json:"totalCalls"`
	TotalMs        float64         `json:"totalMs"`
}

func StatementStatistics(ctx context.Context, c DatabaseConfig, order string) (*StatementReport, error) {
	out := &StatementReport{CheckedAt: time.Now(), Items: []StatementStat{}}
	conn, err := OpenDatabase(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var schema string
	err = conn.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'`).Scan(&schema)
	if err == pgx.ErrNoRows {
		out.Reason = "该数据库未启用 pg_stat_statements；可继续查看实时会话和已采集慢日志"
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	view := pgx.Identifier{schema, "pg_stat_statements"}.Sanitize()
	var modern bool
	if err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=$1::regclass AND attname='total_exec_time' AND NOT attisdropped)`, view).Scan(&modern); err != nil {
		return nil, err
	}
	total, mean, max, min := "total_time", "mean_time", "max_time", "min_time"
	if modern {
		total, mean, max, min = "total_exec_time", "mean_exec_time", "max_exec_time", "min_exec_time"
	}
	sort := map[string]string{"total": "total_ms", "average": "average_ms", "max": "max_ms", "calls": "calls", "reads": "shared_reads"}[order]
	if sort == "" {
		sort = "total_ms"
	}
	q := fmt.Sprintf(`WITH stats AS (SELECT COALESCE(queryid::text,'') AS query_id,pg_get_userbyid(userid)::text AS username,COALESCE(query,'') AS sql,calls::bigint,%s::float8 AS total_ms,%s::float8 AS average_ms,%s::float8 AS max_ms,%s::float8 AS min_ms,rows::bigint,shared_blks_hit::bigint AS shared_hits,shared_blks_read::bigint AS shared_reads,temp_blks_written::bigint AS temp_writes FROM %s WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND calls>0) SELECT *,CASE WHEN sum(total_ms) OVER()>0 THEN total_ms*100/sum(total_ms) OVER() ELSE 0 END,count(*) OVER(),sum(calls) OVER()::bigint,sum(total_ms) OVER() FROM stats ORDER BY %s DESC,query_id,username LIMIT 100`, total, mean, max, min, view, sort)
	rows, err := conn.Query(ctx, q)
	if err != nil {
		out.Reason = "累计 SQL 统计不可用：" + redactSecret(err.Error(), c.Password)
		return out, nil
	}
	for rows.Next() {
		var x StatementStat
		if err := rows.Scan(&x.QueryID, &x.Username, &x.SQL, &x.Calls, &x.TotalMs, &x.AverageMs, &x.MaxMs, &x.MinMs, &x.Rows, &x.SharedHits, &x.SharedReads, &x.TempWrites, &x.TimeShare, &out.TotalTemplates, &out.TotalCalls, &out.TotalMs); err != nil {
			rows.Close()
			return nil, err
		}
		out.Items = append(out.Items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out.Available = true
	var statsVisible bool
	if conn.QueryRow(ctx, `SELECT (SELECT rolsuper FROM pg_roles WHERE rolname=current_user) OR pg_has_role(current_user,'pg_read_all_stats','USAGE')`).Scan(&statsVisible) == nil && !statsVisible {
		out.Reason = "当前账号缺少 pg_read_all_stats，其他账号的 SQL 文本与 Query ID 可能不可见"
	}
	info := pgx.Identifier{schema, "pg_stat_statements_info"}.Sanitize()
	var exists bool
	if conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, info).Scan(&exists) == nil && exists {
		_ = conn.QueryRow(ctx, "SELECT stats_reset FROM "+info).Scan(&out.StatsReset)
	}
	if out.StatsReset == nil {
		if out.Reason != "" {
			out.Reason += "；"
		}
		out.Reason += "扩展未提供重置时间，累计统计起点未知；本表不是选定时间段内的慢日志"
	}
	return out, nil
}
