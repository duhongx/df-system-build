package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/repository"
	"df-build-server/pkg/crypto"
)

func TestExpectedCoverageSurvivesEmptyAndPartialCollectors(t *testing.T) {
	for _, observations := range [][]inspection.Observation{nil, {{Code: "redis-info", Status: "NORMAL"}}} {
		r := opsprobe.JobResult{Type: "redis", ExpectedChecks: inspection.RequiredCodes("redis"), ReportChecks: observations}
		setCollectionCoverage(&r)
		if r.Coverage.Total != 2 || r.Coverage.Planned != 2 || r.Coverage.Completed != len(observations) || r.Coverage.Unknown != 2-len(observations) || r.Coverage.NotRun != 0 {
			t.Fatalf("omitted checks disappeared: %+v", r.Coverage)
		}
	}
}

func TestEnvironmentConnectionReferencesSettingsAndRejectsStaleTargets(t *testing.T) {
	db := operationsTestDB(t)
	old := repository.DB
	repository.DB = db
	t.Cleanup(func() { repository.DB = old })
	t.Setenv("DF_ENCRYPT_KEY", "configured-test-key-32-characters")
	repo := repository.NewSettingsRepo()
	keys := []string{"postgresql_host", "postgresql_port", "postgresql_database", "postgresql_admin_password"}
	prior := map[string]string{}
	for _, key := range keys {
		prior[key], _ = repo.Read(key, false)
	}
	t.Cleanup(func() { _ = repo.BatchUpdate(prior) })
	if err := repo.BatchUpdate(map[string]string{"postgresql_host": "192.0.2.21", "postgresql_port": "5432", "postgresql_database": "report_fixture", "postgresql_admin_password": "first-secret"}); err != nil {
		t.Fatal(err)
	}
	password, _ := crypto.Encrypt("independent-secret")
	independent := model.OperationsDatabase{Name: "independent-" + time.Now().Format("150405.000000000"), Host: "192.0.2.21", Port: 5432, Database: "report_fixture", Username: "postgres", PasswordEncrypted: password, Revision: 1}
	if err := db.Create(&independent).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewOperationsService(db, t.TempDir())
	row, err := svc.SyncEnvironmentDatabase(context.Background(), true, "")
	if err != nil || row == nil {
		t.Fatalf("create reference: %v", err)
	}
	t.Cleanup(func() { db.Delete(&model.OperationsDatabase{}, []uint{row.ID, independent.ID}) })
	if row.ID == independent.ID || row.PasswordEncrypted != "" || row.Source != "environment" {
		t.Fatal("independent connection adopted or credential copied")
	}
	stale := *row
	if err := repo.Set("postgresql_admin_password", "second-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.databaseConfig(stale); err == nil {
		t.Fatal("stale reference silently used new settings")
	}
	updated, err := svc.SyncEnvironmentDatabase(context.Background(), false, "")
	if err != nil || updated.ID != row.ID || updated.Revision != row.Revision+1 || updated.PasswordEncrypted != "" {
		t.Fatalf("sync: %v", err)
	}
	cfg, err := svc.databaseConfig(*updated)
	if err != nil || cfg.Password != "second-secret" {
		t.Fatal("reference did not use current credential")
	}
	var actual model.OperationsDatabase
	if err := db.First(&actual, independent.ID).Error; err != nil || actual.Revision != 1 || actual.PasswordEncrypted != password {
		t.Fatal("independent connection changed")
	}
}

func TestHostCoverageBelongsToExactRunAndMember(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	run := model.InspectionRun{Name: "coverage", Status: "RUNNING"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	h := model.Server{Host: "192.0.2.41", Remark: "cluster-a"}
	other := model.Server{Host: "192.0.2.42", Remark: "cluster-b"}
	db.Create(&h)
	db.Create(&other)
	result := opsprobe.JobResult{Type: "kubernetes", RelatedHosts: []opsprobe.RelatedHostResult{{ServerID: h.ID, Result: &opsprobe.JobResult{}}}}
	db.Create(&model.InspectionTargetRun{RunID: run.ID, Type: "component", Status: "COMPLETED", ResultJSON: mustJSON(result)})
	for _, test := range []struct {
		run, host uint
		want      int
	}{{run.ID, h.ID, 1}, {run.ID, other.ID, 0}, {run.ID + 90000, h.ID, 0}} {
		opt := opsprobe.ManualOptions{ConfiguredComponents: []string{"kubernetes"}}
		svc.applyCollectedHostCoverage(test.run, test.host, &opt)
		if len(opt.ConfiguredComponents) != test.want {
			t.Fatalf("coverage leaked: %+v", test)
		}
	}
	matched, err := svc.matchingServers(context.Background(), []string{h.Host}, other.ID)
	if err != nil || len(matched) != 1 || matched[0].ID != other.ID {
		t.Fatal("explicit association did not uniquely select its host")
	}
}

func TestRelatedResourceSummaryDoesNotDuplicateFactsAndKeepsSource(t *testing.T) {
	r := &opsprobe.JobResult{}
	facts := &opsprobe.JobResult{Metrics: &opsprobe.HostMetrics{CPUCores: 2, MemTotalBytes: 1 << 30}, ReportChecks: []inspection.Observation{{Code: "cloudhis-6", Status: "NORMAL", Value: "duplicate"}}}
	appendRelatedHost(r, model.Server{ID: 23, Host: "192.0.2.23"}, facts, []string{"cloudhis-6"})
	if len(r.ReportChecks) != 1 || r.ReportChecks[0].Status != "UNKNOWN" || r.ReportChecks[0].Origin.ID != 23 {
		t.Fatalf("source or incomplete resources lost: %+v", r.ReportChecks)
	}
}

func TestStoppingExistingPlanPreservesScopeAndRunningTask(t *testing.T) {
	db := operationsTestDB(t)
	next := time.Now().Add(time.Hour)
	plan := model.InspectionPlan{Name: "existing", Enabled: true, DailyTime: "10:30", Timezone: "Asia/Shanghai", TargetsJSON: `[{"type":"host","id":123}]`, NextRunAt: &next}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	run := model.InspectionRun{Name: "already running", PlanID: plan.ID, Status: "RUNNING"}
	db.Create(&run)
	svc := NewOperationsService(db, t.TempDir())
	if err := svc.SetInspectionPlanEnabled(context.Background(), plan.ID, false); err != nil {
		t.Fatal(err)
	}
	var got model.InspectionPlan
	db.First(&got, plan.ID)
	db.First(&run, run.ID)
	var actualScope, originalScope any
	_ = json.Unmarshal([]byte(got.TargetsJSON), &actualScope)
	_ = json.Unmarshal([]byte(plan.TargetsJSON), &originalScope)
	if got.Enabled || mustJSON(actualScope) != mustJSON(originalScope) || run.Status != "RUNNING" {
		t.Fatal("stopping plan changed scope or interrupted current task")
	}
}

func TestMinIODailyRecordsRetainConflictsGapsAndMidnightTimes(t *testing.T) {
	valid := minioDailyObservation("2026-10-27 00:00:00 100\n2026-10-28 105\n")
	if valid.Status != "RECORDED" || valid.Tables[0].Rows[1][2] != "5" {
		t.Fatalf("midnight records lost: %+v", valid)
	}
	for _, data := range []string{"2026-10-27 100\n2026-10-29 105", "2026-10-27 100\n2026-10-27 101\n2026-10-28 105"} {
		if o := minioDailyObservation(data); o.Status != "UNKNOWN" || len(o.Gaps) == 0 {
			t.Fatal("incomplete/conflicting daily records treated as complete")
		}
	}
}

func TestMinIOMembersComeFromAdminInfoNotTheMCExecutor(t *testing.T) {
	o := opsprobe.InspectMinIOArchitectureText("● 192.0.2.101:9000\n Uptime: 1 day\n Drives: 1/1 OK\n● 192.0.2.102:9000\n Drives: 1/1 OK\n")
	addresses := minioMemberAddresses([]inspection.Observation{o})
	if len(addresses) != 2 || addresses[0] != "192.0.2.101" || addresses[1] != "192.0.2.102" {
		t.Fatalf("member addresses lost: %v", addresses)
	}
	if len(minioMemberAddresses(nil)) != 0 {
		t.Fatal("executor was inferred without members")
	}
}

func TestRegisteredNetworkComponentsDoNotRaiseDuplicateMissingSourceOnTheirHost(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	run := model.InspectionRun{Name: "linked components", Status: "RUNNING"}
	db.Create(&run)
	for _, kind := range []string{"redis", "rabbitmq", "elasticsearch"} {
		r := opsprobe.JobResult{Type: kind, ExpectedChecks: inspection.RequiredCodes(kind)}
		r.ReportChecks = inspection.EnsureResults(r.ExpectedChecks, nil, "已登记连接但账号无读取权限")
		if err := db.Create(&model.InspectionTargetRun{RunID: run.ID, ServerID: 12345, Type: "component", Status: "COMPLETED", ResultJSON: mustJSON(r)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		host uint
		want int
	}{{12345, 3}, {12346, 0}} {
		var opt opsprobe.ManualOptions
		svc.applyCollectedHostCoverage(run.ID, test.host, &opt)
		if len(opt.ConfiguredComponents) != test.want {
			t.Fatalf("registered results duplicated as missing sources on host %d: %v", test.host, opt.ConfiguredComponents)
		}
	}
}
