package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"strings"
	"testing"
)

func TestApplicationFrontendIdentityAndOfflineRegistration(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	s := NewApplicationService()
	app, err := s.Create(&CreateAppRequest{AppName: "web-new", AppType: "vue", VueRole: "sub", AppCode: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if app.ArtifactName != "42.zip" || app.GitRepo != "" {
		t.Fatal(app)
	}
	for _, req := range []CreateAppRequest{{AppName: "web-other", AppType: "vue", VueRole: "sub", AppCode: "42"}, {AppName: "web-reserved", AppType: "vue", VueRole: "sub", AppCode: "cdn"}, {AppName: "other-main", AppType: "vue", VueRole: "main"}} {
		if _, err := s.Create(&req); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
	p := model.Pipeline{PipelineNo: "identity-history", AppName: app.AppName, ApplicationID: app.ID, TriggerUser: "test"}
	if err := repository.DB.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(app.ID, &UpdateAppRequest{AppType: "vue", VueRole: "sub", AppCode: "43"}); err == nil {
		t.Fatal("historical target changed")
	}
}

func TestRollbackRejectsOlderBatchAfterAnotherChildPublished(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	first := model.ArtifactDeployBatch{DeployBatchNo: "old-child", VersionNo: "v1", Namespace: "prod"}
	if err := repository.DB.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	old := model.ArtifactDeployRecord{DeployBatchNo: first.DeployBatchNo, VersionNo: "v1", AppName: "child04", Namespace: "prod", DeploymentName: "web-main", BeforeImage: "B", AfterImage: "F1", DeployStatus: "success", Status: "deployed"}
	newer := model.ArtifactDeployRecord{DeployBatchNo: "new-child", VersionNo: "v2", AppName: "child10", Namespace: "prod", DeploymentName: "web-main", BeforeImage: "F1", AfterImage: "F2", DeployStatus: "success", Status: "deployed"}
	if err := repository.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Create(&newer).Error; err != nil {
		t.Fatal(err)
	}
	rollbacker := &fakeDeploymentRollbacker{}
	if err := RollbackDeployBatch(context.Background(), first.DeployBatchNo, rollbacker, nil); err == nil || !strings.Contains(err.Error(), "后续发布") {
		t.Fatal(err)
	}
	if len(rollbacker.calls) != 0 {
		t.Fatal("stale rollback reached cluster")
	}
}
