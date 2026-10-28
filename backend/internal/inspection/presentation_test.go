package inspection

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPresentationKeepsFrozenEvidenceAndMovesLargeOutputOutOfReportBody(t *testing.T) {
	rows := [][]string{}
	for i := 0; i < 120; i++ {
		rows = append(rows, []string{fmt.Sprintf("pod-%d", i), "1/1", "Running", "0", "5d"})
	}
	rows = append(rows, []string{"broken-pod", "0/1", "CrashLoopBackOff", "9", "1d"})
	o := Observation{Code: "cloudhis-10", Status: "WARNING", Value: "121 个 Pod，1 个异常", Tables: []Table{{Title: "Pod", Columns: []string{"Pod", "就绪", "状态", "重启次数", "运行时间／补充"}, Rows: rows}}, Executions: []Execution{{Command: "kubectl get pods", Stdout: strings.Repeat("raw-overlay-and-command-output\n", 10000)}}}
	source := &Report{Summary: Summary{Planned: 1, Completed: 1, Warning: 1}, Items: []Item{{Definition: Definition{Code: "cloudhis-10"}, Evidence: []Evidence{{Observation: o}}}}}
	before, _ := json.Marshal(source)
	view := Presentation(source)
	e := view.Items[0].Evidence[0]
	if len(e.Executions) != 0 || len(e.Tables) != 1 || len(e.Tables[0].Rows) != 1 || e.Tables[0].Rows[0][0] != "broken-pod" || !strings.Contains(e.Value, "共 121 个 Pod") {
		t.Fatal("report did not prioritize and summarize actual results")
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("presentation changed the immutable source snapshot")
	}
	if view.Summary != source.Summary {
		t.Fatal("presentation changed coverage statistics")
	}
}

func TestPresentationPodHistoryUsesRowsAndCompactOverviewWithoutChangingFrozenResults(t *testing.T) {
	rows := [][]string{}
	paragraph := ""
	for i := 0; i < 11; i++ {
		name := fmt.Sprintf("calico-example-%02d", i)
		rows = append(rows, []string{name, "1/1", "Running", "2", "(9d ago) 189d"})
		paragraph += name + "：就绪 1/1，状态 Running，重启 2；"
	}
	o := Observation{Code: "cloudhis-8", Status: "WARNING", Value: "11 个 Pod，11 个需关注", Reason: paragraph, Tables: []Table{{Columns: []string{"Pod", "就绪", "状态", "重启次数", "运行时间／补充"}, Rows: rows}}}
	source := &Report{Summary: Summary{Planned: 1, Completed: 1, Warning: 1}, Items: []Item{{Definition: Definition{Code: o.Code}, Evidence: []Evidence{{ID: "pod-evidence", Observation: o}}}}, Findings: []Finding{{EvidenceID: "pod-evidence", Value: o.Value, Reason: paragraph}}}
	before, _ := json.Marshal(source)
	view := Presentation(source)
	e := view.Items[0].Evidence[0]
	for _, text := range []string{"当前异常 0 个", "当前运行且就绪 11 个", "11 个有累计重启记录"} {
		if !strings.Contains(e.Value, text) || !strings.Contains(view.Findings[0].Value, text) {
			t.Fatalf("inconsistent Pod summary: %+v", e)
		}
	}
	if strings.Contains(e.Reason, "calico-example") || strings.Contains(view.Findings[0].Reason, "calico-example") || !strings.Contains(e.Reason, "不等同于当前故障") {
		t.Fatal("Pod names were repeated in a prose block or history was presented as a current fault")
	}
	if len(e.Tables) != 1 || !strings.HasPrefix(e.Tables[0].Title, "历史重启记录") || len(e.Tables[0].Rows) != 10 || !strings.Contains(e.Tables[0].Note, "11 行") {
		t.Fatalf("history table did not retain counts and row limit: %+v", e.Tables)
	}
	if row := e.Tables[0].Rows[0]; len(row) != 4 || row[1] != "2" || row[2] != "9d ago" || row[3] != "189d" {
		t.Fatalf("restart time and Pod age were mixed: %v", row)
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) || source.Summary != view.Summary {
		t.Fatal("reformatting changed frozen evidence, warning classification or coverage")
	}
}

func TestPresentationSeparatesCurrentPodFaultsAndHistoryAndRetainsCollectionFailures(t *testing.T) {
	o := Observation{Code: "cloudhis-10", Status: "WARNING", Tables: []Table{{Columns: []string{"Pod", "就绪", "状态", "重启次数", "运行时间／补充"}, Rows: [][]string{
		{"healthy", "1/1", "Running", "0", "10d"},
		{"old-restart", "1/1", "Running", "2", "10d"},
		{"crashing", "0/1", "CrashLoopBackOff", "12", "(2m ago) 1d"},
		{"not-ready", "0/1", "Running", "0", "1d"},
	}}}}
	view := presentationObservation(o, o.Code)
	if !strings.Contains(view.Value, "当前异常 2 个") || !strings.Contains(view.Value, "2 个有累计重启记录") || len(view.Tables) != 2 {
		t.Fatalf("wrong mixed-state summary: %+v", view)
	}
	if len(view.Tables[0].Rows) != 2 || view.Tables[0].Rows[0][0] != "crashing" || view.Tables[1].Rows[0][0] != "old-restart" || view.Tables[1].Rows[0][2] != "未提供" {
		t.Fatal("current faults and historic restarts were mixed or missing time was invented")
	}
	o.Status, o.Reason, o.Gaps = "UNKNOWN", "permission denied", []string{"partial collection"}
	view = presentationObservation(o, o.Code)
	if !strings.Contains(view.Reason, "permission denied") || strings.Contains(view.Value, "当前异常") {
		t.Fatal("partial evidence was presented as a complete Pod health summary")
	}
	o.Status, o.Gaps = "WARNING", nil
	o.Tables[0].Rows[0][3] = "unknown"
	if _, ok := podPresentation(o); ok {
		t.Fatal("unrecognised restart count was treated as valid evidence")
	}
}

func TestPresentationPreservesFailureReasonAndSQLResultsWithoutPrintingSQLSource(t *testing.T) {
	o := Observation{Status: "UNKNOWN", Value: "执行并保存 1 条命令或 SQL 的输出", Executions: []Execution{{Kind: "sql", Command: "SELECT huge SQL", ExitCode: 1, Stderr: "permission denied"}}, Tables: []Table{{Columns: []string{"pid", "datname", "username", "app", "client_ip", "state_change", "query"}, Rows: [][]string{{"1", "his", "app", "api", "host", "now", strings.Repeat("query", 300)}}}}}
	view := presentationObservation(o, "cloudhis-29")
	if !strings.Contains(view.Reason, "permission denied") || len(view.Tables[0].Columns) > 6 || len(view.Executions) != 0 {
		t.Fatal("report lost the actual failure or kept wide SQL output")
	}
}

func TestRedactionDoesNotConsumeNextDirectorySizeAfterAuthorizationOrTokenNames(t *testing.T) {
	text := "2G ./df-authorization\n8.5G ./df-sapi\n1G ./token\n3G ./next\n"
	if Redact(text) != text {
		t.Fatal("redaction destroyed ordinary directory sizes")
	}
	for _, text := range []string{"password=private-value", "SecretKey : private-value", "token private-value", "password =\nprivate-value", "Authorization: Bearer private-value"} {
		if strings.Contains(Redact(text), "private-value") {
			t.Fatalf("credential not redacted: %q", text)
		}
	}
}
