package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"

	"github.com/gin-gonic/gin"
)

func setupSettingsHandlerTestDB(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
}

func TestUpdateK8sSettingsResetsK8sClient(t *testing.T) {
	setupSettingsHandlerTestDB(t)
	gin.SetMode(gin.TestMode)

	resetCalled := 0
	originalReset := resetK8sClient
	resetK8sClient = func() { resetCalled++ }
	t.Cleanup(func() { resetK8sClient = originalReset })

	body := strings.NewReader(`{"k8s_namespace":"customer-a","docker_registry_url":"registry.test:5000"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/settings", body)
	c.Request.Header.Set("Content-Type", "application/json")

	NewSettingsHandler().Update(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if resetCalled != 1 {
		t.Fatalf("expected k8s client reset once, got %d", resetCalled)
	}
}

func TestReadKubeconfigReturnsCurrentContextNamespace(t *testing.T) {
	setupSettingsHandlerTestDB(t)
	gin.SetMode(gin.TestMode)

	kubeconfigPath := filepath.Join(t.TempDir(), "kube-config")
	content := `
apiVersion: v1
kind: Config
current-context: customer-context
contexts:
- name: customer-context
  context:
    cluster: customer-cluster
    user: customer-user
    namespace: customer-prod
`
	if err := os.WriteFile(kubeconfigPath, []byte(content), 0644); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/settings/read-kubeconfig?path="+kubeconfigPath, nil)

	NewSettingsHandler().ReadKubeconfig(c)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Content   string `json:"content"`
			Namespace string `json:"namespace"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code != 0 {
		t.Fatalf("expected success response, got code %d", resp.Code)
	}
	if resp.Data.Namespace != "customer-prod" {
		t.Fatalf("expected namespace customer-prod, got %q", resp.Data.Namespace)
	}
}
