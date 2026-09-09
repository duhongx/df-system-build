package pipeline

import (
	"df-build-server/internal/model"
	"testing"
)

func TestConfirmationResumesOnlyCompleteBuild(t *testing.T) {
	defs := ResolveStagesWithMode("vue", "manual")
	stages := []model.PipelineStage{{StageCode: "BUILD_IMAGE", Status: "SUCCESS"}, {StageCode: "PUSH_IMAGE", Status: "SUCCESS"}, {StageCode: "K8S_DEPLOY", Status: "PENDING"}, {StageCode: "NOTIFY", Status: "PENDING"}}
	if err := validateResumeStages(stages, defs, "registry/web:v1"); err != nil {
		t.Fatal(err)
	}
	if err := validateResumeStages(nil, defs, "registry/web:v1"); err == nil {
		t.Fatal("accepted missing checkpoints")
	}
	stages[1].Status = "FAILED"
	if err := validateResumeStages(stages, defs, "registry/web:v1"); err == nil {
		t.Fatal("accepted failed push")
	}
	stages[1].Status = "SUCCESS"
	stages[2].Status = "SUCCESS"
	if err := validateResumeStages(stages, defs, "registry/web:v1"); err == nil {
		t.Fatal("accepted repeated deployment")
	}
}
