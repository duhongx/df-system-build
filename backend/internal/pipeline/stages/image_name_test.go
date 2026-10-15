package stages

import (
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
)

func TestImageNameUsesProdBranchCommitAndShanghaiTime(t *testing.T) {
	now := time.Date(2026, 10, 14, 6, 26, 13, 0, time.UTC)
	pc := &types.PipelineContext{GitBranch: "RC_2.16.1_250514", Pipeline: &model.Pipeline{GitCommitHash: "67b5cf6e9db2bb3fad6ee07126e62b36cd365820"}}
	for _, app := range []string{"yb-yibaogl", "web-main", "web-cdr"} {
		got, err := buildImageName(pc, "192.168.1.154:8888/", app, now)
		want := "192.168.1.154:8888/prod/" + app + ":RC_2.16.1_250514-67b5cf6e-20261014142613"
		if err != nil || got != want {
			t.Fatalf("app=%s: got %q, %v; want %q", app, got, err, want)
		}
	}
}

func TestImageNameNormalizesAndBoundsBranch(t *testing.T) {
	pc := &types.PipelineContext{GitBranch: "feature/修复 auth", Pipeline: &model.Pipeline{GitCommitHash: strings.Repeat("A", 40)}}
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	got, err := buildImageName(pc, "registry", "app", now)
	if err != nil || got != "registry/prod/app:feature-auth-aaaaaaaa-20261014080000" {
		t.Fatalf("unexpected normalized image: %q, %v", got, err)
	}
	pc.GitBranch = strings.Repeat("branch", 40)
	got, err = buildImageName(pc, "registry", "app", now)
	_, tag, _ := strings.Cut(got, ":")
	if err != nil || len(tag) != 128 || !strings.HasSuffix(tag, "-aaaaaaaa-20261014080000") {
		t.Fatalf("invalid bounded tag: %q, %v", tag, err)
	}
}

func TestImageNameRejectsMissingOrInvalidSourceCommit(t *testing.T) {
	for _, commit := range []string{"", "abc", "not-a-commit-id"} {
		pc := &types.PipelineContext{GitBranch: "release", Pipeline: &model.Pipeline{GitCommitHash: commit}}
		if _, err := buildImageName(pc, "registry", "app", time.Now()); err == nil {
			t.Fatalf("accepted invalid source commit %q", commit)
		}
	}
}

func TestImageNamePreservesArtifactTagWithoutInventingCommit(t *testing.T) {
	pc := &types.PipelineContext{GitBranch: "manual-upload", DeployMode: "artifact_image", PipelineID: 17}
	got, err := buildImageName(pc, "registry", "app", time.Date(2026, 10, 14, 6, 26, 13, 0, time.UTC))
	if err != nil || got != "registry/prod/app:manual-upload-20261014142613-p17" {
		t.Fatalf("unexpected artifact image: %q, %v", got, err)
	}
}
