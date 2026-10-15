package pipeline

import (
	"context"
	"encoding/json"
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
