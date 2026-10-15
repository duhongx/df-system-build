package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"github.com/gin-gonic/gin"
)

func logHandlerTestRouter(t *testing.T, s *service.LogCollectionService) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	old := config.AppConfig
	config.AppConfig = &config.Config{JWT: config.JWTConfig{Secret: "log-collection-test-only", ExpireHours: 1}}
	t.Cleanup(func() { config.AppConfig = old })
	token, err := middleware.GenerateToken(1, "tester")
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	NewLogCollectionHandler(s).RegisterRoutes(r.Group("/api"))
	return r, token
}

func TestLogCollectionRoutesRequireAuthentication(t *testing.T) {
	router, token := logHandlerTestRouter(t, nil)
	for _, endpoint := range []struct{ method, path string }{{"GET", "/sources"}, {"POST", "/sources"}, {"PUT", "/sources/1"}, {"DELETE", "/sources/1"}, {"GET", "/sources/1/files"}, {"POST", "/jobs"}, {"GET", "/jobs"}, {"GET", "/jobs/1"}, {"POST", "/jobs/1/cancel"}, {"DELETE", "/jobs/1"}, {"POST", "/jobs/1/download"}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(endpoint.method, "/api/log-collection"+endpoint.path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%+v: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/log-collection/jobs/bad-id", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(w, req)
	var result struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Code != 12401 {
		t.Fatalf("%s", w.Body.String())
	}
}

func TestLogCollectionNativeDownload(t *testing.T) {
	db := testutil.OpenGormPostgres(t)
	if err := db.AutoMigrate(&model.LogCollectionJob{}); err != nil {
		t.Fatal(err)
	}
	s := service.NewLogCollectionService(db, t.TempDir())
	router, token := logHandlerTestRouter(t, s)
	archiveBytes := []byte("PK\x03\x04test archive bytes")
	job := model.LogCollectionJob{Status: "partial", ArchiveSize: int64(len(archiveBytes)), Message: "one file failed"}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	p := s.ArchivePath(job.ID)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, archiveBytes, 0600); err != nil {
		t.Fatal(err)
	}
	requestURL := "/api/log-collection/jobs/" + strings.TrimSuffix(filepath.Base(filepath.Dir(p)), "/") + "/download"
	makeRequest := func() *httptest.ResponseRecorder {
		body := url.Values{"token": []string{token}}.Encode()
		req := httptest.NewRequest("POST", requestURL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := makeRequest()
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), archiveBytes) || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if err := db.Model(&job).Update("status", "running").Error; err != nil {
		t.Fatal(err)
	}
	if w := makeRequest(); w.Code != http.StatusConflict {
		t.Fatalf("running download: %d", w.Code)
	}
	if err := db.Model(&job).Update("status", "partial").Error; err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(p)
	if w := makeRequest(); w.Code != http.StatusNotFound {
		t.Fatalf("missing download: %d", w.Code)
	}
}
