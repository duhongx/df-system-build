package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"df-build-server/internal/model"
	pg_query "github.com/pganalyze/pg_query_go/v6"
)

type SlowTemplate struct {
	ID         string                   `json:"id"`
	Template   string                   `json:"template"`
	Database   string                   `json:"database"`
	Phase      string                   `json:"phase"`
	LogCount   int64                    `json:"logCount"`
	TotalMs    float64                  `json:"totalMs"`
	AverageMs  float64                  `json:"averageMs"`
	MaxMs      float64                  `json:"maxMs"`
	TimeShare  float64                  `json:"timeShare"`
	FirstSeen  time.Time                `json:"firstSeen"`
	LastSeen   time.Time                `json:"lastSeen"`
	Samples    []model.DatabaseLogEvent `json:"samples"`
	Users      []string                 `json:"users"`
	Clients    []string                 `json:"clients"`
	Normalized bool                     `json:"normalized"`
}
type SlowTrend struct {
	Time     time.Time `json:"time"`
	LogCount int64     `json:"logCount"`
	TotalMs  float64   `json:"totalMs"`
	MaxMs    float64   `json:"maxMs"`
}
type SlowLogReport struct {
	From          time.Time                `json:"from"`
	To            time.Time                `json:"to"`
	LogCount      int64                    `json:"logCount"`
	TemplateCount int                      `json:"templateCount"`
	TotalMs       float64                  `json:"totalMs"`
	MaxMs         float64                  `json:"maxMs"`
	Templates     []SlowTemplate           `json:"templates"`
	Trend         []SlowTrend              `json:"trend"`
	StepSeconds   int                      `json:"stepSeconds"`
	Source        model.OperationsDatabase `json:"source"`
}

func (s *OperationsService) AnalyzeSlowLogs(ctx context.Context, id uint, filter OperationsEventFilter, phase string) (*SlowLogReport, error) {
	var source model.OperationsDatabase
	if err := s.db.WithContext(ctx).First(&source, id).Error; err != nil {
		return nil, err
	}
	to := time.Now()
	from := to.Add(-24 * time.Hour)
	var err error
	if filter.From != "" {
		from, err = time.Parse(time.RFC3339, filter.From)
		if err != nil {
			return nil, fmt.Errorf("开始时间无效")
		}
	}
	if filter.To != "" {
		to, err = time.Parse(time.RFC3339, filter.To)
		if err != nil {
			return nil, fmt.Errorf("结束时间无效")
		}
	}
	if cutoff := time.Now().Add(-48 * time.Hour); from.Before(cutoff) {
		from = cutoff
	}
	if !from.Before(to) {
		return nil, fmt.Errorf("结束时间必须晚于开始时间")
	}
	if phase != "" && phase != "statement" && phase != "execute" && phase != "parse" && phase != "bind" {
		return nil, fmt.Errorf("无效的日志阶段")
	}
	q := s.db.WithContext(ctx).Model(&model.DatabaseLogEvent{}).Where("database_id=? AND type='slow_sql' AND event_time>=? AND event_time<=?", id, from, to)
	if filter.Keyword != "" {
		term := "%" + filter.Keyword + "%"
		q = q.Where("sql ILIKE ? OR username ILIKE ? OR db_name ILIKE ? OR client ILIKE ?", term, term, term, term)
	}
	if phase != "" {
		q = q.Where("phase=?", phase)
	}
	if filter.MinDuration > 0 {
		q = q.Where("duration_ms>=?", filter.MinDuration)
	}
	rows, err := q.Select("id,database_id,type,event_time,duration_ms,phase,db_name,username,client,sql").Order("event_time DESC,id DESC").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	step := int(math.Ceil(to.Sub(from).Seconds() / 120))
	if step < 60 {
		step = 60
	}
	out := &SlowLogReport{From: from, To: to, StepSeconds: step, Source: source, Templates: []SlowTemplate{}, Trend: []SlowTrend{}}
	groups := map[string]*SlowTemplate{}
	buckets := map[int64]*SlowTrend{}
	normalizations := map[string][2]string{}
	users := map[string]map[string]bool{}
	clients := map[string]map[string]bool{}
	for rows.Next() {
		var event model.DatabaseLogEvent
		if err := s.db.ScanRows(rows, &event); err != nil {
			return nil, err
		}
		normalized, ok := normalizations[event.SQL]
		if !ok {
			text, e := pg_query.Normalize(event.SQL)
			fingerprint, e2 := pg_query.Fingerprint(event.SQL)
			if e != nil || e2 != nil {
				text = event.SQL
				fingerprint = "raw:" + fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
			}
			normalized = [2]string{text, fingerprint}
			normalizations[event.SQL] = normalized
		}
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(event.DBName+"\x00"+event.Phase+"\x00"+normalized[1])))
		group := groups[key]
		if group == nil {
			group = &SlowTemplate{ID: key, Template: normalized[0], Database: event.DBName, Phase: event.Phase, Normalized: !strings.HasPrefix(normalized[1], "raw:"), FirstSeen: event.EventTime, LastSeen: event.EventTime, Samples: []model.DatabaseLogEvent{}, Users: []string{}, Clients: []string{}}
			groups[key] = group
			users[key] = map[string]bool{}
			clients[key] = map[string]bool{}
		}
		group.LogCount++
		group.TotalMs += event.DurationMs
		if event.DurationMs > group.MaxMs {
			group.MaxMs = event.DurationMs
		}
		if event.EventTime.Before(group.FirstSeen) {
			group.FirstSeen = event.EventTime
		}
		if event.EventTime.After(group.LastSeen) {
			group.LastSeen = event.EventTime
		}
		users[key][event.Username] = true
		clients[key][event.Client] = true
		if len(group.Samples) < 5 {
			group.Samples = append(group.Samples, event)
		}
		bucket := event.EventTime.Unix() / int64(step) * int64(step)
		point := buckets[bucket]
		if point == nil {
			point = &SlowTrend{Time: time.Unix(bucket, 0)}
			buckets[bucket] = point
		}
		point.LogCount++
		point.TotalMs += event.DurationMs
		if event.DurationMs > point.MaxMs {
			point.MaxMs = event.DurationMs
		}
		out.LogCount++
		out.TotalMs += event.DurationMs
		if event.DurationMs > out.MaxMs {
			out.MaxMs = event.DurationMs
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for key, group := range groups {
		group.AverageMs = group.TotalMs / float64(group.LogCount)
		if out.TotalMs > 0 {
			group.TimeShare = group.TotalMs * 100 / out.TotalMs
		}
		for x := range users[key] {
			if x != "" {
				group.Users = append(group.Users, x)
			}
		}
		for x := range clients[key] {
			if x != "" {
				group.Clients = append(group.Clients, x)
			}
		}
		sort.Strings(group.Users)
		sort.Strings(group.Clients)
		out.Templates = append(out.Templates, *group)
	}
	sort.Slice(out.Templates, func(i, j int) bool {
		if out.Templates[i].TotalMs != out.Templates[j].TotalMs {
			return out.Templates[i].TotalMs > out.Templates[j].TotalMs
		}
		return out.Templates[i].ID < out.Templates[j].ID
	})
	out.TemplateCount = len(out.Templates)
	for start := from.Unix() / int64(step) * int64(step); start <= to.Unix(); start += int64(step) {
		point := buckets[start]
		if point == nil {
			point = &SlowTrend{Time: time.Unix(start, 0)}
		}
		out.Trend = append(out.Trend, *point)
	}
	return out, nil
}
