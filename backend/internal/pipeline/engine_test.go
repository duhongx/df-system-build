package pipeline

import (
	"df-build-server/internal/model"
	"testing"
)

func TestRolloutUsesOnlyCompleteBuild(t *testing.T) {
	defs := ResolveStagesWithMode("vue", "manual")
	stages := []model.PipelineStage{{StageCode: "BUILD_IMAGE", Status: "SUCCESS"}, {StageCode: "PUSH_IMAGE", Status: "SUCCESS"}, {StageCode: "K8S_DEPLOY", Status: "PENDING"}, {StageCode: "NOTIFY", Status: "PENDING"}}
	if err := ValidateRolloutStages(stages, defs, "registry/web:v1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRolloutStages(nil, defs, "registry/web:v1"); err == nil {
		t.Fatal("accepted missing checkpoints")
	}
	stages[1].Status = "FAILED"
	if err := ValidateRolloutStages(stages, defs, "registry/web:v1"); err == nil {
		t.Fatal("accepted failed push")
	}
	stages[1].Status = "SUCCESS"
	stages[2].Status = "SUCCESS"
	if err := ValidateRolloutStages(stages, defs, "registry/web:v1"); err == nil {
		t.Fatal("accepted repeated deployment")
	}
}
