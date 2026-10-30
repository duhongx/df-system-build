package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMonitoringWorkflowCollectsPersistsAndArchivesThroughAuthenticatedAPI(t *testing.T) {
	base := testutil.OpenGormPostgres(t)
	schema := fmt.Sprintf("monitoring_workflow_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	dsn, _ := testutil.PostgresDSNFromEnv()
	pg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pg.RuntimeParams["search_path"] = schema
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: stdlib.OpenDB(*pg)}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var activeSchema string
	if err := db.Raw("SELECT current_schema()").Scan(&activeSchema).Error; err != nil || activeSchema != schema {
		t.Fatalf("workflow database was not isolated in its own schema: %s", activeSchema)
	}
	oldDB, oldConfig := repository.DB, config.AppConfig
	repository.DB = db
	config.AppConfig = &config.Config{JWT: config.JWTConfig{Secret: "local-monitoring-workflow-only", ExpireHours: 1}}
	t.Setenv("DF_ENCRYPT_KEY", "configured-test-key-32-characters")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		repository.DB = oldDB
		config.AppConfig = oldConfig
		pool, _ := db.DB()
		_ = pool.Close()
		base.Exec("DROP SCHEMA " + schema + " CASCADE")
		parent, _ := base.DB()
		_ = parent.Close()
	})
	if err := db.AutoMigrate(&model.Server{}, &model.HostMonitorConfig{}, &model.HostMetricSample{}, &model.OperationsDatabase{}, &model.DatabaseQueryRecord{}, &model.DatabaseLogCursor{}, &model.DatabaseLogEvent{}, &model.InspectionPlan{}, &model.InspectionRun{}, &model.InspectionTargetRun{}, &model.ComponentConnection{}, &model.MonitoringState{}, &model.CollectionSample{}, &model.ManagedService{}, &model.ServiceOperation{}, &model.Settings{}, &model.User{}); err != nil {
		t.Fatal(err)
	}
	requests := &atomic.Int32{}
	component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "fixture-key" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_cluster/health":
			fmt.Fprint(w, `{"cluster_name":"workflow-cluster","status":"yellow","number_of_nodes":1,"number_of_data_nodes":1,"active_shards":10,"unassigned_shards":2}`)
		case "/_nodes/stats/fs,jvm":
			fmt.Fprint(w, `{"nodes":{"one":{"name":"workflow-node","fs":{"total":{"total_in_bytes":100,"available_in_bytes":90}},"jvm":{"mem":{"heap_used_percent":30}}}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer component.Close()
	svc := service.NewOperationsService(db, t.TempDir())
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewOperationsHandler(svc).RegisterRoutes(router.Group("/api"))
	token, err := middleware.GenerateToken(1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	raw := func(method, path string, payload any, actorToken string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		if payload != nil {
			if err := json.NewEncoder(&body).Encode(payload); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "/api/operations"+path, &body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+actorToken)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	api := func(method, path string, payload any, dest any) {
		out := raw(method, path, payload, token)
		var envelope struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &envelope); err != nil || out.Code >= 400 || envelope.Code != 0 {
			t.Fatalf("%s %s: HTTP %d %s", method, path, out.Code, out.Body.String())
		}
		if dest != nil {
			if err := json.Unmarshal(envelope.Data, dest); err != nil {
				t.Fatal(err)
			}
		}
	}
	var connection model.ComponentConnection
	api("POST", "/components", service.ComponentRequest{Kind: "elasticsearch", Name: "工作流自检 ES", Endpoint: component.URL, Username: "reader", Secret: "fixture-key", Enabled: true}, &connection)
	list := raw("GET", "/components", nil, token)
	if strings.Contains(list.Body.String(), "fixture-key") || strings.Contains(list.Body.String(), "secretEncrypted") {
		t.Fatal("component credentials were returned")
	}
	// No viewer or manual collection request is needed to create the first sample.
	svc.Start(ctx)
	var monitor service.MonitoringView
	automaticDeadline := time.Now().Add(10 * time.Second)
	for {
		api("GET", fmt.Sprintf("/monitoring/component/%d", connection.ID), nil, &monitor)
		if monitor.Result != nil {
			break
		}
		if time.Now().After(automaticDeadline) {
			t.Fatal("background monitoring did not produce a first sample")
		}
		time.Sleep(100 * time.Millisecond)
	}
	result := *monitor.Result
	if requests.Load() != 2 || result.Coverage == nil || result.Coverage.Planned != 2 || result.Coverage.Completed != 2 || result.Coverage.Warning != 1 {
		t.Fatalf("collection/coverage: calls=%d %+v", requests.Load(), result.Coverage)
	}
	var run model.InspectionRun
	api("POST", "/configured-inspections", map[string]any{"name": "系统巡检报告 · 本地流程自检", "targets": []service.InspectionTargetRequest{{Type: "component", ID: connection.ID}}}, &run)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := db.First(&run, run.ID).Error; err != nil {
			t.Fatal(err)
		}
		if run.Status == "COMPLETED" || run.Status == "PARTIAL" || run.Status == "FAILED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background report did not finish: %s %s", run.Status, run.Message)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if run.Status != "COMPLETED" || run.HTMLStatus != "READY" || run.PDFStatus != "READY" {
		t.Fatalf("background report: %+v", run)
	}
	var sample model.CollectionSample
	api("GET", fmt.Sprintf("/collections/component/%d", connection.ID), nil, &sample)
	var saved opsprobe.JobResult
	if err := json.Unmarshal([]byte(sample.ResultJSON), &saved); err != nil {
		t.Fatal(err)
	}
	var frozen inspection.Report
	api("GET", fmt.Sprintf("/inspection-runs/%d/report-data", run.ID), nil, &frozen)
	if frozen.FinishedAt.Before(frozen.StartedAt) || frozen.StartedAt.After(result.FinishedAt) {
		t.Fatal("monitoring report has an inverted or fabricated collection time range", frozen.StartedAt, frozen.FinishedAt)
	}
	if saved.Coverage == nil || mustMonitoringJSON(saved.Coverage) != mustMonitoringJSON(frozen.Summary) {
		t.Fatal("component page and report use different coverage")
	}
	html := raw("GET", fmt.Sprintf("/inspection-runs/%d/report/html", run.ID), nil, token)
	pdf := raw("GET", fmt.Sprintf("/inspection-runs/%d/report/pdf", run.ID), nil, token)
	if html.Code != 200 || !strings.Contains(html.Body.String(), "workflow-cluster") || !strings.Contains(html.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("online report: HTTP %d", html.Code)
	}
	if pdf.Code != 200 || !bytes.HasPrefix(pdf.Body.Bytes(), []byte("%PDF")) || !strings.Contains(pdf.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("PDF download: HTTP %d", pdf.Code)
	}
	frozenJSON := run.ReportSnapshotJSON
	calls := requests.Load()
	if err := svc.GenerateInspectionReports(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&run, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if requests.Load() != calls || run.ReportSnapshotJSON != frozenJSON {
		t.Fatal("reformatting recollected or changed frozen results")
	}
	if dest := os.Getenv("DFOPS_MONITORING_WORKFLOW_ARTIFACTS"); dest != "" {
		if err := os.MkdirAll(dest, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"workflow-report.html": html.Body.Bytes(), "workflow-report.pdf": pdf.Body.Bytes(), "workflow-report.json": []byte(frozenJSON)} {
			if err := os.WriteFile(filepath.Join(dest, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Exercise authorization at the real HTTP route before any SSH connection.
	host := model.Server{Host: "127.0.0.1", Port: 1, Username: "fixture", CreatedBy: "admin"}
	db.Create(&host)
	var managed model.ManagedService
	api("POST", fmt.Sprintf("/hosts/%d/services", host.ID), service.ManagedServiceInput{Name: "SkyWalking OAP", Mode: "script", RunUser: "fixture", Directory: "/opt/skywalking", StartScript: "/opt/skywalking/bin/oap.sh", Identity: "org.apache.skywalking.oap.server.starter.OAPServerStartUp"}, &managed)
	outsider, _ := middleware.GenerateToken(2, "outsider")
	denied := raw("POST", fmt.Sprintf("/hosts/%d/services/%d/operations", host.ID, managed.ID), map[string]string{"action": "start"}, outsider)
	var response struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(denied.Body.Bytes(), &response)
	var operations int64
	db.Model(&model.ServiceOperation{}).Where("service_id=?", managed.ID).Count(&operations)
	if response.Code == 0 || operations != 0 {
		t.Fatal("unauthorized HTTP request created a service operation")
	}
}

func mustMonitoringJSON(value any) string { b, _ := json.Marshal(value); return string(b) }
