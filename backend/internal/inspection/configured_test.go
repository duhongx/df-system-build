package inspection

import (
	"fmt"
	"testing"
)

func TestConfiguredReportContainsOnlyActualScopeAndCompactsNormalResults(t *testing.T) {
	var observations []Observation
	for i := 0; i < 50; i++ {
		observations = append(observations, Observation{Code: fmt.Sprintf("redis-%d", i), Name: "运行项", Status: "NORMAL", Value: "本次正常"})
	}
	observations = append(observations, Observation{Code: "redis-failure", Name: "复制", Status: "UNKNOWN", Reason: "账号缺少读取权限"})
	r := Build(Report{Mode: "configured", Objects: []Object{{ID: 1, Type: "redis", Name: "redis-1", Status: "COMPLETED", Observations: observations}}})
	if len(r.Items) != 51 || r.Summary.NotRun != 0 || r.Summary.Unknown != 1 || len(r.Findings) != 1 {
		t.Fatalf("scope/counts: %+v", r.Summary)
	}
	sections := Sections(Presentation(&r))
	for _, s := range sections {
		if s.ID == "redis" {
			if len(s.Items) != 2 || len(s.Items[0].Evidence[0].Tables[0].Rows) != 50 {
				t.Fatalf("normal rows not compacted: %+v", s)
			}
			if len(s.Items[1].Evidence[0].Tables) != 1 || s.Items[1].Evidence[0].Tables[0].Rows[0][2] != "账号缺少读取权限" {
				t.Fatal("failure explanation lost")
			}
		}
	}
}

func TestConfiguredReportOmitsEmptyHostChapter(t *testing.T) {
	r := Build(Report{Mode: "configured", Objects: []Object{{ID: 1, Type: "elasticsearch", Status: "COMPLETED", Observations: []Observation{{Code: "elasticsearch-health", Status: "NORMAL", Value: "green"}}}}})
	for _, section := range Sections(&r) {
		if section.ID == "resources" {
			t.Fatal("a component-only report included an empty Linux host chapter")
		}
	}
}

func TestConfiguredMissingHostResourceIsKeptInComponentChapter(t *testing.T) {
	r := Build(Report{Mode: "configured", Objects: []Object{{ID: 1, Type: "database", Status: "COMPLETED", Observations: []Observation{{Code: "cloudhis-15", Status: "UNKNOWN", Reason: "未关联数据库所在主机"}, {Code: "postgresql-table-health", Name: "表统计", Status: "RECORDED", Tables: []Table{{Columns: []string{"数据库", "表"}, Rows: [][]string{{"business", "orders"}}}}}}}}})
	found := false
	for _, section := range Sections(Presentation(&r)) {
		for _, item := range section.Items {
			for _, e := range item.Evidence {
				for _, table := range e.Tables {
					for _, row := range table.Rows {
						if len(row) > 2 && row[2] == "未关联数据库所在主机" {
							found = true
						}
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("missing component host resource disappeared from the report")
	}
	components := Components(&r)
	if len(components.Rows) != 1 || components.Rows[0][2] != "0 项" || components.Rows[0][3] != "1 项" {
		t.Fatal("normal result table counted as a space fault", components)
	}
}

func TestConfiguredPresentationDoesNotRestorePodAdviceOrThreshold(t *testing.T) {
	o := Observation{Code: "cloudhis-10", Status: "RECORDED", Tables: []Table{{Columns: []string{"Pod", "就绪", "状态", "重启次数", "运行时间／补充"}, Rows: [][]string{{"app", "1/1", "Running", "2", "10d"}}}}}
	r := Report{Mode: "configured", Version: "3.2", Items: []Item{{Definition: Definition{Code: o.Code}, Evidence: []Evidence{{Observation: o}}}}}
	view := Presentation(&r)
	got := view.Items[0].Evidence[0]
	if got.Advice != "" || got.Threshold != "" || got.Status != "RECORDED" {
		t.Fatal("presentation reintroduced an unconfirmed health policy", got)
	}
}
