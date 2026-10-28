package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestInspectionReportsRequireAuthenticationAndServeHTMLInlinePDFAsDownload(t *testing.T) {
	db := testutil.OpenGormPostgres(t)
	if err := db.AutoMigrate(&model.InspectionRun{}, &model.InspectionTargetRun{}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	svc := service.NewOperationsService(db, root)
	run := model.InspectionRun{Name: "report HTTP fixture", Status: "PARTIAL", HTMLStatus: "READY", PDFStatus: "READY"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	for format, content := range map[string]string{"html": "<!doctype html><h1>系统巡检报告</h1>", "pdf": "%PDF-1.7\nfixture"} {
		path, _ := svc.ReportPath(run.ID, format)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldDB, oldConfig := repository.DB, config.AppConfig
	repository.DB = db
	config.AppConfig = &config.Config{JWT: config.JWTConfig{Secret: "inspection-report-fixture", ExpireHours: 1}}
	t.Cleanup(func() { repository.DB = oldDB; config.AppConfig = oldConfig })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewOperationsHandler(svc).RegisterRoutes(router.Group("/api"))
	token, err := middleware.GenerateToken(1, "inspection-operator")
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	base := fmt.Sprintf("/api/operations/inspection-runs/%d", run.ID)
	for _, suffix := range []string{"", "/report/html", "/report/pdf"} {
		if got := call(base+suffix, false).Code; got != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", suffix, got)
		}
	}
	html := call(base+"/report/html", true)
	if html.Code != 200 || html.Header().Get("Content-Disposition") != "inline" || !strings.Contains(html.Header().Get("Content-Type"), "text/html") || html.Header().Get("Content-Security-Policy") == "" || html.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("HTML is not private inline content: %v", html.Header())
	}
	pdf := call(base+"/report/pdf", true)
	if pdf.Code != 200 || !strings.HasPrefix(pdf.Header().Get("Content-Disposition"), "attachment;") || !strings.HasPrefix(pdf.Body.String(), "%PDF") {
		t.Fatalf("PDF download invalid: %v", pdf.Header())
	}
	if body := call(base, true).Body.String(); !strings.Contains(body, `"executionLabel":"检查已结束"`) || !strings.Contains(body, `"coverage":`) {
		t.Fatalf("coverage unavailable: %s", body)
	}
	if body := call("/api/operations/inspection-runs?status=ENDED", true).Body.String(); !strings.Contains(body, "report HTTP fixture") || !strings.Contains(body, `"executionLabel":"检查已结束"`) || strings.Contains(body, "resultJSON") {
		t.Fatalf("ended-task list unavailable or leaked evidence: %s", body)
	}
}
