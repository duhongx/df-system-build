package stages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
)

func setupImageFlowDB(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Settings{}, &model.ConfigItem{}); err != nil {
		t.Fatal(err)
	}
}

func TestJavaScriptPreparationReportsMissingEmptyAndWriteErrors(t *testing.T) {
	setupImageFlowDB(t)
	s := &BuildImageStage{}
	var logs []string
	pc := &types.PipelineContext{AppName: "service", OnLog: func(_, _ uint, line, stream string) { logs = append(logs, line) }}
	dir := t.TempDir()
	if err := s.copyScripts(pc, dir, "registry"); err == nil || !strings.Contains(err.Error(), "app-sh-java") {
		t.Fatalf("missing script ignored: %v", err)
	}
	item := model.ConfigItem{Code: "app-sh-java", Name: "app", Content: ""}
	if err := repository.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.copyScripts(pc, dir, "registry"); err == nil || !strings.Contains(err.Error(), "内容为空") {
		t.Fatalf("empty script ignored: %v", err)
	}
	item.Content = "#!/bin/sh\nexec java -jar /opt/app.jar\n"
	if err := repository.DB.Save(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "app.sh"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.copyScripts(pc, dir, "registry"); err == nil || !strings.Contains(err.Error(), "app.sh") {
		t.Fatalf("write failure ignored: %v", err)
	}
	if strings.Contains(strings.Join(logs, "\n"), "已生成") {
		t.Fatal("write failure logged as generated")
	}
}

func TestJavaImageBuildAndPushUseOnlyUniqueTag(t *testing.T) {
	setupImageFlowDB(t)
	if err := repository.NewSettingsRepo().Set("docker_registry_url", "registry.example"); err != nil {
		t.Fatal(err)
	}
	configs := []model.ConfigItem{
		{Name: "dockerfile", Code: "dockerfile-java", Content: "FROM base\nCOPY ${artifactName} /opt/app.jar\nCOPY app.sh /opt/app.sh\nCOPY delete_app.sh /opt/delete_app.sh\n"},
		{Name: "app", Code: "app-sh-java", Content: "#!/bin/sh\nexec java -jar /opt/app.jar\n"},
		{Name: "delete", Code: "delete-app-java", Content: "#!/bin/sh\nexit 0\n"},
	}
	if err := repository.DB.Create(&configs).Error; err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	var buildTags []string
	var pushed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.43")
			fmt.Fprint(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/build"):
			buildTags = append(buildTags, r.URL.Query()["t"]...)
			fmt.Fprint(w, `{"stream":"built\n"}`)
		case strings.HasSuffix(r.URL.Path, "/push"):
			pushed = append(pushed, r.URL.String())
			fmt.Fprintf(w, `{"aux":{"Digest":%q}}`, digest)
		case strings.Contains(r.URL.Path, "/distribution/"):
			fmt.Fprintf(w, `{"Descriptor":{"digest":%q}}`, digest)
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprintf(w, `{"Id":%q}`, digest)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	t.Setenv("DOCKER_HOST", srv.URL)
	t.Setenv("DOCKER_API_VERSION", "1.43")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	pc := &types.PipelineContext{PipelineID: 17, AppName: "service", AppType: "java", GitBranch: "manual-upload", ArtifactName: "service.jar", Workspace: &types.Workspace{BaseDir: t.TempDir(), AppName: "service", PipelineID: 17}, OnLog: func(uint, uint, string, string) {}}
	path := filepath.Join(pc.Workspace.SourceDir(), "build/libs/service.jar")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("jar fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&BuildImageStage{}).Run(context.Background(), pc); err != nil {
		t.Fatal(err)
	}
	if _, err := (&PushImageStage{}).Run(context.Background(), pc); err != nil {
		t.Fatal(err)
	}
	if len(buildTags) != 1 || buildTags[0] != pc.ImageName || strings.Contains(buildTags[0], "latest") || len(pushed) != 1 || strings.Contains(pushed[0], "latest") {
		t.Fatalf("unexpected tags: build=%v push=%v", buildTags, pushed)
	}
}
