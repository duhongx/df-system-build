package stages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

func TestK8sSettingsErrorsDoNotSilentlyFallback(t *testing.T) {
	setupImageFlowDB(t)
	k8s.ResetClient()
	defer k8s.ResetClient()
	if ns, err := k8s.GetDefaultNamespace(); err != nil || ns != "default" {
		t.Fatalf("optional missing namespace: %s %v", ns, err)
	}
	cause := errors.New("settings database unavailable")
	failedKey := ""
	cb := "test:k8s_setting_error"
	if err := repository.DB.Callback().Query().Before("gorm:query").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" && strings.Contains(fmt.Sprint(tx.Statement.Clauses["WHERE"].Expression), failedKey) {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer repository.DB.Callback().Query().Remove(cb)
	for _, key := range []string{"k8s_kubeconfig_content", "k8s_kubeconfig_path", "k8s_namespace"} {
		failedKey = key
		var err error
		if key == "k8s_namespace" {
			_, err = k8s.GetDefaultNamespace()
		} else {
			_, err = k8s.GetClient()
		}
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), key) {
			t.Fatalf("silent fallback for %s: %v", key, err)
		}
	}
	pc := &types.PipelineContext{OnLog: func(uint, uint, string, string) {}}
	for _, tc := range []struct{ key, content string }{
		{"docker_registry_url", "image: ${registryUrl}/app:tag"},
		{"skywalking_graphql_url", "endpoint: ${skywalkingGraphqlUrl}"},
	} {
		failedKey = tc.key
		tpl := model.ConfigItem{Code: "check-" + tc.key, Content: tc.content}
		if err := repository.DB.Create(&tpl).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := (&K8sDeployStage{}).renderResource(context.Background(), pc, "test", "service", tpl.Code, nil); !errors.Is(err, cause) {
			t.Fatalf("template ignored %s: %v", tc.key, err)
		}
	}
	// Templates without the optional setting must not read it.
	tpl := model.ConfigItem{Code: "check-no-settings", Content: "image: ${imageName}"}
	if err := repository.DB.Create(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&K8sDeployStage{}).renderResource(context.Background(), pc, "test", "service", tpl.Code, nil); err != nil {
		t.Fatalf("unused setting blocked render: %v", err)
	}
	failedKey = "no-injected-failure"
	tpl = model.ConfigItem{Code: "check-optional-missing", Content: "endpoint: ${skywalkingGraphqlUrl}"}
	if err := repository.DB.Create(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&K8sDeployStage{}).renderResource(context.Background(), pc, "test", "service", tpl.Code, nil); err != nil {
		t.Fatalf("missing optional setting blocked render: %v", err)
	}
}

func TestTemplateNodeLookupHonorsTaskDeadline(t *testing.T) {
	setupImageFlowDB(t)
	k8s.ResetClient()
	defer k8s.ResetClient()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: local\n  cluster:\n    server: %s\ncontexts:\n- name: local\n  context:\n    cluster: local\n    user: local\ncurrent-context: local\nusers:\n- name: local\n  user: {}\n", srv.URL)
	if err := repository.NewSettingsRepo().Set("k8s_kubeconfig_content", config); err != nil {
		t.Fatal(err)
	}
	tpl := model.ConfigItem{Code: "check-node-timeout", Content: "node: ${nodeIP}"}
	if err := repository.DB.Create(&tpl).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	pc := &types.PipelineContext{OnLog: func(uint, uint, string, string) {}}
	_, err := (&K8sDeployStage{}).renderResource(ctx, pc, "test", "service", tpl.Code, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("node lookup lost task deadline: %v", err)
	}
}
