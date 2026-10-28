package inspection

import (
	"strings"
	"testing"
)

func TestRecordedCommandIsNotAnAutomaticHealthyConclusion(t *testing.T) {
	r := Build(Report{Objects: []Object{{ID: 1, Type: "host", Status: "COMPLETED", CheckCodes: []string{"cloudhis-7"}, Observations: []Observation{{Code: "cloudhis-7", Status: "RECORDED", Executions: []Execution{{Command: "kubectl get nodes", Stdout: "node-a Ready", ExitCode: 0}}}}}}})
	if r.Items[6].Status != "RECORDED" || r.Summary.Recorded != 1 || r.Summary.Normal != 0 || r.Summary.Completed != 1 {
		t.Fatalf("recorded command misclassified %+v", r.Summary)
	}
	if r.Summary.Planned != 1 || r.Summary.NotRun != 38 || r.Summary.Unknown != 0 || len(r.Findings) != 0 {
		t.Fatal("unselected checks were counted as collection failures")
	}
	r = Build(Report{Objects: []Object{{ID: 1, Type: "host", Status: "COMPLETED", CheckCodes: []string{"cloudhis-7"}, Observations: []Observation{{Code: "cloudhis-7", Status: "RECORDED", Executions: []Execution{{Command: "kubectl get nodes", ExitCode: 127, Stderr: "not found"}}}}}}})
	if r.Items[6].Status != "UNKNOWN" || r.Items[6].Complete {
		t.Fatal("failed command was counted as recorded")
	}
}

func TestArchiveRequiresOneActualLeaderResult(t *testing.T) {
	r := Build(Report{Objects: []Object{{ID: 1, Type: "host", Status: "COMPLETED", CheckCodes: []string{"cloudhis-19"}, Observations: []Observation{{Code: "cloudhis-19", Status: "NOT_APPLICABLE", Reason: "本机是 Replica"}}}}})
	if r.Items[18].Status != "UNKNOWN" || r.Summary.Completed != 0 {
		t.Fatal("replica-only archive check marked complete")
	}
}

func TestCoveragePreservesPartialObjectFailureAndSeverity(t *testing.T) {
	r := Build(Report{Objects: []Object{
		{ID: 1, Name: "db-a", Type: "database", Status: "COMPLETED", Observations: []Observation{{Code: "cloudhis-27", Status: "CRITICAL", Value: "99%"}}},
		{ID: 2, Name: "db-b", Type: "database", Status: "FAILED", Message: "permission denied"},
	}})
	if len(r.Items) != 39 || r.Summary.Total != 39 {
		t.Fatal("manual catalogue incomplete")
	}
	item := r.Items[26]
	if item.Status != "CRITICAL" || item.Complete || item.Missing != 1 || len(item.Evidence) != 2 {
		t.Fatalf("partial evidence masked: %+v", item)
	}
	if r.Summary.Completed != 0 || r.Summary.Coverage != 0 || r.Summary.Critical != 1 {
		t.Fatalf("coverage inflated: %+v", r.Summary)
	}
	if len(r.Findings) == 0 || r.Findings[0].Status != "CRITICAL" || !strings.Contains(r.Conclusion, "不能") {
		t.Fatalf("conclusion/priority: %+v", r)
	}
	if r.Summary.Normal+r.Summary.Warning+r.Summary.Critical+r.Summary.Unknown+r.Summary.NotApplicable+r.Summary.NotRun != 39 {
		t.Fatal("summary does not reconcile")
	}
}

func TestCatalogueScopeAndExplicitNotApplicable(t *testing.T) {
	r := Build(Report{Objects: []Object{{ID: 1, Type: "host", Status: "COMPLETED", Roles: []string{"postgresql"}, Observations: []Observation{{Code: "cloudhis-15", Status: "NORMAL"}, {Code: "cloudhis-24", Status: "NOT_APPLICABLE", Reason: "单机环境未配置复制用途"}}, Supplement: []Observation{{Code: "service:test", Name: "服务", Status: "WARNING"}}}}})
	if r.Summary.Completed != 1 || r.Summary.NotApplicable != 1 || r.Items[5].Status != "NOT_RUN" {
		t.Fatalf("wrong scope %+v", r.Summary)
	}
	if r.Summary.Coverage != 100.0 {
		t.Fatalf("bad denominator %f", r.Summary.Coverage)
	}
	if r.Findings[0].Name != "服务" {
		t.Fatal("supplemental warning lost")
	}
	r = Build(Report{Objects: []Object{{ID: 1, Type: "database", Status: "COMPLETED", Observations: []Observation{{Code: "cloudhis-20", Status: "NOT_APPLICABLE"}}}}})
	if r.Items[19].Status != "UNKNOWN" || r.Summary.NotApplicable != 0 {
		t.Fatal("unsupported not-applicable decision accepted")
	}
}

func TestReportRedactsCredentialsWithoutRemovingEvidence(t *testing.T) {
	r := Build(Report{Objects: []Object{{ID: 1, Type: "database", Name: "db", Status: "COMPLETED", Observations: []Observation{{Code: "cloudhis-20", Status: "NORMAL", Evidence: "password='sensitive value'; token=secret123; https://admin:private@example.invalid/db", Tables: []Table{{Columns: []string{"value"}, Rows: [][]string{{"primary_conninfo='user=u password=p'"}}}}}}}}})
	e := r.Items[19].Evidence[0]
	for _, v := range []string{"sensitive value", "secret123", "admin:private", "password=p"} {
		if strings.Contains(e.Evidence+e.Tables[0].Rows[0][0], v) {
			t.Fatalf("credential leaked: %s", v)
		}
	}
	if !strings.Contains(e.Evidence, "[REDACTED]") {
		t.Fatal("evidence missing redaction marker")
	}
	for _, s := range []string{`{"password":"secret-in-json"}`, `Authorization: Bearer secret-in-json`, `PGPASSWORD='secret-in-json'`, `api_key=secret-in-json`} {
		if strings.Contains(Redact(s), "secret-in-json") {
			t.Fatalf("credential syntax not redacted: %s", s)
		}
	}
}

func TestMissingEvidenceWithMeasuredWarningNeverCountsAsComplete(t *testing.T) {
	r := Build(Report{Objects: []Object{{ID: 1, Type: "database", Status: "COMPLETED", Observations: []Observation{{Code: "cloudhis-30", Status: "WARNING", Value: "one long transaction", Gaps: []string{"activity truncated"}}}}}})
	if r.Items[29].Status != "WARNING" || r.Items[29].Complete || r.Summary.Completed != 0 {
		t.Fatal("incomplete warning counted as full coverage")
	}
}
