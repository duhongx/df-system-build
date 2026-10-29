package service

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	platformk8s "df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func inspectionClusterFixture(t *testing.T, name string) (string, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/nodes" {
			fmt.Fprintf(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":%q},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`, name)
		} else if r.URL.Path == "/api/v1/pods" {
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"PodList","items":[]}`)
		} else {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: %s\ncontexts:\n- name: fixture\n  context:\n    cluster: fixture\n    user: fixture\ncurrent-context: fixture\nusers:\n- name: fixture\n  user: {}\n", srv.URL), calls
}

func environmentClusterService(t *testing.T, content, path string) (*OperationsService, model.ComponentConnection) {
	t.Helper()
	db := operationsTestDB(t)
	old := repository.DB
	repository.DB = db
	t.Cleanup(func() { repository.DB = old })
	logger.Init("error", "stdout", "")
	platformk8s.ResetClient()
	t.Cleanup(platformk8s.ResetClient)
	repo := repository.NewSettingsRepo()
	prior := map[string]string{}
	for _, key := range []string{"k8s_kubeconfig_content", "k8s_kubeconfig_path"} {
		prior[key], _ = repo.Read(key, false)
	}
	t.Cleanup(func() { _ = repo.BatchUpdate(prior) })
	if err := repo.BatchUpdate(map[string]string{"k8s_kubeconfig_content": content, "k8s_kubeconfig_path": path}); err != nil {
		t.Fatal(err)
	}
	svc := NewOperationsService(db, t.TempDir())
	if err := svc.EnsureInspectionSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	var row model.ComponentConnection
	if err := db.Where("source='environment' AND kind='kubernetes'").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return svc, row
}

func TestConfiguredEnvironmentClusterRejectsChangedConnectionBeforeAnyRequest(t *testing.T) {
	first, firstCalls := inspectionClusterFixture(t, "first")
	second, secondCalls := inspectionClusterFixture(t, "second")
	svc, frozen := environmentClusterService(t, first, "")
	if err := repository.NewSettingsRepo().Set("k8s_kubeconfig_content", second); err != nil {
		t.Fatal(err)
	}
	out := svc.collectKubernetes(context.Background(), frozen, "")
	if len(out) != 1 || out[0].Status != "UNKNOWN" || !strings.Contains(out[0].Reason, "变化") || firstCalls.Load() != 0 || secondCalls.Load() != 0 {
		t.Fatalf("stale target contacted a cluster: first=%d second=%d results=%+v", firstCalls.Load(), secondCalls.Load(), out)
	}
	if err := svc.EnsureInspectionSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	var current model.ComponentConnection
	svc.db.First(&current, frozen.ID)
	out = svc.collectKubernetes(context.Background(), current, "")
	if current.Revision != frozen.Revision+1 || firstCalls.Load() != 0 || secondCalls.Load() != 2 || len(out) != 5 {
		t.Fatal("refreshed connection did not use the current cluster")
	}
}

func TestConfiguredEnvironmentClusterDetectsFileContentChangesAtSamePath(t *testing.T) {
	first, firstCalls := inspectionClusterFixture(t, "first-file")
	second, secondCalls := inspectionClusterFixture(t, "second-file")
	file := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(file, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	svc, frozen := environmentClusterService(t, "", file)
	if err := os.WriteFile(file, []byte(second), 0600); err != nil {
		t.Fatal(err)
	}
	out := svc.collectKubernetes(context.Background(), frozen, "")
	if len(out) != 1 || out[0].Status != "UNKNOWN" || firstCalls.Load() != 0 || secondCalls.Load() != 0 {
		t.Fatal("same-path replacement redirected a frozen target")
	}
	if err := svc.EnsureInspectionSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	var current model.ComponentConnection
	svc.db.First(&current, frozen.ID)
	if current.Revision != frozen.Revision+1 {
		t.Fatal("file replacement did not invalidate prior results")
	}
}

func TestConfiguredEnvironmentClusterPreservesRelativeCertificatePaths(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"NodeList","items":[]}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "certs"), 0700)
	if err := os.WriteFile(filepath.Join(dir, "certs", "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: %s\n    certificate-authority: certs/ca.pem\ncontexts:\n- name: fixture\n  context:\n    cluster: fixture\n    user: fixture\ncurrent-context: fixture\nusers:\n- name: fixture\n  user: {}\n", srv.URL)
	source := inspectionKubeconfig{content: []byte(config), path: filepath.Join(dir, "cluster.yaml")}
	cfg, err := source.clientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("relative CA could not authenticate the test cluster: %v", err)
	}
}
