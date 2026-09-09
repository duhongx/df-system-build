package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
)

type countingStage struct {
	code       string
	calls      *[]string
	failedPush bool
}

func (s countingStage) Name() string { return s.code }
func (s countingStage) Code() string { return s.code }
func (s countingStage) Run(_ context.Context, pc *types.PipelineContext) (*types.StageResult, error) {
	*s.calls = append(*s.calls, s.code)
	if s.code == "BUILD_IMAGE" {
		pc.ImageName = "registry/service:prepared"
	}
	if s.code == "PUSH_IMAGE" && s.failedPush {
		return nil, errors.New("push denied")
	}
	if s.code == "K8S_DEPLOY" && pc.ImageName != "registry/service:prepared" {
		return nil, errors.New("rollout used a different image")
	}
	return &types.StageResult{}, nil
}

func TestImagePreparationAndRolloutAreSeparateExecutions(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}, &model.StageLog{}, &model.ArtifactDeployRecord{}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"artifact_image", "build_image", "upload_and_build", "artifact_deploy", "deploy", "deploy_with_approval", "manual"} {
		t.Run(mode, func(t *testing.T) {
			p := &model.Pipeline{PipelineNo: mode, AppName: "service", AppType: "java", Status: "RUNNING", DeployMode: mode}
			if err := repository.NewPipelineRepo().Create(p); err != nil {
				t.Fatal(err)
			}
			var calls []string
			defs := ResolveStagesWithMode(p.AppType, mode)
			for i := range defs {
				defs[i].Runner = countingStage{code: defs[i].Code, calls: &calls}
			}
			pc := &types.PipelineContext{PipelineID: p.ID, Pipeline: p, AppName: p.AppName, AppType: p.AppType}
			e := NewEngine()
			if err := e.executeStages(context.Background(), p, pc, defs); !errors.Is(err, ErrImageReady) {
				t.Fatalf("image preparation did not finish: %v", err)
			}
			if calls[len(calls)-1] != "PUSH_IMAGE" {
				t.Fatalf("rollout ran during preparation: %v", calls)
			}
			for _, code := range calls {
				if code == "K8S_DEPLOY" || code == "NOTIFY" {
					t.Fatalf("unexpected first-step stage: %v", calls)
				}
			}
			var saved model.Pipeline
			if err := repository.DB.First(&saved, p.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.ImageName != "registry/service:prepared" {
				t.Fatalf("image not persisted: %+v", saved)
			}
			// A fresh execution reconstructs the context from the saved image.
			saved.Status = "DEPLOYING"
			calls = nil
			pc = &types.PipelineContext{PipelineID: saved.ID, Pipeline: &saved, AppName: saved.AppName, AppType: saved.AppType, ImageName: saved.ImageName}
			if err := e.executeStages(context.Background(), &saved, pc, defs); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, []string{"K8S_DEPLOY", "NOTIFY"}) {
				t.Fatalf("rollout repeated image preparation: %v", calls)
			}
			if err := e.executeStages(context.Background(), &saved, pc, defs); err == nil {
				t.Fatal("repeated rollout accepted")
			}
		})
	}
}

func TestFailedPushCannotEnterRollout(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}, &model.StageLog{}, &model.ArtifactDeployRecord{}); err != nil {
		t.Fatal(err)
	}
	p := &model.Pipeline{PipelineNo: "push-failure", AppName: "service", AppType: "java", Status: "RUNNING", DeployMode: "artifact_image"}
	if err := repository.NewPipelineRepo().Create(p); err != nil {
		t.Fatal(err)
	}
	var calls []string
	defs := ResolveStagesWithMode(p.AppType, p.DeployMode)
	for i := range defs {
		defs[i].Runner = countingStage{code: defs[i].Code, calls: &calls, failedPush: true}
	}
	pc := &types.PipelineContext{PipelineID: p.ID, Pipeline: p, AppName: p.AppName, AppType: p.AppType}
	if err := NewEngine().executeStages(context.Background(), p, pc, defs); err == nil || errors.Is(err, ErrImageReady) {
		t.Fatalf("failed push treated as ready: %v", err)
	}
	stages, err := repository.NewStageRepo().FindByPipelineID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRolloutStages(stages, defs, p.ImageName); err == nil {
		t.Fatal("failed push accepted for rollout")
	}
	if !reflect.DeepEqual(calls, []string{"BUILD_IMAGE", "PUSH_IMAGE"}) {
		t.Fatalf("failed push continued: %v", calls)
	}
}
