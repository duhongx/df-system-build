package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
	"df-build-server/pkg/sse"
)

func TestUpdateLogsPersistScopeAndPublishFailureWithStageIdentity(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}, &model.StageLog{}, &model.Settings{}); err != nil {
		t.Fatal(err)
	}
	// Fail before contacting Docker or Kubernetes.
	if err := repository.NewSettingsRepo().Set("docker_registry_url", ""); err != nil {
		t.Fatal(err)
	}
	p := &model.Pipeline{PipelineNo: repository.NewPipelineRepo().GenerateNo("log-test"), AppName: "web-cdr", AppType: "vue", TriggerUser: "test", DeployMode: "artifact_deploy", FrontendUpdateSummary: "独立应用：web-cdr；制品=web-cdr.zip\n子应用：门诊；appCode=04；制品=04.zip"}
	if err := repository.NewPipelineRepo().Create(p); err != nil {
		t.Fatal(err)
	}
	saved, err := repository.NewPipelineRepo().FindByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	engine.sseHub = sse.NewHub()
	events := engine.sseHub.Subscribe(p.ID)
	defer engine.sseHub.Unsubscribe(p.ID, events)
	pc := &types.PipelineContext{PipelineID: p.ID, Pipeline: saved, AppName: p.AppName, AppType: "vue", VueRole: "standalone", DeployMode: p.DeployMode, K8sNamespace: "test"}
	if err := engine.Execute(context.Background(), saved, pc); err == nil {
		t.Fatal("expected missing registry to stop execution")
	}
	stages, err := repository.NewStageRepo().FindByPipelineID(p.ID)
	if err != nil || len(stages) != 4 || stages[0].Status != "FAILED" || stages[1].Status != "SKIPPED" {
		t.Fatalf("unexpected failure progression: %+v %v", stages, err)
	}
	logs, err := repository.NewStageLogRepo().GetByStageID(stages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	byID := map[uint]model.StageLog{}
	for _, entry := range logs {
		text.WriteString(entry.Content + "\n")
		byID[entry.ID] = entry
	}
	for _, want := range []string{"独立应用：web-cdr", "子应用：门诊", "[阶段失败]", "docker_registry_url", "未配置", "停止后续步骤"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("missing persisted detail %q: %s", want, text.String())
		}
	}
	failures := 0
	for len(events) > 0 {
		event := <-events
		if event.Type != "stage_log" {
			continue
		}
		var live model.StageLog
		if err := json.Unmarshal([]byte(event.Data), &live); err != nil {
			t.Fatal(err)
		}
		stored, ok := byID[live.ID]
		if !ok || live.StageID != stages[0].ID || live.Content != stored.Content || live.Stream != stored.Stream {
			t.Fatalf("live/history log mismatch: %+v", live)
		}
		if live.Stream == "stderr" && strings.Contains(live.Content, "[阶段失败]") {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("expected exactly one structured stage failure, got %d", failures)
	}
}

type deploymentFailureRunner struct{ state string }

func (r deploymentFailureRunner) Name() string { return "滚动更新" }
func (r deploymentFailureRunner) Code() string { return "K8S_DEPLOY" }
func (r deploymentFailureRunner) Run(context.Context, *types.PipelineContext) (*types.StageResult, error) {
	return &types.StageResult{ExitCode: 1, Error: "模拟部署失败", OnlineState: r.state}, errors.New("模拟部署失败")
}

func TestDeploymentFailureLogsActualSubmissionBoundary(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}, &model.StageLog{}, &model.Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, state string }{
		{"precheck", "本阶段尚未向 Kubernetes 提交修改，线上镜像未由本阶段改变"},
		{"submitted", "正式镜像已提交，滚动更新结果尚未确认"},
		{"completed", "指定镜像已完成滚动更新，后续记录保存失败"},
		{"unknown", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &model.Pipeline{PipelineNo: "log-boundary-" + tc.name, AppName: "web-main", AppType: "vue", TriggerUser: "test", DeployMode: "artifact_image", K8sNamespace: "test"}
			if err := repository.DB.Create(p).Error; err != nil {
				t.Fatal(err)
			}
			pc := &types.PipelineContext{PipelineID: p.ID, Pipeline: p, AppName: p.AppName, AppType: p.AppType, VueRole: "main", K8sNamespace: "test"}
			runner := deploymentFailureRunner{state: tc.state}
			engine := NewEngine()
			engine.sseHub = sse.NewHub()
			if err := engine.executeStages(context.Background(), p, pc, []StageDefinition{{Code: runner.Code(), Name: runner.Name(), Runner: runner}}); err == nil {
				t.Fatal("expected deployment failure")
			}
			stages, err := repository.NewStageRepo().FindByPipelineID(p.ID)
			if err != nil || len(stages) != 1 {
				t.Fatalf("missing stage: %v %v", stages, err)
			}
			logs, err := repository.NewStageLogRepo().GetByStageID(stages[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			var stateLogs []string
			for _, entry := range logs {
				if strings.HasPrefix(entry.Content, "[线上状态]") {
					stateLogs = append(stateLogs, entry.Content)
				}
			}
			want := tc.state
			if want == "" {
				want = "部署结果尚未确认，请结合提交日志核对 Deployment 的镜像和滚动状态"
			}
			if len(stateLogs) != 1 || stateLogs[0] != "[线上状态] "+want {
				t.Fatalf("contradictory deployment state: %v", stateLogs)
			}
		})
	}
}
