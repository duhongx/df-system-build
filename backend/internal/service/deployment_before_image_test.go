package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRetryPreservesRecoverableImage(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Settings{}); err != nil {
		t.Fatal(err)
	}
	// Previous deployment A1 failed to start; Deployment still desires A1.
	// The existing Ready A0 Pod does not affect CurrentDeploymentImage.
	dep := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "service", Namespace: "test"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "service", Image: "registry/service:A1-failed"}}}}}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/deployments/service") {
			t.Errorf("unexpected request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dep)
	}))
	defer srv.Close()
	config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: local\n  cluster:\n    server: %s\ncontexts:\n- name: local\n  context:\n    cluster: local\n    user: local\ncurrent-context: local\nusers:\n- name: local\n  user: {}\n", srv.URL)
	if err := repository.NewSettingsRepo().Set("k8s_kubeconfig_content", config); err != nil {
		t.Fatal(err)
	}
	k8s.ResetClient()
	defer k8s.ResetClient()
	old := model.ArtifactDeployRecord{DeployBatchNo: "failed-A1", VersionNo: "failed-A1", PipelineID: 100, AppName: "service", DeploymentName: "service", Namespace: "test", BeforeImage: "registry/service:A0", AfterImage: "registry/service:A1-failed", Status: "failed", DeployStatus: "failed"}
	if err := repository.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	batch := model.ArtifactDeployBatch{DeployBatchNo: "retry-A2", VersionNo: "retry-A2"}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	record := model.ArtifactDeployRecord{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, PipelineID: 101, AppName: "service", DeploymentName: "service", Namespace: "test"}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	before, err := ImagesBeforeDeployment(context.Background(), "test", "service", 101)
	if err != nil {
		t.Fatal(err)
	}
	if err := CaptureBeforeDeployImages(context.Background(), 101, "service", before, nil); err != nil {
		t.Fatal(err)
	}
	if err := CaptureAfterDeployVersions(context.Background(), 101, "service", "registry/service:A2", nil); err != nil {
		t.Fatal(err)
	}
	rollbacker := &fakeDeploymentRollbacker{}
	if err := RollbackDeployBatch(context.Background(), batch.DeployBatchNo, rollbacker, nil); err != nil {
		t.Fatal(err)
	}
	if len(rollbacker.calls) != 1 || rollbacker.calls[0] != "test/service=registry/service:A0" {
		t.Fatalf("unexpected rollback target: %v", rollbacker.calls)
	}
	if err := repository.DB.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.BeforeImage != "registry/service:A0" || record.BeforeDesiredImage != "registry/service:A1-failed" {
		t.Fatalf("lost image audit: %+v", record)
	}
	// A second failed retry continues carrying A0.
	dep.Spec.Template.Spec.Containers[0].Image = "registry/service:A2"
	record.DeployStatus = "failed"
	if err := repository.DB.Save(&record).Error; err != nil {
		t.Fatal(err)
	}
	images, err := resolveBeforeDeploymentImages(&dep, "test", "service", 102)
	if err != nil || images.Rollback != "registry/service:A0" {
		t.Fatalf("lost baseline across repeated failures: %+v %v", images, err)
	}
	// Once the desired image really completed rolling out, it becomes the baseline.
	dep.Status = appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	images, err = resolveBeforeDeploymentImages(&dep, "test", "service", 102)
	if err != nil || images.Rollback != "registry/service:A2" {
		t.Fatalf("ignored completed rollout: %+v %v", images, err)
	}
	// Missing history must not block a repair or invent a recoverable tag.
	dep.Status.UpdatedReplicas = 0
	dep.Spec.Template.Spec.Containers[0].Image = "registry/service:external"
	images, err = resolveBeforeDeploymentImages(&dep, "test", "service", 102)
	if err != nil || images.Rollback != "" || images.Desired != "registry/service:external" {
		t.Fatalf("unexpected unknown baseline: %+v %v", images, err)
	}
}
