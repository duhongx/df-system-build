package service

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/repository"
	"df-build-server/pkg/crypto"
	"github.com/jackc/pgx/v5"
)

func TestConfiguredPostgreSQLCollectsActualGuideSQLWithoutAnSSHHost(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	t.Setenv("DF_ENCRYPT_KEY", "configured-test-key-32-characters")
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	password, err := crypto.Encrypt(cfg.Password)
	if err != nil {
		t.Fatal(err)
	}
	row := model.OperationsDatabase{Name: "actual-pg-inspection-" + time.Now().Format("150405.000000000"), Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, PasswordEncrypted: password, SSLMode: "disable", Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.CollectDatabaseInspection(context.Background(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ReportChecks) < 19 || result.Database == nil {
		t.Fatalf("incomplete SQL collection: %+v", result)
	}
	for _, check := range result.ReportChecks {
		if check.Code == "cloudhis-4" || check.Code == "cloudhis-15" || check.Code == "cloudhis-17" || check.Code == "cloudhis-18" || check.Code == "cloudhis-19" {
			if check.Status != "UNKNOWN" {
				t.Fatal("unavailable host facts treated as inspected")
			}
			continue
		}

		if check.Status == "UNKNOWN" {
			t.Fatalf("%s: %s / %v", check.Code, check.Reason, check.Gaps)
		}
	}
	sample, err := svc.LatestCollection(context.Background(), "database", row.ID)
	if err != nil || sample == nil || sample.Revision != row.Revision {
		t.Fatalf("missing latest snapshot: %v", err)
	}
}

func TestConfiguredConnectionsRetentionAndFrozenReports(t *testing.T) {
	db := operationsTestDB(t)
	oldDB := repository.DB
	repository.DB = db
	t.Cleanup(func() { repository.DB = oldDB })
	t.Setenv("DF_ENCRYPT_KEY", "configured-test-key-32-characters")
	svc := NewOperationsService(db, t.TempDir())
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")
	row, err := svc.SaveComponent(ctx, ComponentRequest{Kind: "redis", Name: "redis-" + suffix, Endpoint: "redis://127.0.0.1:6379", Secret: "do-not-return-this", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "do-not-return-this") || row.SecretEncrypted == "do-not-return-this" {
		t.Fatal("credential exposed")
	}
	updated, err := svc.SaveComponent(ctx, ComponentRequest{ID: row.ID, Kind: "redis", Name: row.Name, Endpoint: row.Endpoint, Enabled: true})
	if err != nil || updated.Revision != 2 || updated.SecretEncrypted != row.SecretEncrypted {
		t.Fatalf("credential/revision preservation: %v", err)
	}
	if _, err := svc.SaveComponent(ctx, ComponentRequest{ID: row.ID, Kind: "redis", Name: row.Name, Endpoint: "redis://user:password@host:6379", Enabled: true}); err == nil {
		t.Fatal("accepted embedded password")
	}
	host := model.Server{Host: "127.0.0.1", Username: "fixture", Remark: "host-" + suffix, CreatedBy: "owner", AuthType: "password"}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateConfiguredInspection(ctx, "巡检测试", "operator", []InspectionTargetRequest{{Type: "host", ID: host.ID}, {Type: "component", ID: row.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var targets []model.InspectionTargetRun
	db.Where("run_id=?", run.ID).Order("id").Find(&targets)
	var manual opsprobe.ManualOptions
	if len(targets) > 0 {
		_ = json.Unmarshal([]byte(targets[0].ManualJSON), &manual)
	}
	if len(targets) != 2 || !manual.Auto || targets[1].ConfigRevision != 2 {
		t.Fatalf("wrong resolved scope: %+v", targets)
	}
	now := time.Now()
	result := opsprobe.JobResult{Type: "redis", StartedAt: now, FinishedAt: now, ReportChecks: []inspection.Observation{{Code: "redis-info", Name: "Redis 运行", Status: "NORMAL", Value: "连接正常"}}}
	db.Model(&targets[0]).Updates(map[string]any{"status": "COMPLETED", "result_json": mustJSON(opsprobe.JobResult{Type: "host", Checks: []opsprobe.Check{{Code: "host_cpu", Name: "CPU", Status: "NORMAL", Value: "2%"}}})})
	db.Model(&targets[1]).Updates(map[string]any{"status": "COMPLETED", "result_json": mustJSON(result)})
	if err := svc.GenerateInspectionReports(ctx, run.ID); err != nil && os.Getenv("DFOPS_REPORT_FONT") != "" {
		t.Fatal(err)
	}
	db.First(run, run.ID)
	frozen := run.ReportSnapshotJSON
	if frozen == "" {
		t.Fatal("no frozen report")
	}
	before := time.Now().Add(-49 * time.Hour)
	db.Create(&model.CollectionSample{TargetType: "component", TargetID: row.ID, Revision: 1, CollectedAt: before, ResultJSON: mustJSON(result)})
	if sample, err := svc.LatestCollection(ctx, "component", row.ID); err != nil || sample != nil {
		t.Fatal("expired sample exposed")
	}
	if err := svc.rememberCollection("component", row.ID, 2, &result); err != nil {
		t.Fatal(err)
	}
	db.Create(&model.HostMetricSample{ServerID: host.ID, CollectedAt: before, MetricsJSON: "{}"})
	db.Create(&model.DatabaseLogEvent{DatabaseID: 90231029, SourceKey: "retention-" + suffix, Type: "slow_sql", EventTime: before, CreatedAt: time.Now()})
	svc.cleanupOperations(ctx)
	var count int64
	db.Model(&model.CollectionSample{}).Where("target_id=? AND collected_at<?", row.ID, time.Now().Add(-48*time.Hour)).Count(&count)
	if count != 0 {
		t.Fatal("expired component samples retained")
	}
	db.Model(&model.HostMetricSample{}).Where("server_id=?", host.ID).Count(&count)
	if count != 0 {
		t.Fatal("expired host samples retained")
	}
	db.Model(&model.DatabaseLogEvent{}).Where("source_key=?", "retention-"+suffix).Count(&count)
	if count != 0 {
		t.Fatal("expired database log retained")
	}
	db.First(run, run.ID)
	if run.ReportSnapshotJSON != frozen {
		t.Fatal("48h cleanup touched archived report")
	}
	if err := svc.GenerateInspectionReports(ctx, run.ID); err != nil && os.Getenv("DFOPS_REPORT_FONT") != "" {
		t.Fatal(err)
	}
	db.First(run, run.ID)
	if run.ReportSnapshotJSON != frozen {
		t.Fatal("regeneration recollected data")
	}
	if dest := os.Getenv("DFOPS_CONFIGURED_REPORT_FIXTURE"); dest != "" {
		for _, ext := range []string{"html", "pdf"} {
			name, _ := svc.ReportPath(run.ID, ext)
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dest, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "configured-report."+ext), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestManagedServicePermissionsAndConcurrentOperationGuard(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")
	owner := "owner-" + suffix
	maintainer := "maintainer-" + suffix
	db.Create(&model.User{Username: maintainer, PasswordHash: "fixture"})
	host := model.Server{Host: "127.0.0.1", Port: 1, Username: "fixture", CreatedBy: owner, AuthType: "password"}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	req := ManagedServiceInput{Name: "SkyWalking OAP", Mode: "script", RunUser: "root", Directory: "/opt/skywalking", StartScript: "/opt/skywalking/bin/oap.sh", Identity: "org.apache.skywalking.oap.server.starter.OAPServerStartUp", Operators: []string{maintainer}}
	if _, err := svc.SaveManagedService(ctx, host.ID, "stranger", req); err == nil {
		t.Fatal("unauthorized configuration accepted")
	}
	row, err := svc.SaveManagedService(ctx, host.ID, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	if !ServiceCanOperate(*row, maintainer) || ServiceCanConfigure(*row, maintainer) || ServiceCanOperate(*row, "stranger") {
		t.Fatal("incorrect permission boundary")
	}
	if _, err := svc.SaveManagedService(ctx, host.ID, owner, req); err == nil {
		t.Fatal("duplicate process configuration accepted")
	}
	if _, err := svc.StartServiceOperation(ctx, row.ID, "start", "stranger"); err == nil {
		t.Fatal("unauthorized action accepted")
	}
	op := model.ServiceOperation{ServiceID: row.ID, ServerID: host.ID, Action: "start", Operator: owner, Status: "RUNNING", StartedAt: time.Now()}
	db.Create(&op)
	if _, err := svc.StartServiceOperation(ctx, row.ID, "restart", maintainer); err == nil {
		t.Fatal("overlapping action accepted")
	}
	req.ID = row.ID
	if _, err := svc.SaveManagedService(ctx, host.ID, owner, req); err == nil {
		t.Fatal("running configuration changed")
	}
	if err := svc.DeleteManagedService(ctx, row.ID, owner); err == nil {
		t.Fatal("running service deleted")
	}
	db.Model(&op).Update("status", "UNKNOWN")
	if _, err := svc.StartServiceOperation(ctx, row.ID, "start", maintainer); err == nil {
		t.Fatal("uncertain operation immediately retried")
	}
}

func TestManagedProcessScriptQuotesConfigurationAndHasValidShellSyntax(t *testing.T) {
	row := model.ManagedService{Mode: "script", RunUser: "root", Directory: "/opt/test space's", StartScript: "/opt/test space's/bin/start.sh", Identity: "org.example.OAPServerStartUp"}
	for _, action := range []string{"start", "restart", "status"} {
		script := serviceProcessScript(row, action)
		cmd := exec.Command("/bin/sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %s / %v", action, out, err)
		}
		if strings.Contains(script, "pkill") || strings.Contains(script, "pgrep") || !strings.Contains(script, "grep -Fqx") {
			t.Fatal("broad process identification")
		}
	}
}

func TestSystemdServiceRequiresARunningMainProcess(t *testing.T) {
	for _, v := range []struct{ text, want string }{{"LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=123", "RUNNING"}, {"LoadState=loaded\nActiveState=active\nSubState=exited\nMainPID=0", "UNKNOWN"}, {"LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0", "STOPPED"}, {"LoadState=not-found\nActiveState=inactive", "UNKNOWN"}} {
		if got := systemdServiceState(v.text); got != v.want {
			t.Fatalf("%s: %s", v.text, got)
		}
	}
}

func TestManagedServiceDoesNotRedirectQueuedOperationAfterHostEdit(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	server := model.Server{Host: "192.0.2.10", Port: 22, Username: "fixture", AuthType: "password"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	row := model.ManagedService{Name: "SkyWalking", ServerID: server.ID, Mode: "systemd", Unit: "skywalking.service", CreatedBy: "owner", OperatorsJSON: "[]"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	op := model.ServiceOperation{ServiceID: row.ID, ServerID: server.ID, TargetName: row.Name, TargetEndpoint: serverEndpoint(server), ConfigurationJSON: mustJSON(map[string]string{"unit": row.Unit}), Action: "restart", Status: "QUEUED", Operator: "owner", StartedAt: time.Now()}
	if err := db.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&server).Update("host", "192.0.2.20").Error; err != nil {
		t.Fatal(err)
	}
	svc.runServiceOperation(row, op, model.Server{ID: server.ID, Host: "192.0.2.10", Port: 22, Username: "fixture"})
	db.First(&op, op.ID)
	if op.Status != "FAILED" || !strings.Contains(op.Message, "目标已变化") {
		t.Fatalf("queued operation followed edited host: %+v", op)
	}
	if !strings.Contains(op.ConfigurationJSON, "skywalking.service") || op.TargetEndpoint != "fixture@192.0.2.10:22" {
		t.Fatal("operation target audit lost")
	}
}
