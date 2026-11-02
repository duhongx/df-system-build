package service

import (
	"context"
	"df-build-server/internal/inspection"
	"df-build-server/internal/opsprobe"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Aggregate database-scoped observations without losing database attribution or
// letting a failed database overwrite results from the others.
func mergeDatabaseObservations(dst *[]inspection.Observation, database string, source []inspection.Observation) {
	for _, o := range source {
		o.Advice, o.Threshold = "", ""
		if o.Status != "UNKNOWN" && o.Status != "NOT_APPLICABLE" {
			o.Status = "RECORDED"
		}
		if o.Attributes == nil {
			o.Attributes = map[string]string{}
		}
		o.Attributes["scope"] = "database"
		for i := range o.Executions {
			o.Executions[i].Database = database
		}
		for i := range o.Tables {
			o.Tables[i].Columns = append([]string{"数据库"}, o.Tables[i].Columns...)
			for j := range o.Tables[i].Rows {
				o.Tables[i].Rows[j] = append([]string{database}, o.Tables[i].Rows[j]...)
			}
		}
		if o.Status == "UNKNOWN" {
			o.Gaps = append(o.Gaps, database+"："+reportDataReason(o.Reason))
		}
		found := -1
		for i := range *dst {
			if (*dst)[i].Code == o.Code {
				found = i
				break
			}
		}
		if found < 0 {
			o.Value = database + "：" + o.Value
			*dst = append(*dst, o)
			continue
		}
		old := &(*dst)[found]
		if old.Status == "UNKNOWN" && o.Status != "UNKNOWN" {
			old.Status = "RECORDED"
			old.Reason = ""
		}
		old.Gaps = append(old.Gaps, o.Gaps...)
		old.Executions = append(old.Executions, o.Executions...)
		old.Value += "；" + database + "：" + o.Value
		for _, table := range o.Tables {
			merged := false
			for i := range old.Tables {
				if old.Tables[i].Title == table.Title && strings.Join(old.Tables[i].Columns, "\x00") == strings.Join(table.Columns, "\x00") {
					old.Tables[i].Rows = append(old.Tables[i].Rows, table.Rows...)
					merged = true
					break
				}
			}
			if !merged {
				old.Tables = append(old.Tables, table)
			}
		}
		if old.CollectedAt.IsZero() || (!o.CollectedAt.IsZero() && o.CollectedAt.Before(old.CollectedAt)) {
			old.CollectedAt = o.CollectedAt
		}
	}
}

func (s *OperationsService) collectAllPostgreSQLDatabases(ctx context.Context, cfg opsprobe.DatabaseConfig, lane string, r *opsprobe.JobResult) {
	databases, connections, maximum, err := opsprobe.PostgreSQLDatabases(ctx, cfg)
	if err != nil {
		r.ReportChecks = append(r.ReportChecks, inspection.Observation{Code: "postgresql-databases", Name: "数据库范围", Status: "UNKNOWN", Reason: SQLExecutionErrorMessage(err), CollectedAt: time.Now()})
		return
	}
	inventory := inspection.Observation{Code: "postgresql-databases", Name: "数据库范围", Status: "RECORDED", CollectedAt: time.Now(), Value: fmt.Sprintf("实例内 %d 个数据库", len(databases)), Tables: []inspection.Table{{Columns: []string{"数据库", "当前连接", "数据可用情况", "采集时间", "说明"}}}}
	if lane == "live" {
		r.Indicators["connections"], r.Indicators["maxConnections"] = float64(connections), float64(maximum)
		r.ReportChecks = append(r.ReportChecks, inspection.Observation{Code: "cloudhis-27", Name: "实例连接数", Status: "RECORDED", Value: fmt.Sprintf("%d / %d（实例全部数据库）", connections, maximum), Attributes: map[string]string{"scope": "instance"}, CollectedAt: inventory.CollectedAt})
	}
	complete := true
	for _, database := range databases {
		reason := ""
		if !database.AllowConnections {
			reason = "数据库禁止连接"
		} else if !database.CanConnect {
			reason = "当前账号没有 CONNECT 权限"
		}
		sampleAt := time.Now()
		state := "已读取"
		if reason != "" {
			state = "未连接"
		} else {
			c := cfg
			c.Database = database.Name
			if lane == "deep" {
				checks := opsprobe.InspectionDatabase(ctx, c)
				for _, o := range checks {
					if o.Status == "UNKNOWN" {
						state = "部分数据未读取"
					}
				}
				mergeDatabaseObservations(&r.ReportChecks, database.Name, checks)
			} else {
				d, err := opsprobe.Diagnose(ctx, c)
				if err != nil {
					reason = SQLExecutionErrorMessage(err)
					state = "未读取"
				} else {
					sampleAt = d.CheckedAt
					if r.Database == nil {
						r.Database = d
					}
					for _, spec := range []struct{ code, name, key string }{{"cloudhis-28", "当前锁等待", "lockWaits"}, {"cloudhis-30", "长事务（≥300秒）", "longTransactions"}} {
						count := 0
						table := inspection.Table{Columns: []string{"PID", "用户", "事务秒数", "等待事件", "SQL 摘要"}}
						for _, a := range d.Activities {
							if a.DBName != database.Name {
								continue
							}
							if (spec.key == "lockWaits" && a.WaitEventType == "Lock") || (spec.key == "longTransactions" && a.TransactionSeconds >= 300) {
								count++
								table.Rows = append(table.Rows, []string{strconv.Itoa(a.PID), a.Username, fmt.Sprintf("%.2f", a.TransactionSeconds), strings.TrimSpace(a.WaitEventType + " " + a.WaitEvent), inspection.Preview(a.Query, 1000)})
							}
						}
						o := inspection.Observation{Code: spec.code, Name: spec.name, Status: "RECORDED", Value: fmt.Sprintf("%d 个", count), CollectedAt: d.CheckedAt, Tables: []inspection.Table{table}}
						if !d.StatsVisible {
							complete = false
							o.Status = "UNKNOWN"
							o.Reason = "当前账号缺少 pg_read_all_stats，其他账号会话不可见"
						} else {
							r.Indicators[spec.key] += float64(count)
						}
						mergeDatabaseObservations(&r.ReportChecks, database.Name, []inspection.Observation{o})
					}
					r.Indicators["deadlocks"] += float64(d.Deadlocks)
					health := opsprobe.PostgreSQLTableHealth(ctx, c)
					if health.Status == "UNKNOWN" {
						state = "部分数据未读取"
					}
					mergeDatabaseObservations(&r.ReportChecks, database.Name, []inspection.Observation{health})
				}
			}
		}
		if reason != "" && database.AllowConnections {
			complete = false
			codes := []string{"cloudhis-28", "cloudhis-30", "postgresql-table-health"}
			if lane == "deep" {
				codes = nil
				for n := 20; n <= 35; n++ {
					codes = append(codes, fmt.Sprintf("cloudhis-%d", n))
				}
			}
			mergeDatabaseObservations(&r.ReportChecks, database.Name, inspection.EnsureResults(codes, nil, reason))
		}
		inventory.Tables[0].Rows = append(inventory.Tables[0].Rows, []string{database.Name, strconv.Itoa(database.Connections), state, sampleAt.Format(time.RFC3339), reason})
	}
	if !complete {
		delete(r.Indicators, "lockWaits")
		delete(r.Indicators, "longTransactions")
		delete(r.Indicators, "deadlocks")
		inventory.Attributes = map[string]string{"complete": "false"}
	}
	r.ReportChecks = append(r.ReportChecks, inventory)
}
