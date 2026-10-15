package stages

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
)

func TestLocalBuildRunsHostCommandAndEnvironment(t *testing.T) {
	pc := &types.PipelineContext{Workspace: &types.Workspace{BaseDir: t.TempDir(), AppName: "app", PipelineID: 1},
		BuildConfig:  &model.BuildConfig{EnvVars: "PILOT_COMPILE_VALUE=host-tools"},
		BuildCommand: `printf '%s' "$PILOT_COMPILE_VALUE" > result.txt`, OnLog: func(uint, uint, string, string) {}}
	if err := os.MkdirAll(pc.Workspace.SourceDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := (&LocalBuildStage{}).runLocal(context.Background(), pc); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(pc.Workspace.SourceDir(), "result.txt"))
	if err != nil || string(got) != "host-tools" {
		t.Fatalf("host result=%q, error=%v", got, err)
	}
	pc.BuildCommand = "exit 7"
	result, err := (&LocalBuildStage{}).runLocal(context.Background(), pc)
	if err == nil || result.ExitCode != 7 {
		t.Fatalf("lost compilation failure: %+v, %v", result, err)
	}
}

func TestConfiguredArtifactDirectoriesAndVueArchive(t *testing.T) {
	workspace := &types.Workspace{BaseDir: t.TempDir(), AppName: "app", PipelineID: 2}
	pc := &types.PipelineContext{Workspace: workspace, AppType: "java", ArtifactDir: "target"}
	got, err := sourceArtifactDirectory(pc)
	if err != nil || got != filepath.Join(workspace.SourceDir(), "target") {
		t.Fatalf("Maven directory=%q, %v", got, err)
	}
	for _, dir := range []string{"../outside", "/tmp/outside"} {
		pc.ArtifactDir = dir
		if _, err := sourceArtifactDirectory(pc); err == nil {
			t.Fatalf("accepted %q", dir)
		}
	}
	pc.AppType, pc.ArtifactDir, pc.ArtifactName = "vue", "out", "app.zip"
	pc.OnLog = func(uint, uint, string, string) {}
	output := filepath.Join(workspace.SourceDir(), "out")
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("built Vue"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ZipPackageStage{}).Run(context.Background(), pc); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(filepath.Join(workspace.SourceDir(), "app.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "index.html" {
		t.Fatalf("unexpected archive: %+v", zr.File)
	}
	pc.ArtifactDir = "."
	if _, err := (&ZipPackageStage{}).Run(context.Background(), pc); err == nil {
		t.Fatal("accepted self-containing output directory")
	}
}
