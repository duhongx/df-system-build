package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/stages"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"github.com/gin-gonic/gin"
)

func TestBuildProfilesEnforceCustomerBoundaryAndHostConfigAPI(t *testing.T) {
	repository.DB = testutil.OpenGormPostgres(t)
	if err := repository.DB.AutoMigrate(&model.Settings{}, &model.BuildConfig{}, &model.Application{}, &model.Task{}); err != nil {
		t.Fatal(err)
	}
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{JWT: config.JWTConfig{Secret: "profile-test", ExpireHours: 1}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	token, err := middleware.GenerateToken(1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	api := router.Group("/api")
	NewBuildConfigHandler().RegisterRoutes(api)
	NewTaskHandler().RegisterRoutes(api)
	NewApplicationHandler().RegisterRoutes(api)
	NewSettingsHandler().RegisterRoutes(api)
	request := func(method, path, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var data map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return w.Code, data
	}
	settings := repository.NewSettingsRepo()
	if err := settings.Set("deploy_source", "both"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set("customer_env", "浙江"); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{
		{"DELETE", "/api/tasks/1"}, {"PUT", "/api/tasks/1"}, {"POST", "/api/tasks"}, {"POST", "/api/tasks/1/execute"}, {"POST", "/api/tasks/batch-execute"},
		{"POST", "/api/build-configs"}, {"PUT", "/api/build-configs/1"}, {"DELETE", "/api/build-configs/1"},
	} {
		status, body := request(route.method, route.path, `{}`)
		if status != http.StatusForbidden || !strings.Contains(body["message"].(string), "非本地") {
			t.Fatalf("%s not blocked: %d %+v", route.path, status, body)
		}
	}
	for _, path := range []string{"/api/tasks", "/api/build-configs"} {
		status, body := request("GET", path, "")
		if status != 200 || body["code"] != float64(0) {
			t.Fatalf("read-only history blocked: %s %d %+v", path, status, body)
		}
	}
	for _, runner := range []types.StageRunner{&stages.GitCloneStage{}, &stages.LocalBuildStage{}} {
		if _, err := runner.Run(context.Background(), &types.PipelineContext{}); err == nil {
			t.Fatal("customer mode entered source execution")
		}
	}
	status, body := request("POST", "/api/applications", `{"appName":"customer-no-git","appType":"java"}`)
	if status != 200 || body["code"] != float64(0) {
		t.Fatalf("customer app requires Git: %d %+v", status, body)
	}
	var count int64
	repository.DB.Model(&model.Task{}).Count(&count)
	if count != 0 {
		t.Fatal("blocked requests created source tasks")
	}
	// Legacy clients cannot override the environment using the retired field.
	status, body = request("PUT", "/api/settings", `{"customer_env":"本地","deploy_source":"artifact"}`)
	if status != 200 || body["code"] != float64(0) {
		t.Fatalf("save environment: %d %+v", status, body)
	}
	_, result := request("GET", "/api/settings", "")
	if _, exists := result["data"].(map[string]any)["deploy_source"]; exists {
		t.Fatal("retired mode exposed")
	}
	status, body = request("POST", "/api/build-configs", `{"name":"host-tools","category":"java","buildCommand":"gradle build","artifactDir":"build/libs"}`)
	if status != 200 || body["code"] != float64(0) {
		t.Fatalf("local config rejected: %d %+v", status, body)
	}
	configJSON, _ := json.Marshal(body["data"])
	for _, field := range []string{"buildMode", "dockerImage", "cpuLimit", "memoryLimit", "cacheMounts"} {
		if strings.Contains(string(configJSON), field) {
			t.Fatalf("removed field exposed: %s", field)
		}
	}
	_, body = request("POST", "/api/build-configs", `{"name":"legacy-client","category":"java","buildCommand":"gradle build","buildMode":"docker","dockerImage":"gradle:8"}`)
	if body["code"] == float64(0) {
		t.Fatal("legacy container request silently became host execution")
	}
}
