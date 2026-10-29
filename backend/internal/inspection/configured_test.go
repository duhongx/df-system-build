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
			if s.Items[1].Reason == "" && s.Items[1].Evidence[0].Reason != "账号缺少读取权限" {
				t.Fatal("failure explanation lost")
			}
		}
	}
}
