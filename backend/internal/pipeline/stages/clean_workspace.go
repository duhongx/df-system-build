package stages

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"df-build-server/internal/pipeline/types"
)

type CleanWorkspaceStage struct{}

func (s *CleanWorkspaceStage) Name() string { return "清理工作区" }
func (s *CleanWorkspaceStage) Code() string { return "CLEAN_WORKSPACE" }

func (s *CleanWorkspaceStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	root := pCtx.Workspace.PipelineDir()
	pCtx.OnLog(pCtx.PipelineID, 0, "清理工作目录: "+root, "stdout")
	os.RemoveAll(root)
	os.MkdirAll(root+"/source", 0755)
	os.MkdirAll(root+"/artifacts", 0755)

	// Cleanup old pipelines: keep only the latest 3 per app
	appDir := fmt.Sprintf("%s/%s", pCtx.Workspace.BaseDir, pCtx.AppName)
	s.cleanOldPipelines(appDir, 3, pCtx)

	pCtx.OnLog(pCtx.PipelineID, 0, "工作目录已就绪", "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}

// cleanOldPipelines removes pipeline directories exceeding maxKeep count
func (s *CleanWorkspaceStage) cleanOldPipelines(appDir string, maxKeep int, pCtx *types.PipelineContext) {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return
	}

	// Collect pipeline directories
	type pipelineDir struct {
		name string
		id   int
	}
	var dirs []pipelineDir
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "pipeline-") {
			idStr := strings.TrimPrefix(e.Name(), "pipeline-")
			id := 0
			fmt.Sscanf(idStr, "%d", &id)
			dirs = append(dirs, pipelineDir{name: e.Name(), id: id})
		}
	}

	if len(dirs) <= maxKeep {
		return
	}

	// Sort by ID ascending (oldest first)
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].id < dirs[j].id })

	// Remove oldest directories (keep the latest maxKeep)
	toRemove := dirs[:len(dirs)-maxKeep]
	for _, d := range toRemove {
		path := appDir + "/" + d.name
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("清理历史构建: %s", d.name), "stdout")
		os.RemoveAll(path)
	}
}
