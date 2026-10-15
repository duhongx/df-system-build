package stages

import (
	"df-build-server/internal/pipeline/types"
	"fmt"
	"path/filepath"
	"strings"
)

func sourceArtifactDirectory(pCtx *types.PipelineContext) (string, error) {
	dir := strings.TrimSpace(pCtx.ArtifactDir)
	if pCtx.AppType == "vue" && pCtx.VueRole == "cdn" {
		dir = "cdn"
	}
	if dir == "" {
		dir = "build/libs"
		if pCtx.AppType == "vue" {
			dir = "dist"
		}
	}
	dir = filepath.Clean(dir)
	if filepath.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("产物目录必须位于当前任务源码目录内")
	}
	return filepath.Join(pCtx.Workspace.SourceDir(), dir), nil
}
