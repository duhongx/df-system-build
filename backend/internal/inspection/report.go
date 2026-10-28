// Package inspection defines the versioned evidence and report snapshot shared by
// the remote executor, API and both report formats. It never performs collection.
package inspection

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Version = "2.1"

type Table struct {
	Title   string     `json:"title"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Note    string     `json:"note"`
}

type Observation struct {
	ConfigurationState string             `json:"configurationState,omitempty"`
	Measurements       map[string]float64 `json:"measurements,omitempty"`
	Executions         []Execution        `json:"executions,omitempty"`
	Code               string             `json:"code"`
	Name               string             `json:"name"`
	Status             string             `json:"status"`
	Value              string             `json:"value"`
	Threshold          string             `json:"threshold"`
	Source             string             `json:"source"`
	CollectedAt        time.Time          `json:"collectedAt"`
	Reason             string             `json:"reason"`
	Advice             string             `json:"advice"`
	Evidence           string             `json:"evidence"`
	Tables             []Table            `json:"tables"`
	Gaps               []string           `json:"gaps"`
}

type Object struct {
	ExecutorEndpoint string        `json:"executorEndpoint"`
	CheckCodes       []string      `json:"checkCodes,omitempty"`
	ID               uint          `json:"id"`
	Name             string        `json:"name"`
	Type             string        `json:"type"`
	Endpoint         string        `json:"endpoint"`
	Roles            []string      `json:"roles"`
	Status           string        `json:"status"`
	Message          string        `json:"message"`
	Observations     []Observation `json:"observations"`
	Supplement       []Observation `json:"supplement"`
	Tables           []Table       `json:"tables"`
}

type Evidence struct {
	ID         string `json:"id"`
	ObjectID   uint   `json:"objectId"`
	ObjectName string `json:"objectName"`
	Endpoint   string `json:"endpoint"`
	Observation
}

type Item struct {
	Definition
	Status   string     `json:"status"`
	Complete bool       `json:"complete"`
	Missing  int        `json:"missing"`
	Reason   string     `json:"reason"`
	Evidence []Evidence `json:"evidence"`
}

type Finding struct {
	ID         string `json:"id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ObjectID   uint   `json:"objectId"`
	ObjectName string `json:"objectName"`
	Value      string `json:"value"`
	Reason     string `json:"reason"`
	Advice     string `json:"advice"`
	Owner      string `json:"owner"`
	EvidenceID string `json:"evidenceId"`
	Impact     string `json:"impact"`
}

type Summary struct {
	NotRun        int     `json:"notRun"`
	Planned       int     `json:"planned"`
	Recorded      int     `json:"recorded"`
	Total         int     `json:"total"`
	Normal        int     `json:"normal"`
	Warning       int     `json:"warning"`
	Critical      int     `json:"critical"`
	Unknown       int     `json:"unknown"`
	NotApplicable int     `json:"notApplicable"`
	Completed     int     `json:"completed"`
	Missing       int     `json:"missing"`
	Coverage      float64 `json:"coverage"`
}

type Report struct {
	Version     string    `json:"version"`
	RunID       uint      `json:"runId"`
	Name        string    `json:"name"`
	Operator    string    `json:"operator"`
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt"`
	GeneratedAt time.Time `json:"generatedAt"`
	Conclusion  string    `json:"conclusion"`
	Summary     Summary   `json:"summary"`
	Items       []Item    `json:"items"`
	Findings    []Finding `json:"findings"`
	Objects     []Object  `json:"objects"`
	Limitations []string  `json:"limitations"`
}

func Build(r Report) Report {
	r.Version = Version
	r.Items, r.Findings = []Item{}, []Finding{}
	r.Summary = Summary{Total: len(Catalogue())}
	r.Limitations = []string{"依据 HIS 运维巡检操作指导书 V2.0，记录实际执行的命令或 SQL、执行时间及返回结果。", "“已记录”表示执行成功并保存输出，不等于自动认定业务正常；指导书要求记录输出的项目交由运维人员核对。", "命令失败、权限不足、缺少工具、输出截断及未执行项保留具体原因，不补造结果。"}
	for i := range r.Objects {
		o := &r.Objects[i]
		o.Name, o.Endpoint, o.Message, o.ExecutorEndpoint = Redact(o.Name), Redact(o.Endpoint), Redact(o.Message), Redact(o.ExecutorEndpoint)
		for j := range o.Observations {
			sanitize(&o.Observations[j])
		}
		for j := range o.Supplement {
			sanitize(&o.Supplement[j])
		}
		for j := range o.Tables {
			sanitizeTable(&o.Tables[j])
		}
	}
	for _, def := range Catalogue() {
		item := Item{Definition: def, Status: "RECORDED", Complete: true, Evidence: []Evidence{}}
		for _, o := range r.Objects {
			if !matches(def, o) {
				continue
			}
			var observations []Observation
			for _, obs := range o.Observations {
				if obs.Code == def.Code {
					observations = append(observations, obs)
				}
			}
			if len(observations) == 0 {
				observations = []Observation{{Code: def.Code, Status: "UNKNOWN", Reason: "该对象尚未取得此项的结构化证据。"}}
				if o.Status != "COMPLETED" {
					observations[0].Reason = "对象采集未完成：" + o.Message
				}
			}
			for _, obs := range observations {
				for _, entry := range obs.Executions {
					if entry.ExitCode != 0 || entry.Error != "" || entry.Truncated {
						obs.Status = "UNKNOWN"
						if len(obs.Gaps) == 0 {
							obs.Gaps = append(obs.Gaps, "执行失败或输出截断，请查看原始错误与退出码。")
						}
					}
				}
				if obs.Status == "NOT_APPLICABLE" && obs.Reason == "" {
					obs.Status, obs.Reason = "UNKNOWN", "缺少不适用原因"
				}
				if obs.Status != "NORMAL" && obs.Status != "RECORDED" && obs.Status != "WARNING" && obs.Status != "CRITICAL" && obs.Status != "NOT_APPLICABLE" {
					obs.Status = "UNKNOWN"
				}
				if len(obs.Gaps) > 0 || obs.Status == "UNKNOWN" || (o.Status != "COMPLETED" && obs.Status != "NOT_APPLICABLE") {
					item.Missing++
					item.Complete = false
					if obs.Status == "NORMAL" || obs.Status == "RECORDED" {
						obs.Status = "UNKNOWN"
					}
					if o.Status != "COMPLETED" {
						obs.Gaps = append(obs.Gaps, "对象采集未完整结束："+o.Message)
					}
				}
				e := Evidence{ID: fmt.Sprintf("e-%s-%d-%d", def.Code, o.ID, len(item.Evidence)+1), ObjectID: o.ID, ObjectName: o.Name, Endpoint: o.Endpoint, Observation: obs}
				item.Evidence = append(item.Evidence, e)
				if priority(obs.Status) > priority(item.Status) || (item.Status == "RECORDED" && obs.Status == "NORMAL") {
					item.Status = obs.Status
				}
				if obs.Status != "NORMAL" && obs.Status != "RECORDED" && obs.Status != "NOT_APPLICABLE" || len(obs.Gaps) > 0 {
					r.Findings = append(r.Findings, Finding{ID: e.ID, Code: def.Code, Name: def.Name, Status: obs.Status, ObjectID: o.ID, ObjectName: o.Name, Value: obs.Value, Reason: strings.TrimSpace(obs.Reason + " " + strings.Join(obs.Gaps, "；")), Advice: first(obs.Advice, def.Advice), Owner: def.Owner, EvidenceID: e.ID})
				}
			}
		}
		if len(item.Evidence) == 0 {
			item.Status, item.Complete, item.Missing = "NOT_RUN", false, 0
			item.Reason = "本次未分配执行服务器，未执行该项检查"
		} else {
			allNA := true
			for _, e := range item.Evidence {
				if e.Status != "NOT_APPLICABLE" {
					allNA = false
				}
			}
			if allNA {
				item.Status = "NOT_APPLICABLE"
				if def.Code == "cloudhis-19" {
					item.Status, item.Complete, item.Missing = "UNKNOWN", false, 1
					item.Reason = "本次选中的主机均未取得当前 Leader 的归档检查结果"
					r.Findings = append(r.Findings, Finding{Code: def.Code, Name: def.Name, Status: "UNKNOWN", Reason: item.Reason})
				}
			}
		}
		switch item.Status {
		case "NOT_RUN":
			r.Summary.NotRun++
		case "RECORDED":
			r.Summary.Recorded++
		case "NORMAL":
			r.Summary.Normal++
		case "WARNING":
			r.Summary.Warning++
		case "CRITICAL":
			r.Summary.Critical++
		case "NOT_APPLICABLE":
			r.Summary.NotApplicable++
		default:
			r.Summary.Unknown++
		}
		if item.Complete && item.Status != "NOT_APPLICABLE" {
			r.Summary.Completed++
		}
		r.Summary.Missing += item.Missing
		r.Items = append(r.Items, item)
	}
	// Supplemental host/service results remain visible and can raise findings,
	// but must never inflate completion of an unrelated manual check.
	for _, o := range r.Objects {
		mappedResources := false
		for _, obs := range o.Observations {
			if obs.Code == "cloudhis-6" || obs.Code == "cloudhis-15" || obs.Code == "cloudhis-36" {
				mappedResources = true
			}
		}
		for i, obs := range o.Supplement {
			if mappedResources && (strings.HasPrefix(obs.Code, "host_") || strings.HasPrefix(obs.Code, "disk:") || strings.HasPrefix(obs.Code, "inode:") || obs.Code == "collection_warning") {
				continue
			}
			if obs.Status != "NORMAL" && obs.Status != "RECORDED" {
				r.Findings = append(r.Findings, Finding{ID: fmt.Sprintf("extra-%d-%d", o.ID, i), Name: obs.Name, Status: obs.Status, ObjectID: o.ID, ObjectName: o.Name, Value: obs.Value, Reason: obs.Reason, Advice: obs.Advice, Owner: "运维人员", EvidenceID: fmt.Sprintf("object-%d", o.ID)})
			}
		}
	}
	sort.SliceStable(r.Findings, func(i, j int) bool { return priority(r.Findings[i].Status) > priority(r.Findings[j].Status) })
	for i := range r.Findings {
		r.Findings[i].Impact = impactFor(r.Findings[i].Code)
		if r.Findings[i].Status == "UNKNOWN" {
			r.Findings[i].Impact = "本项执行结果未取得，无法据此判断业务状态。"
		}
	}
	r.Summary.Planned = r.Summary.Total - r.Summary.NotRun - r.Summary.NotApplicable
	if n := r.Summary.Planned; n > 0 {
		r.Summary.Coverage = float64(r.Summary.Completed) * 100 / float64(n)
	}
	r.Conclusion = fmt.Sprintf("本次选定 %d 项，已取得结果 %d 项；正常 %d 项，提醒 %d 项，已记录待核对 %d 项，结果未完整取得 %d 项。指导书另有 %d 项未纳入本次执行。", r.Summary.Planned, r.Summary.Completed, r.Summary.Normal, r.Summary.Warning, r.Summary.Recorded, r.Summary.Unknown, r.Summary.NotRun)
	for _, f := range r.Findings {
		if f.Status == "CRITICAL" || f.Status == "WARNING" {
			r.Conclusion += "\n优先核对“" + f.ObjectName + "”的“" + f.Name + "”。"
			break
		}
	}
	if r.Summary.Unknown > 0 || r.Summary.Missing > 0 {
		r.Conclusion += " 检查证据尚不完整，不能据此认定整个环境运行正常。"
	}
	r.Name, r.Operator = Redact(r.Name), Redact(r.Operator)
	return r
}

func impactFor(code string) string {
	switch code {
	case "cloudhis-4", "cloudhis-5":
		return "故障后可用恢复点及恢复能力可能不足，影响业务数据保护。"
	case "cloudhis-6", "cloudhis-15", "cloudhis-36", "cloudhis-37":
		return "资源或文件系统余量不足时，可能影响业务响应与新数据写入。"
	case "cloudhis-11", "cloudhis-13", "cloudhis-14", "cloudhis-19":
		return "持续增长可能消耗磁盘空间，影响服务运行、归档或更新。"
	case "cloudhis-17", "cloudhis-18", "cloudhis-23", "cloudhis-24", "cloudhis-25":
		return "需评估数据复制、归档和 WAL 保留对可用性及恢复链的影响。"
	case "cloudhis-27":
		return "连接容量耗尽可能导致 HIS 新请求无法取得数据库连接。"
	case "cloudhis-28", "cloudhis-30", "cloudhis-33":
		return "可能延长业务等待、占用数据库资源或阻碍维护，需要结合业务用途核实。"
	case "cloudhis-38":
		return "成员或磁盘异常可能降低存储冗余及读写可用性。"
	default:
		return "需结合本项证据与环境要求评估影响；证据不足时无法给出正常结论。"
	}
}

func matches(d Definition, o Object) bool {
	for _, obs := range o.Observations {
		if obs.Code == d.Code {
			return true
		}
	}
	if len(o.CheckCodes) > 0 {
		for _, code := range o.CheckCodes {
			if code == d.Code {
				return true
			}
		}
		return false
	}
	if d.Scope == "database" {
		return o.Type == "database"
	}
	for _, role := range o.Roles {
		if d.Scope == role && o.Type == "host" {
			return true
		}
	}
	return false
}
func priority(s string) int {
	return map[string]int{"CRITICAL": 4, "WARNING": 3, "UNKNOWN": 2, "FAILED": 2, "CANCELED": 2, "NORMAL": 1, "RECORDED": 1}[s]
}
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var secretAssignment = regexp.MustCompile(`(?i)(\b(?:password|pgpassword|passwd|secret(?:_key)?|secretkey|access_key|accesskey|api_key|apikey|token|authorization|primary_conninfo)\b["']?[ \t]*(?:[:=]\s*|[ \t]+))('[^']*'|"[^"]*"|[^\s;,&]+)`)
var urlCredential = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@]+(?::[^\s/@]*)?@`)
var bearerCredential = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]+`)

func Redact(s string) string {
	return secretAssignment.ReplaceAllString(urlCredential.ReplaceAllString(bearerCredential.ReplaceAllString(s, "${1}[REDACTED]"), "${1}[REDACTED]@"), "${1}[REDACTED]")
}
func sanitize(o *Observation) {
	for i := range o.Executions {
		e := &o.Executions[i]
		e.Command, e.Stdout, e.Stderr, e.Error = Redact(e.Command), Redact(e.Stdout), Redact(e.Stderr), Redact(e.Error)
	}
	o.Name, o.Value, o.Source, o.Reason, o.Advice, o.Evidence, o.Threshold = Redact(o.Name), Redact(o.Value), Redact(o.Source), Redact(o.Reason), Redact(o.Advice), Redact(o.Evidence), Redact(o.Threshold)
	for i := range o.Gaps {
		o.Gaps[i] = Redact(o.Gaps[i])
	}
	for i := range o.Tables {
		sanitizeTable(&o.Tables[i])
	}
}
func sanitizeTable(t *Table) {
	t.Title, t.Note = Redact(t.Title), Redact(t.Note)
	for i := range t.Columns {
		t.Columns[i] = Redact(t.Columns[i])
	}
	for i := range t.Rows {
		for j := range t.Rows[i] {
			t.Rows[i][j] = Redact(t.Rows[i][j])
		}
	}
}
