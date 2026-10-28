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
	o := Observation{Value: "121 个 Pod，1 个异常", Tables: []Table{{Title: "Pod", Columns: []string{"Pod", "就绪", "状态", "重启次数", "运行时间"}, Rows: rows}}, Executions: []Execution{{Command: "kubectl get pods", Stdout: strings.Repeat("raw-overlay-and-command-output\n", 10000)}}}
	source := &Report{Summary: Summary{Planned: 1, Completed: 1, Warning: 1}, Items: []Item{{Definition: Definition{Code: "cloudhis-10"}, Evidence: []Evidence{{Observation: o}}}}}
	before, _ := json.Marshal(source)
	view := Presentation(source)
	e := view.Items[0].Evidence[0]
	if len(e.Executions) != 0 || len(e.Tables[0].Rows) != 10 || e.Tables[0].Rows[0][0] != "broken-pod" || !strings.Contains(e.Tables[0].Note, "121") {
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
