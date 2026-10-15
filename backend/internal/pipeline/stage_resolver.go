package pipeline

import (
	"df-build-server/internal/pipeline/stages"
	"df-build-server/internal/pipeline/types"
)

// StageDefinition describes a stage to be created
type StageDefinition struct {
	Code   string
	Name   string
	Runner types.StageRunner
}

// ResolveStages returns the ordered stage list based on app type (default deploy mode)
func ResolveStages(appType string) []StageDefinition {
	return ResolveStagesWithMode(appType, "deploy")
}

// NormalizeDeployMode keeps stored tasks compatible while all image workflows
// use the environment to choose automatic or separate rollout.
func NormalizeDeployMode(mode string) string {
	switch mode {
	case "artifact_image", "artifact_deploy", "manual":
		return "artifact_image"
	case "upload_only":
		return "upload_only"
	case "upload_and_build", "upload_and_deploy", "upload_deploy":
		return "upload_and_build"
	default:
		return "build_image"
	}
}

// ResolveStagesWithMode describes both steps. Outside local environments the engine ends step one after
// PUSH_IMAGE; local execution continues directly through K8S_DEPLOY and NOTIFY.
func ResolveStagesWithMode(appType, deployMode string) []StageDefinition {
	deployMode = NormalizeDeployMode(deployMode)
	if deployMode == "artifact_image" {
		defs := []StageDefinition{
			{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			{Code: "K8S_DEPLOY", Name: "滚动更新", Runner: &stages.K8sDeployStage{}},
			{Code: "NOTIFY", Name: "发送通知", Runner: &stages.NotifyStage{}},
		}
		return defs
	}

	var defs []StageDefinition

	// Common: clean workspace + git clone + compile
	switch appType {
	case "java":
		defs = []StageDefinition{
			{Code: "CLEAN_WORKSPACE", Name: "清理工作区", Runner: &stages.CleanWorkspaceStage{}},
			{Code: "GIT_CLONE", Name: "拉取代码", Runner: &stages.GitCloneStage{}},
			{Code: "GRADLE_BUILD", Name: "本机编译", Runner: &stages.LocalBuildStage{}},
		}
	case "vue":
		defs = []StageDefinition{
			{Code: "CLEAN_WORKSPACE", Name: "清理工作区", Runner: &stages.CleanWorkspaceStage{}},
			{Code: "GIT_CLONE", Name: "拉取代码", Runner: &stages.GitCloneStage{}},
			{Code: "YARN_BUILD", Name: "安装依赖并编译", Runner: &stages.LocalBuildStage{}},
			{Code: "ZIP_PACKAGE", Name: "打包制品", Runner: &stages.ZipPackageStage{}},
		}
	default:
		return nil
	}

	// Append stages based on deploy mode
	switch deployMode {
	case "build_image":
		// DevOps mode: build image → push → K8s deploy
		defs = append(defs,
			StageDefinition{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			StageDefinition{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			StageDefinition{Code: "K8S_DEPLOY", Name: "滚动更新", Runner: &stages.K8sDeployStage{}},
		)
	case "upload_only":
		// Master mode: upload to remote servers only
		defs = append(defs,
			StageDefinition{Code: "CHECK_REMOTE_DIR", Name: "检查远端目录", Runner: &stages.CheckRemoteDirStage{}},
			StageDefinition{Code: "UPLOAD_ARTIFACT", Name: "上传制品", Runner: &stages.UploadArtifactStage{}},
		)
	case "upload_and_build":
		// Both: upload + image + K8s
		defs = append(defs,
			StageDefinition{Code: "CHECK_REMOTE_DIR", Name: "检查远端目录", Runner: &stages.CheckRemoteDirStage{}},
			StageDefinition{Code: "UPLOAD_ARTIFACT", Name: "上传制品", Runner: &stages.UploadArtifactStage{}},
			StageDefinition{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			StageDefinition{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			StageDefinition{Code: "K8S_DEPLOY", Name: "滚动更新", Runner: &stages.K8sDeployStage{}},
		)
	default:
		defs = append(defs,
			StageDefinition{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			StageDefinition{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			StageDefinition{Code: "K8S_DEPLOY", Name: "滚动更新", Runner: &stages.K8sDeployStage{}},
		)
	}

	// Always end with notify
	defs = append(defs, StageDefinition{Code: "NOTIFY", Name: "发送通知", Runner: &stages.NotifyStage{}})

	return defs
}
