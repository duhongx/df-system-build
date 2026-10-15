package stages

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"df-build-server/internal/pipeline/types"
)

var imageBranchCharacters = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
var imageCommitCharacters = regexp.MustCompile(`^[a-fA-F0-9]{8,64}$`)

// buildImageName keeps the registry host separate from the prod image namespace,
// so authentication and base-image paths continue to use the registry host.
func buildImageName(pCtx *types.PipelineContext, registryURL, appName string, now time.Time) (string, error) {
	branch := strings.Trim(imageBranchCharacters.ReplaceAllString(strings.TrimSpace(pCtx.GitBranch), "-"), ".-")
	if branch == "" {
		return "", fmt.Errorf("镜像标签缺少有效的 Git 分支")
	}
	timestamp := now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("20060102150405")
	commit := ""
	if pCtx.Pipeline != nil {
		commit = strings.TrimSpace(pCtx.Pipeline.GitCommitHash)
	}
	var tag string
	if commit == "" && (pCtx.DeployMode == "artifact_image" || pCtx.DeployMode == "artifact_deploy" || pCtx.DeployMode == "manual" || pCtx.GitBranch == "manual-upload") {
		// Uploaded artifacts may not carry Git metadata. Keep their existing
		// identifier until an explicit artifact naming policy is selected.
		suffix := fmt.Sprintf("-%s-p%d", timestamp, pCtx.PipelineID)
		if len(branch) > 128-len(suffix) {
			branch = branch[:128-len(suffix)]
		}
		tag = branch + suffix
	} else {
		if !imageCommitCharacters.MatchString(commit) {
			return "", fmt.Errorf("镜像标签缺少有效的 Git commitid，请检查代码拉取阶段")
		}
		suffix := "-" + strings.ToLower(commit[:8]) + "-" + timestamp
		if len(branch) > 128-len(suffix) {
			branch = branch[:128-len(suffix)]
		}
		tag = branch + suffix
	}
	return strings.TrimRight(registryURL, "/") + "/prod/" + appName + ":" + tag, nil
}
