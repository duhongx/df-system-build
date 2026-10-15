package stages

import (
	"context"
	"df-build-server/internal/frontend"
	"fmt"
	"path/filepath"

	"df-build-server/internal/pipeline/types"
)

type ZipPackageStage struct{}

func (s *ZipPackageStage) Name() string { return "打包制品" }
func (s *ZipPackageStage) Code() string { return "ZIP_PACKAGE" }

func (s *ZipPackageStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	sourceDir := pCtx.Workspace.SourceDir()
	zipName := pCtx.ArtifactName
	zipPath := fmt.Sprintf("%s/%s", sourceDir, zipName)
	artifactDir, err := sourceArtifactDirectory(pCtx)
	if err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	if filepath.Clean(artifactDir) == filepath.Clean(sourceDir) {
		err := fmt.Errorf("前端产物目录不能为源码根目录，请指定 dist 等编译输出目录")
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("打包 %s → %s", artifactDir, zipName), "stdout")
	commit := ""
	if pCtx.Pipeline != nil {
		commit = pCtx.Pipeline.GitCommitHash
	}
	if err := frontend.WriteBuildManifest(artifactDir, frontend.BuildManifest{AppName: pCtx.AppName, Role: pCtx.VueRole, AppCode: pCtx.AppCode, Commit: commit}); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	if err := zipDirectory(artifactDir, ".", zipPath, pCtx.VueRole == "main"); err != nil {
		errMsg := fmt.Sprintf("打包失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("打包完成: %s", zipName), "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}
