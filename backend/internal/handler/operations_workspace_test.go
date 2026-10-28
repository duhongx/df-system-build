package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestWorkspaceAPIUsesAuthenticatedOwnerAndOmitsResultsCredentials(t *testing.T) {
	db := testutil.OpenGormPostgres(t)
	if err := db.AutoMigrate(&model.DatabaseWorkspace{}, &model.DatabaseSavedQuery{}); err != nil {
		t.Fatal(err)
	}
	originalDB, originalConfig := repository.DB, config.AppConfig
	repository.DB = db
	config.AppConfig = &config.Config{JWT: config.JWTConfig{Secret: "workspace-api", ExpireHours: 1}}
	t.Cleanup(func() { repository.DB = originalDB; config.AppConfig = originalConfig })
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewOperationsHandler(service.NewOperationsService(db, t.TempDir())).RegisterRoutes(r.Group("/api"))
	aliceName := fmt.Sprintf("workspace-alice-%d", time.Now().UnixNano())
	alice, _ := middleware.GenerateToken(1, aliceName)
	bob, _ := middleware.GenerateToken(2, "workspace-bob")
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/operations"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"/database-workspace", "/saved-queries"} {
		if w := call("GET", path, "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("unprotected %s", path)
		}
	}
	body := `{"revision":0,"state":{"version":1,"owner":"workspace-bob","tabs":[{"id":1,"name":"草稿","databaseId":7,"revision":2,"sql":"select 42","parameters":"[]","password":"credential-must-not-save","result":{"rows":["result-must-not-save"]}}],"closedTabs":[],"activeTabId":1,"pane":"sql"}}`
	w := call("PUT", "/database-workspace", body, alice)
	if !strings.Contains(w.Body.String(), `"code":0`) || strings.Contains(w.Body.String(), "must-not-save") {
		t.Fatalf("workspace response %s", w.Body.String())
	}
	w = call("GET", "/database-workspace", "", alice)
	if !strings.Contains(w.Body.String(), "select 42") || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("restore failed %s", w.Body.String())
	}
	var persisted model.DatabaseWorkspace
	if err := db.Where("owner=?", aliceName).First(&persisted).Error; err != nil || strings.Contains(persisted.StateJSON, "must-not-save") {
		t.Fatalf("persisted unsafe state %+v %v", persisted, err)
	}
	w = call("GET", "/database-workspace", "", bob)
	if strings.Contains(w.Body.String(), "select 42") {
		t.Fatal("workspace user scope bypass")
	}
	w = call("PUT", "/database-workspace", body, alice)
	if !strings.Contains(w.Body.String(), `"code":16002`) {
		t.Fatalf("conflict not identified: %s", w.Body.String())
	}
	w = call("POST", "/saved-queries", `{"name":"common","sql":"select 1","parametersJSON":"[]","owner":"workspace-bob"}`, alice)
	var favorite struct {
		Code int
		Data model.DatabaseSavedQuery
	}
	if err := json.Unmarshal(w.Body.Bytes(), &favorite); err != nil || favorite.Code != 0 || favorite.Data.ID == 0 {
		t.Fatalf("favorite response %s", w.Body.String())
	}
	w = call("GET", "/saved-queries", "", bob)
	if strings.Contains(w.Body.String(), "common") {
		t.Fatal("favorites leaked through owner field")
	}
	path := fmt.Sprintf("/saved-queries/%d?revision=1", favorite.Data.ID)
	w = call("DELETE", path, "", bob)
	if strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatal("another user deleted favorite")
	}
}
