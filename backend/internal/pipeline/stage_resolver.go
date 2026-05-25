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

// ResolveStagesWithMode returns stages based on app type and deploy mode
// Deploy modes:
//   - "deploy" (default): 编译 → 构建镜像 → 推送 Nexus → K8s 部署
//   - "deploy_with_approval": 编译 → 构建镜像 → 推送 Nexus → 等待确认 → K8s 部署
//   - "artifact_deploy": 手动上传制品 → 构建镜像 → 推送 Nexus → K8s 部署
func ResolveStagesWithMode(appType, deployMode string) []StageDefinition {
	if deployMode == "" {
		deployMode = "deploy"
	}

	if deployMode == "artifact_deploy" || deployMode == "manual" {
		defs := []StageDefinition{
			{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			{Code: "K8S_DEPLOY", Name: "K8s 部署", Runner: &stages.K8sDeployStage{}},
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
			{Code: "GRADLE_BUILD", Name: "Gradle 编译", Runner: &stages.DockerBuildStage{}},
		}
	case "vue":
		defs = []StageDefinition{
			{Code: "CLEAN_WORKSPACE", Name: "清理工作区", Runner: &stages.CleanWorkspaceStage{}},
			{Code: "GIT_CLONE", Name: "拉取代码", Runner: &stages.GitCloneStage{}},
			{Code: "YARN_BUILD", Name: "安装依赖并编译", Runner: &stages.DockerBuildStage{}},
			{Code: "ZIP_PACKAGE", Name: "打包制品", Runner: &stages.ZipPackageStage{}},
		}
	default:
		return nil
	}

	// Append stages based on deploy mode
	switch deployMode {
	case "deploy", "deploy_with_approval":
		// DevOps mode: build image → push → K8s deploy
		defs = append(defs,
			StageDefinition{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			StageDefinition{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			StageDefinition{Code: "K8S_DEPLOY", Name: "K8s 部署", Runner: &stages.K8sDeployStage{}},
		)
	case "upload_only":
		// Master mode: upload to remote servers only
		defs = append(defs,
			StageDefinition{Code: "CHECK_REMOTE_DIR", Name: "检查远端目录", Runner: &stages.CheckRemoteDirStage{}},
			StageDefinition{Code: "UPLOAD_ARTIFACT", Name: "上传制品", Runner: &stages.UploadArtifactStage{}},
		)
	case "upload_and_deploy":
		// Both: upload + image + K8s
		defs = append(defs,
			StageDefinition{Code: "CHECK_REMOTE_DIR", Name: "检查远端目录", Runner: &stages.CheckRemoteDirStage{}},
			StageDefinition{Code: "UPLOAD_ARTIFACT", Name: "上传制品", Runner: &stages.UploadArtifactStage{}},
			StageDefinition{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			StageDefinition{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			StageDefinition{Code: "K8S_DEPLOY", Name: "K8s 部署", Runner: &stages.K8sDeployStage{}},
		)
	default:
		defs = append(defs,
			StageDefinition{Code: "BUILD_IMAGE", Name: "构建镜像", Runner: &stages.BuildImageStage{}},
			StageDefinition{Code: "PUSH_IMAGE", Name: "推送镜像", Runner: &stages.PushImageStage{}},
			StageDefinition{Code: "K8S_DEPLOY", Name: "K8s 部署", Runner: &stages.K8sDeployStage{}},
		)
	}

	// Always end with notify
	defs = append(defs, StageDefinition{Code: "NOTIFY", Name: "发送通知", Runner: &stages.NotifyStage{}})

	return defs
}
