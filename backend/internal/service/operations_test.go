package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

func operationsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.OpenGormPostgres(t)
	if err := db.AutoMigrate(&model.Server{}, &model.HostMonitorConfig{}, &model.HostMetricSample{}, &model.OperationsDatabase{}, &model.DatabaseLogCursor{}, &model.DatabaseLogEvent{}, &model.InspectionPlan{}, &model.InspectionRun{}, &model.InspectionTargetRun{}); err != nil {
		t.Fatal(err)
	}
	return db
}
func fakeOpsSSH(t *testing.T) (string, int) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() != "fixture" || string(p) != "fixture-password" {
			return nil, fmt.Errorf("bad fixture credential")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var connections []net.Conn
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, raw)
			mu.Unlock()
			go func() {
				defer raw.Close()
				_, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for ch := range channels {
					channel, reqs, e := ch.Accept()
					if e != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range reqs {
							switch req.Type {
							case "subsystem":
								_ = req.Reply(true, nil)
								server, e := sftp.NewServer(channel)
								if e == nil {
									_ = server.Serve()
									_ = server.Close()
								}
								return
							case "exec":
								var command struct{ Value string }
								_ = ssh.Unmarshal(req.Payload, &command)
								_ = req.Reply(true, nil)
								code := uint32(0)
								if strings.HasPrefix(command.Value, "stat -Lc ") {
									_, quoted, _ := strings.Cut(command.Value, " -- ")
									name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(quoted, "'"), "'"), "'\"'\"'", "'")
									st, e := os.Stat(name)
									if e != nil {
										code = 1
										io.WriteString(channel.Stderr(), e.Error())
									} else {
										sys := st.Sys().(*syscall.Stat_t)
										fmt.Fprintf(channel, "%d:%d %d %d\n", sys.Dev, sys.Ino, st.Size(), st.ModTime().Unix())
									}
								}
								channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
								return
							default:
								_ = req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range connections {
			c.Close()
		}
	})
	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}
func csvOpsRecord(sql string) []byte {
	row := make([]string, 26)
	row[0] = time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05.000 MST")
	row[1] = "fixture-role"
	row[2] = "fixture-db"
	row[3] = "4321"
	row[11] = "LOG"
	row[13] = "duration: 6000.001 ms statement: " + sql
	row[22] = "fixture-app"
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(row)
	w.Flush()
	return b.Bytes()
}
func TestOperationsLogIngestionResumesRotatesAndDeduplicates(t *testing.T) {
	db := operationsTestDB(t)
	host, port := fakeOpsSSH(t)
	password, _ := crypto.Encrypt("fixture-password")
	server := model.Server{Host: host, Port: port, Username: "fixture", AuthType: "password", CredentialEncrypted: password, ConnTimeout: 3}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := filepath.Join(dir, "postgres 'daily'.csv")
	first := csvOpsRecord("SELECT '第一条,包含\n换行'")
	second := csvOpsRecord("SELECT 'second'")
	if err := os.WriteFile(name, append(append([]byte{}, first...), second[:len(second)/2]...), 0600); err != nil {
		t.Fatal(err)
	}
	row := model.OperationsDatabase{Name: "fixture", Host: "127.0.0.1", Port: 5432, Database: "fixture", Username: "fixture", PasswordEncrypted: password, ServerID: server.ID, LogEnabled: true, LogDirectory: dir, LogFormat: "csv", LogTimezone: "Asia/Shanghai", SlowThresholdMs: 5000, Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewOperationsService(db, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	check := func(want int64) {
		t.Helper()
		var n int64
		db.Model(&model.DatabaseLogEvent{}).Where("database_id=?", row.ID).Count(&n)
		if n != want {
			t.Fatalf("events %d want %d", n, want)
		}
	}
	if err := svc.syncDatabaseLogs(ctx, row); err != nil {
		t.Fatal(err)
	}
	check(1)
	events, total, err := svc.ListEvents(ctx, row.ID, 1, 30, OperationsEventFilter{Type: "slow_sql", Keyword: "第一条", MinDuration: 5000})
	if err != nil || total != 1 || len(events) != 1 {
		t.Fatalf("event listing failed: total=%d rows=%d err=%v", total, len(events), err)
	}
	if events[0].PID != 4321 || events[0].SQL != "SELECT '第一条,包含\n换行'" || events[0].Raw != "" {
		t.Fatalf("event projection lost fields or exposed raw data: %+v", events[0])
	}
	if err := svc.syncDatabaseLogs(ctx, row); err != nil {
		t.Fatal(err)
	}
	check(1)
	f, _ := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0600)
	f.Write(second[len(second)/2:])
	f.Close()
	if err := svc.syncDatabaseLogs(ctx, row); err != nil {
		t.Fatal(err)
	}
	check(2)
	if err := os.Rename(name, filepath.Join(dir, "rotated.csv")); err != nil {
		t.Fatal(err)
	}
	if err := svc.syncDatabaseLogs(ctx, row); err != nil {
		t.Fatal(err)
	}
	check(2)
	if err := os.WriteFile(name, csvOpsRecord("SELECT 'new file'"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.syncDatabaseLogs(ctx, row); err != nil {
		t.Fatal(err)
	}
	check(3)
	replacement := bytes.ReplaceAll(csvOpsRecord("SELECT 'replacement'"), []byte("fixture-role"), []byte("changed-role"))
	replacement = append(replacement, csvOpsRecord("SELECT 'extra'")...)
	if err := os.WriteFile(name, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.syncDatabaseLogs(ctx, row); err != nil {
		t.Fatal(err)
	}
	check(5)
	var config model.HostMonitorConfig
	if err := db.First(&config, server.ID).Error; err != nil || !strings.HasPrefix(config.SSHFingerprint, "SHA256:") {
		t.Fatalf("host identity not pinned: %+v %v", config, err)
	}
	db.Model(&config).Update("ssh_fingerprint", "SHA256:changed-fixture-key")
	if err := svc.syncDatabaseLogs(ctx, row); err == nil || !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("changed identity accepted: %v", err)
	}
}
func TestOperationsDatabaseLocksAndReadonlyConnections(t *testing.T) {
	db := operationsTestDB(t)
	dsn, _ := testutil.PostgresDSNFromEnv()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(context.Background(), "CREATE TABLE ops_lock_fixture(id int primary key,value int); INSERT INTO ops_lock_fixture VALUES(1,0)"); err != nil {
		t.Fatal(err)
	}
	blocker, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(context.Background())
	if _, err := blocker.Exec(context.Background(), "BEGIN; UPDATE ops_lock_fixture SET value=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	waiter, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := waiter.Exec(ctx, "UPDATE ops_lock_fixture SET value=2 WHERE id=1"); done <- e }()
	defer func() { cancel(); <-done }()
	probeConfig := opsprobe.DatabaseConfig{Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, Password: cfg.Password, SSLMode: "disable"}
	var snapshot *opsprobe.Diagnostic
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		snapshot, err = opsprobe.Diagnose(ctx, probeConfig)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, a := range snapshot.Activities {
			if a.PID == int(waiter.PgConn().PID()) && len(a.BlockingPIDs) > 0 && a.ElapsedSeconds >= 5 {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	found, lockFound := false, false
	for _, a := range snapshot.Activities {
		if a.PID == int(waiter.PgConn().PID()) && a.State == "active" && len(a.BlockingPIDs) > 0 {
			found = true
			if a.BlockingPIDs[0] != int(blocker.PgConn().PID()) {
				t.Fatalf("wrong blocker %+v", a)
			}
		}
	}
	for _, l := range snapshot.Locks {
		if !l.Granted && l.PID == int(waiter.PgConn().PID()) && l.WaitStart != nil {
			lockFound = true
		}
	}
	if !found || !lockFound {
		t.Fatalf("missing blocking details: %+v", snapshot)
	}
	conn, err := opsprobe.OpenDatabase(ctx, probeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var readOnly string
	if err := conn.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		t.Fatalf("not read-only: %s %v", readOnly, err)
	}
	svc := NewOperationsService(db, t.TempDir())
	server := model.Server{Host: "192.0.2.1", Port: 22, Username: "fixture", AuthType: "password"}
	db.Create(&server)
	run, err := svc.CreateInspection(InspectionRequest{Name: "fixture", Targets: []InspectionTargetRequest{{"host", server.ID}, {"host", server.ID}}}, "review")
	if err != nil || run.TotalTargets != 1 {
		t.Fatalf("run %+v %v", run, err)
	}
	if err := svc.ValidateInspection(InspectionRequest{Targets: []InspectionTargetRequest{{"database", 999999}}}); err == nil {
		t.Fatal("missing database accepted")
	}
}
func TestOperationsDailyScheduleAndHTMLPDF(t *testing.T) {
	next, err := NextInspectionTime(time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC), "09:00", "Asia/Shanghai")
	if err != nil || next.Day() != 28 || next.Hour() != 9 {
		t.Fatalf("next %s %v", next, err)
	}
	if _, err := NextInspectionTime(time.Now(), "25:99", "Asia/Shanghai"); err == nil {
		t.Fatal("bad schedule accepted")
	}
	if os.Getenv("DFOPS_REPORT_FONT") == "" {
		t.Skip("set DFOPS_REPORT_FONT for PDF layout verification")
	}
	target := inspectionReportTarget{Target: model.InspectionTargetRun{Name: "测试主机 <script>alert(1)</script>", Type: "host", Endpoint: "192.0.2.1:22", ServerEndpoint: "fixture@192.0.2.1:22", Status: "COMPLETED"}, Result: opsprobe.JobResult{Version: opsprobe.Version, Checks: []opsprobe.Check{{Code: "cpu", Name: "CPU 本次采样", Status: "NORMAL", Value: "使用率 12.5%"}, {Code: "sql", Name: "慢 SQL 日志", Status: "WARNING", Value: "仅统计已采集样本", Evidence: strings.Repeat("SELECT '中文字段'; -- 长文本换行验证\n", 50)}}}}
	report := inspectionReport{Run: model.InspectionRun{ID: 123, Name: "运维巡检测试报告", Status: "COMPLETED", Operator: "测试管理员", TotalTargets: 1, WarningCount: 1}, Targets: []inspectionReportTarget{target}, Generated: "2026-10-27 09:00:00"}
	var html bytes.Buffer
	if err := inspectionHTML.Execute(&html, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html.String(), "<script>alert") {
		t.Fatal("HTML evidence was not escaped")
	}
	pdf, err := renderInspectionPDF(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("invalid PDF")
	}
	if out := os.Getenv("DFOPS_OPERATIONS_QA_DIR"); out != "" {
		os.MkdirAll(out, 0700)
		if err := os.WriteFile(filepath.Join(out, "operations-sample.pdf"), pdf, 0600); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(out, "operations-sample.html"), html.Bytes(), 0600)
	}
}

func TestOperationsResumeAndCancellation(t *testing.T) {
	if os.Getenv("DFOPS_REPORT_FONT") == "" {
		t.Skip("PDF font required")
	}
	db := operationsTestDB(t)
	host, port := fakeOpsSSH(t)
	secret, err := crypto.Encrypt("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	server := model.Server{Host: host, Port: port, Username: "fixture", AuthType: "password", CredentialEncrypted: secret}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewOperationsService(db, t.TempDir())
	run, err := svc.CreateInspection(InspectionRequest{Name: "resume", Targets: []InspectionTargetRequest{{"host", server.ID}}}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	result := opsprobe.JobResult{Version: opsprobe.Version, Checks: []opsprobe.Check{{Code: "fixture", Name: "恢复测试", Status: "NORMAL", Value: "已执行"}}}
	data, _ := json.Marshal(result)
	os.WriteFile(filepath.Join(dir, "result.json"), data, 0600)
	if err := db.Model(&model.InspectionTargetRun{}).Where("run_id=?", run.ID).Updates(map[string]any{"status": "UNKNOWN", "remote_directory": dir}).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(run).Updates(map[string]any{"status": "RUNNING", "lease_owner": svc.owner})
	// The fake SSH server has no uname/upload/launch implementation: resume must only read the original result.
	svc.processInspection(context.Background(), run.ID)
	db.First(run, run.ID)
	if run.Status != "COMPLETED" || run.HTMLStatus != "READY" || run.PDFStatus != "READY" {
		t.Fatalf("resume failed: %+v", run)
	}
	// Regeneration must work even after the SSH address becomes unreachable.
	db.Model(&server).Update("port", 1)
	if err := svc.GenerateInspectionReports(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	canceled, err := svc.CreateInspection(InspectionRequest{Name: "cancel", Targets: []InspectionTargetRequest{{"host", server.ID}}}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	db.Model(canceled).Updates(map[string]any{"status": "RUNNING", "lease_owner": svc.owner, "cancel_requested": true})
	svc.processInspection(context.Background(), canceled.ID)
	db.First(canceled, canceled.ID)
	var target model.InspectionTargetRun
	db.Where("run_id=?", canceled.ID).First(&target)
	if canceled.Status != "CANCELED" || target.Status != "CANCELED" || target.RemoteDirectory != "" {
		t.Fatalf("cancel dispatched: %+v %+v", canceled, target)
	}
}
func TestOperationsScheduleFailureIsolationAndIdempotency(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	server := model.Server{Host: "192.0.2.2", Port: 22, Username: "fixture", AuthType: "password"}
	db.Create(&server)
	due := time.Now().Add(-time.Minute)
	bad := model.InspectionPlan{Name: "missing target", Enabled: true, DailyTime: "09:00", Timezone: "Asia/Shanghai", NextRunAt: &due, TargetsJSON: mustJSON([]InspectionTargetRequest{{"host", 999999}}), ServicesJSON: "[]"}
	good := model.InspectionPlan{Name: "valid target", Enabled: true, DailyTime: "09:00", Timezone: "Asia/Shanghai", NextRunAt: &due, TargetsJSON: mustJSON([]InspectionTargetRequest{{"host", server.ID}}), ServicesJSON: "[]"}
	if err := db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&good).Error; err != nil {
		t.Fatal(err)
	}
	svc.enqueueDuePlans(context.Background())
	svc.enqueueDuePlans(context.Background())
	db.First(&bad, bad.ID)
	db.First(&good, good.ID)
	var count int64
	db.Model(&model.InspectionRun{}).Where("plan_id=?", good.ID).Count(&count)
	if count != 1 || bad.LastError == "" || good.LastError != "" || !bad.NextRunAt.After(time.Now()) {
		t.Fatalf("schedule: count=%d bad=%+v good=%+v", count, bad, good)
	}
}

func TestOperationsHostHistoryUsesLastPointPerBucket(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	server := model.Server{Host: "192.0.2.9", Port: 22, Username: "fixture", AuthType: "password"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(30 * time.Second).Add(-5 * time.Minute)
	for i, offset := range []time.Duration{time.Second, 10 * time.Second, 60 * time.Second} {
		sample := model.HostMetricSample{ServerID: server.ID, CollectedAt: base.Add(offset), MetricsJSON: mustJSON(opsprobe.HostMetrics{CPUUsagePercent: float64(i + 1)})}
		if err := db.Create(&sample).Error; err != nil {
			t.Fatal(err)
		}
	}
	points, step, err := svc.HostHistory(context.Background(), server.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if step != 30 || len(points) != 2 || points[0].CPUUsagePercent != 2 || points[1].CPUUsagePercent != 3 || !points[0].CollectedAt.Before(points[1].CollectedAt) {
		t.Fatalf("wrong buckets step=%d points=%+v", step, points)
	}
}

func TestOperationsMissingLockLoggingIsUnknown(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	now := time.Now()
	row := model.OperationsDatabase{Name: "coverage", Host: "192.0.2.8", Port: 5432, Database: "fixture", Username: "fixture", LogEnabled: true, LogDirectory: "/var/log/postgresql", LastLogSyncAt: &now, LogCapability: mustJSON(map[string]string{"source_identity": "192.0.2.8:5432/fixture@0", "resolved_directory": "/var/log/postgresql", "log_lock_waits": "off"})}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	result := opsprobe.JobResult{}
	svc.appendLogInspectionChecks(row.ID, &result)
	for _, c := range result.Checks {
		if c.Code == "log:lock_wait" {
			if c.Status != "UNKNOWN" || c.Error == "" {
				t.Fatalf("missing lock logging represented as healthy: %+v", c)
			}
			return
		}
	}
	t.Fatal("missing lock log capability check")
}

func TestOperationsHostCollectionUsesHTTPWithoutSSH(t *testing.T) {
	db := operationsTestDB(t)
	var n atomic.Int64
	exporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := n.Add(1)
		fmt.Fprintf(w, "node_memory_MemTotal_bytes 1000\nnode_memory_MemAvailable_bytes 600\nnode_cpu_seconds_total{cpu=\"0\",mode=\"idle\"} %d\nnode_cpu_seconds_total{cpu=\"0\",mode=\"user\"} %d\n", 100+50*v, 20+10*v)
	}))
	defer exporter.Close()
	// Unreachable SSH and an unusable credential must not affect HTTP monitoring.
	server := model.Server{Host: "192.0.2.254", Port: 1, Username: "no-ssh", AuthType: "invalid", CredentialEncrypted: "unusable"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewOperationsService(db, t.TempDir())
	if _, e := svc.ConfigureMonitor(server.ID, false, 60, exporter.URL+"/metrics"); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := svc.CollectHostAsync(ctx, server.ID); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(4 * time.Second)
	for svc.HostCollecting(server.ID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cfg, m, e := svc.HostMonitor(server.ID)
	if e != nil || m == nil || m.Source != "node_exporter" || cfg.LastError != "" || n.Load() != 2 {
		t.Fatalf("HTTP collection used SSH or failed: config=%+v metric=%+v err=%v scrapes=%d", cfg, m, e, n.Load())
	}
	if cfg.SSHFingerprint != "" {
		t.Fatal("monitoring unexpectedly established SSH trust")
	}
	if _, e := svc.ConfigureMonitor(server.ID, false, 60, "file:///etc/passwd"); e == nil {
		t.Fatal("non HTTP source accepted")
	}
}
