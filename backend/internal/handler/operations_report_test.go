package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"df-build-server/internal/inspection"
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestOperationsReportAPIRequiresAuthAndKeepsListsCompact(t *testing.T) {
	db := testutil.OpenGormPostgres(t)
	if e := db.AutoMigrate(&model.InspectionRun{}); e != nil {
		t.Fatal(e)
	}
	oldDB, oldConfig := repository.DB, config.AppConfig
	repository.DB = db
	config.AppConfig = &config.Config{JWT: config.JWTConfig{Secret: "inspection-report-local-test", ExpireHours: 1}}
	t.Cleanup(func() { repository.DB = oldDB; config.AppConfig = oldConfig })
	doc := inspection.Build(inspection.Report{Name: "snapshot-only-marker"})
	data, e := json.Marshal(doc)
	if e != nil {
		t.Fatal(e)
	}
	run := model.InspectionRun{Name: "api-report-test", Status: "COMPLETED", ReportSnapshotJSON: string(data)}
	if e := db.Create(&run).Error; e != nil {
		t.Fatal(e)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewOperationsHandler(service.NewOperationsService(db, t.TempDir())).RegisterRoutes(router.Group("/api"))
	token, e := middleware.GenerateToken(1, "test-admin")
	if e != nil {
		t.Fatal(e)
	}
	call := func(path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	path := "/api/operations/inspection-runs/" + strconv.FormatUint(uint64(run.ID), 10) + "/report-data"
	if w := call(path, false); w.Code != http.StatusUnauthorized {
		t.Fatal("report snapshot accessible without authentication")
	}
	if w := call(path, true); w.Code != 200 || !strings.Contains(w.Body.String(), "snapshot-only-marker") || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("report response %d: %s", w.Code, w.Body.String())
	}
	if w := call("/api/operations/inspection-runs", true); w.Code != 200 || strings.Contains(w.Body.String(), "snapshot-only-marker") {
		t.Fatalf("list included report snapshot: %s", w.Body.String())
	}
}
