package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/pkg/crypto"
)

func TestOperationsReportSnapshotIsFrozenAndCredentialsRedacted(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	now := time.Now()
	run := model.InspectionRun{Name: "快照测试", Status: "COMPLETED", StartedAt: &now}
	if e := db.Create(&run).Error; e != nil {
		t.Fatal(e)
	}
	result := opsprobe.JobResult{Version: opsprobe.Version, StartedAt: now, FinishedAt: now, ReportChecks: []inspection.Observation{{Code: "cloudhis-27", Status: "WARNING", Value: "before", Evidence: "password='never-persist-me'", CollectedAt: now}}}
	target := model.InspectionTargetRun{RunID: run.ID, Name: "db", Type: "database", Status: "COMPLETED", ResultJSON: mustJSON(result)}
	if e := db.Create(&target).Error; e != nil {
		t.Fatal(e)
	}
	if e := svc.GenerateInspectionReports(context.Background(), run.ID); e != nil && os.Getenv("DFOPS_REPORT_FONT") != "" {
		t.Fatal(e)
	}
	if e := db.First(&run, run.ID).Error; e != nil {
		t.Fatal(e)
	}
	first := run.ReportSnapshotJSON
	if first == "" || strings.Contains(first, "never-persist-me") {
		t.Fatal("snapshot absent or credential retained")
	}
	if e := db.Model(&target).Update("result_json", mustJSON(opsprobe.JobResult{ReportChecks: []inspection.Observation{{Code: "cloudhis-27", Status: "NORMAL", Value: "after"}}})).Error; e != nil {
		t.Fatal(e)
	}
	if e := svc.GenerateInspectionReports(context.Background(), run.ID); e != nil && os.Getenv("DFOPS_REPORT_FONT") != "" {
		t.Fatal(e)
	}
	db.First(&run, run.ID)
	if run.ReportSnapshotJSON != first {
		t.Fatal("regeneration changed frozen snapshot")
	}
	name, _ := svc.ReportPath(run.ID, "html")
	html, e := os.ReadFile(name)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(html, []byte("before")) || bytes.Contains(html, []byte("never-persist-me")) {
		t.Fatal("HTML did not use sanitized frozen result")
	}
	var doc inspection.Report
	if e := json.Unmarshal([]byte(first), &doc); e != nil {
		t.Fatal(e)
	}
	if len(doc.Items) != 39 {
		t.Fatal("snapshot metadata lost")
	}
}

func TestOperationsCommandsRejectUnselectedServers(t *testing.T) {
	options := opsprobe.ManualOptions{Profiles: []string{"minio"}, MinIOAlias: "hospital"}
	if e := validateInspectionCommands(map[string]opsprobe.ManualOptions{"host:1": options}, nil); e == nil {
		t.Fatal("accepted commands for unselected server")
	}
	if e := validateInspectionCommands(map[string]opsprobe.ManualOptions{"host:1": options}, []InspectionTargetRequest{{Type: "host", ID: 1}}); e != nil {
		t.Fatal(e)
	}
	if e := validateInspectionCommands(map[string]opsprobe.ManualOptions{"database:1": options}, []InspectionTargetRequest{{Type: "database", ID: 1}}); e == nil {
		t.Fatal("host commands accepted for database target")
	}
}

func TestOperationsManualCommandsPersistWithTarget(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	server := model.Server{Host: "192.0.2.254", Port: 22, Username: "test", AuthType: "password"}
	if e := db.Create(&server).Error; e != nil {
		t.Fatal(e)
	}
	key := fmt.Sprintf("host:%d", server.ID)
	options := opsprobe.ManualOptions{Profiles: []string{"postgresql_archive"}, ArchiveDirectory: "/opt/PostgreSQL/14/archive"}
	run, e := svc.CreateInspection(InspectionRequest{Targets: []InspectionTargetRequest{{Type: "host", ID: server.ID}}, Commands: map[string]opsprobe.ManualOptions{key: options}}, "test")
	if e != nil {
		t.Fatal(e)
	}
	var target model.InspectionTargetRun
	if e := db.Where("run_id=?", run.ID).First(&target).Error; e != nil {
		t.Fatal(e)
	}
	var saved opsprobe.ManualOptions
	if e := json.Unmarshal([]byte(target.ManualJSON), &saved); e != nil {
		t.Fatal(e)
	}
	if saved.ArchiveDirectory != options.ArchiveDirectory || len(saved.Codes()) != 1 || saved.Codes()[0] != "cloudhis-19" {
		t.Fatalf("command mapping changed: %+v", saved)
	}
}

func TestOperationsManualOutputAndErrorReachHTMLAndPDF(t *testing.T) {
	entry := opsprobe.ExecuteManualCommand(context.Background(), "printf 'local-command-output\\n'; printf 'local-command-error\\n' >&2; exit 7")
	r := inspectionReport{Run: model.InspectionRun{ID: 28, Name: "本地执行链路自检（非服务器巡检结果）", Status: "PARTIAL", Operator: "local-test"}, Targets: []inspectionReportTarget{{Target: model.InspectionTargetRun{ID: 1, Type: "host", Name: "本机测试进程", ServerEndpoint: "local process", Status: "COMPLETED", ManualJSON: mustJSON(opsprobe.ManualOptions{Profiles: []string{"k8s_host"}})}, Result: opsprobe.JobResult{Version: opsprobe.Version, StartedAt: entry.StartedAt, FinishedAt: entry.FinishedAt, ReportChecks: []inspection.Observation{{Code: "cloudhis-6", Name: "主机命令执行记录", Status: "UNKNOWN", Value: "本地测试仅验证记录链路", Executions: []inspection.Execution{entry}}}}}}}
	r.Snapshot = r.Document()
	var html bytes.Buffer
	if e := inspectionHTML.Execute(&html, r); e != nil {
		t.Fatal(e)
	}
	for _, needle := range []string{"local-command-error", "local process"} {
		if !bytes.Contains(html.Bytes(), []byte(needle)) {
			t.Fatalf("execution evidence absent from HTML: %s", needle)
		}
	}
	if bytes.Contains(html.Bytes(), []byte("local-command-output")) || !strings.Contains(r.Snapshot.Objects[0].Observations[0].Executions[0].Stdout, "local-command-output") || r.Snapshot.Objects[0].Observations[0].Executions[0].ExitCode != 7 {
		t.Fatal("compact report changed or printed full raw evidence")
	}
	if os.Getenv("DFOPS_REPORT_FONT") == "" {
		t.Skip("PDF font required")
	}
	pdf, e := renderInspectionPDF(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	if dir := os.Getenv("DFOPS_OPERATIONS_QA_DIR"); dir != "" {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		for name, data := range map[string][]byte{"actual-local-command-record.html": html.Bytes(), "actual-local-command-record.pdf": pdf, "actual-local-command-record.json": []byte(mustJSON(r.Snapshot))} {
			if e := os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestOperationsComponentSummaryUsesGuideResultAndPreservesActualCommand(t *testing.T) {
	checks, err := opsprobe.CollectManual(context.Background(), opsprobe.ManualOptions{Profiles: []string{"deploy"}}, nil, func(ctx context.Context, _ string) inspection.Execution {
		return opsprobe.ExecuteManualCommand(ctx, "printf 'REPOSITORY TAG IMAGE_ID CREATED SIZE\\nrepo/prod/api v3 c today 1GB\\nrepo/prod/api v2 b yesterday 1GB\\nrepo/prod/api v1 a earlier 1GB\\n'")
	})
	if err != nil || len(checks) != 1 || checks[0].Status != "WARNING" {
		t.Fatalf("local guide interpretation failed %v %+v", err, checks)
	}
	r := inspectionReport{Run: model.InspectionRun{ID: 29, Name: "本地组件汇总链路自检（非客户巡检结果）"}, Targets: []inspectionReportTarget{{Target: model.InspectionTargetRun{ID: 1, Name: "本机测试进程", Type: "host", Status: "COMPLETED", ManualJSON: mustJSON(opsprobe.ManualOptions{Profiles: []string{"deploy"}})}, Result: opsprobe.JobResult{ReportChecks: checks}}}}
	r.Snapshot = r.Document()
	var html bytes.Buffer
	if err := inspectionHTML.Execute(&html, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"组件运行情况汇总", "prod 镜像共 3 个", "repo/prod/api", "未执行"} {
		if !strings.Contains(html.String(), want) {
			t.Fatal("missing component evidence", want)
		}
	}
	if os.Getenv("DFOPS_REPORT_FONT") == "" {
		t.Skip("PDF font required")
	}
	pdf, err := renderInspectionPDF(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("DFOPS_OPERATIONS_QA_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"component-record.html": html.Bytes(), "component-record.pdf": pdf} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestOperationsManualResultReturnsOverSSHAndGeneratesReport(t *testing.T) {
	if os.Getenv("DFOPS_REPORT_FONT") == "" {
		t.Skip("PDF font required")
	}
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	host, port := fakeOpsSSH(t)
	credential, e := crypto.Encrypt("fixture-password")
	if e != nil {
		t.Fatal(e)
	}
	server := model.Server{Host: host, Port: port, Username: "fixture", AuthType: "password", CredentialEncrypted: credential}
	if e := db.Create(&server).Error; e != nil {
		t.Fatal(e)
	}
	options := opsprobe.ManualOptions{Profiles: []string{"k8s_host"}}
	run, e := svc.CreateInspection(InspectionRequest{Name: "本地 SSH 回收链路自检", Targets: []InspectionTargetRequest{{Type: "host", ID: server.ID}}, Commands: map[string]opsprobe.ManualOptions{fmt.Sprintf("host:%d", server.ID): options}}, "local-test")
	if e != nil {
		t.Fatal(e)
	}
	entry := opsprobe.ExecuteManualCommand(context.Background(), "printf 'ssh-returned-output\\n'; printf 'ssh-returned-error\\n' >&2; exit 7")
	result := opsprobe.JobResult{Version: opsprobe.Version, StartedAt: entry.StartedAt, FinishedAt: entry.FinishedAt, ReportChecks: []inspection.Observation{{Code: "cloudhis-6", Status: "UNKNOWN", Value: "本地执行记录经 SSH 回收", Executions: []inspection.Execution{entry}}}}
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "result.json"), []byte(mustJSON(result)), 0600); e != nil {
		t.Fatal(e)
	}
	if e := db.Model(&model.InspectionTargetRun{}).Where("run_id=?", run.ID).Update("remote_directory", dir).Error; e != nil {
		t.Fatal(e)
	}
	if e := db.Model(run).Updates(map[string]any{"status": "RUNNING", "lease_owner": svc.owner}).Error; e != nil {
		t.Fatal(e)
	}
	svc.processInspection(context.Background(), run.ID)
	if e := db.First(run, run.ID).Error; e != nil {
		t.Fatal(e)
	}
	if run.Status != "PARTIAL" || run.HTMLStatus != "READY" || run.PDFStatus != "READY" {
		t.Fatalf("SSH/report pipeline failed %+v", run)
	}
	file, _ := svc.ReportPath(run.ID, "html")
	content, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(content, []byte("ssh-returned-output")) || !bytes.Contains(content, []byte("ssh-returned-error")) || !strings.Contains(run.ReportSnapshotJSON, "ssh-returned-output") {
		t.Fatal("SSH evidence was lost or raw output leaked into the report body")
	}
}

func TestOperationsReportV2RendersStructuredEvidence(t *testing.T) {
	now := time.Date(2026, 10, 27, 19, 0, 0, 0, time.FixedZone("CST", 8*3600))
	latency := 4.2
	result := opsprobe.JobResult{Version: opsprobe.Version, StartedAt: now, FinishedAt: now.Add(time.Second), Metrics: &opsprobe.HostMetrics{Source: "node_exporter", SourceURL: "http://example.invalid:9100/metrics", CollectedAt: now, CPUCores: 8, CPUUsagePercent: 26, MemTotalBytes: 32 << 30, MemAvailableBytes: 20 << 30, MemUsagePercent: 37.5, Disks: []opsprobe.Disk{{Device: "/dev/mapper/data", Mountpoint: "/data", TotalBytes: 1000 << 30, AvailableBytes: 100 << 30, UsedPercent: 90, Inodes: 1000000, InodeUsedPercent: 87}}, DiskIO: []opsprobe.DiskIO{{Device: "dm-0", ReadAwaitMs: &latency, ReadIOPS: 20, WriteIOPS: 30, Utilization: 45}}}}
	result.Checks = opsprobe.HostChecks(result.Metrics)
	database := opsprobe.JobResult{Version: opsprobe.Version, StartedAt: now, FinishedAt: now.Add(time.Second), ReportChecks: []inspection.Observation{
		{Code: "cloudhis-20", Status: "NORMAL", Value: "PostgreSQL 18 / primary", Source: "只读 SQL", CollectedAt: now, Tables: []inspection.Table{{Title: "实例身份", Columns: []string{"版本", "数据库", "采集账号", "启动时间", "主备角色"}, Rows: [][]string{{"PostgreSQL 18", "example", "monitor", "2026-10-01 08:00:00", "primary"}}}}},
		{Code: "cloudhis-27", Status: "WARNING", Value: "客户端连接 85 / 容量 100", Threshold: "80% 提醒，95% 严重", Advice: "核对连接池与空闲事务。", Source: "pg_stat_activity / pg_settings", CollectedAt: now, Tables: []inspection.Table{{Columns: []string{"最大连接", "客户端连接", "使用率"}, Rows: [][]string{{"100", "85", "85%"}}}}},
		{Code: "cloudhis-30", Status: "WARNING", Value: "一条事务超过 30 分钟", Source: "pg_stat_activity", CollectedAt: now, Tables: []inspection.Table{{Columns: []string{"PID", "数据库", "账号", "客户端", "应用", "状态", "开始时间", "事务秒数", "当前或最近 SQL"}, Rows: [][]string{{"123", "example", "app", "192.0.2.10", "HIS", "idle in transaction", "2026-10-27 18:00:00", "3600", "SELECT '<script>alert(1)</script>'; -- " + strings.Repeat("较长的 SQL 证据用于检查换行 ", 30)}}}}},
	}}
	r := inspectionReport{Run: model.InspectionRun{ID: 20261027, Name: "本地模拟 HIS 巡检报告", Operator: "本地验证"}, Targets: []inspectionReportTarget{{Target: model.InspectionTargetRun{ID: 1, ServerID: 1, Type: "host", Name: "示例数据库主机", Endpoint: "192.0.2.1", Status: "COMPLETED"}, Result: result}, {Target: model.InspectionTargetRun{ID: 2, Type: "database", Name: "示例 PostgreSQL", Endpoint: "192.0.2.1:5432/example", Status: "COMPLETED"}, Result: database}}}
	r.Snapshot = r.Document()
	r.Snapshot.GeneratedAt = now.Add(2 * time.Second)
	var html bytes.Buffer
	if e := inspectionHTML.Execute(&html, r); e != nil {
		t.Fatal(e)
	}
	for _, needle := range []string{"39 项检查目录", "已取得结果", "cloudhis-39", "pg_stat_activity"} {
		if !bytes.Contains(html.Bytes(), []byte(needle)) {
			t.Fatalf("missing HTML section %s", needle)
		}
	}
	if bytes.Contains(html.Bytes(), []byte("<script>alert")) {
		t.Fatal("unescaped HTML evidence")
	}
	if os.Getenv("DFOPS_REPORT_FONT") == "" {
		t.Skip("set DFOPS_REPORT_FONT to render PDF")
	}
	pdf, e := renderInspectionPDF(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("invalid PDF")
	}
	if dir := os.Getenv("DFOPS_OPERATIONS_QA_DIR"); dir != "" {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		for name, data := range map[string][]byte{"inspection-report-v2.html": html.Bytes(), "inspection-report-v2.pdf": pdf, "inspection-report-v2.json": []byte(mustJSON(r.Snapshot))} {
			if e := os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
}
