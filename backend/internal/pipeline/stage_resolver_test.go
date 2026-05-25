package pipeline

import (
	"testing"

	"pgregory.net/rapid"
)

// Property: Stage resolution for deploy mode (default)
func TestPropertyStageResolution(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		appType := rapid.SampledFrom([]string{"java", "vue"}).Draw(t, "appType")

		stages := ResolveStages(appType) // default = "deploy" mode

		switch appType {
		case "java":
			// CLEAN_WORKSPACE, GIT_CLONE, GRADLE_BUILD, BUILD_IMAGE, PUSH_IMAGE, K8S_DEPLOY, NOTIFY
			if len(stages) != 7 {
				t.Fatalf("Java deploy should have 7 stages, got %d", len(stages))
			}
			expectedCodes := []string{"CLEAN_WORKSPACE", "GIT_CLONE", "GRADLE_BUILD", "BUILD_IMAGE", "PUSH_IMAGE", "K8S_DEPLOY", "NOTIFY"}
			for i, code := range expectedCodes {
				if stages[i].Code != code {
					t.Fatalf("Java stage %d should be %s, got %s", i, code, stages[i].Code)
				}
			}
		case "vue":
			// CLEAN_WORKSPACE, GIT_CLONE, YARN_BUILD, ZIP_PACKAGE, BUILD_IMAGE, PUSH_IMAGE, K8S_DEPLOY, NOTIFY
			if len(stages) != 8 {
				t.Fatalf("Vue deploy should have 8 stages, got %d", len(stages))
			}
			expectedCodes := []string{"CLEAN_WORKSPACE", "GIT_CLONE", "YARN_BUILD", "ZIP_PACKAGE", "BUILD_IMAGE", "PUSH_IMAGE", "K8S_DEPLOY", "NOTIFY"}
			for i, code := range expectedCodes {
				if stages[i].Code != code {
					t.Fatalf("Vue stage %d should be %s, got %s", i, code, stages[i].Code)
				}
			}
		}
	})
}

// Property: Unknown app type returns nil
func TestPropertyUnknownAppType(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		appType := rapid.StringMatching(`[a-z]{3,10}`).Draw(t, "appType")
		if appType == "java" || appType == "vue" {
			return
		}
		stages := ResolveStages(appType)
		if stages != nil {
			t.Fatalf("Unknown app type %s should return nil, got %d stages", appType, len(stages))
		}
	})
}

// Property: Last stage is always NOTIFY
func TestPropertyLastStageNotify(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		appType := rapid.SampledFrom([]string{"java", "vue"}).Draw(t, "appType")
		mode := rapid.SampledFrom([]string{"deploy", "deploy_with_approval", "artifact_deploy"}).Draw(t, "mode")

		stages := ResolveStagesWithMode(appType, mode)
		if stages == nil {
			return
		}
		if stages[len(stages)-1].Code != "NOTIFY" {
			t.Fatalf("Last stage should be NOTIFY, got %s", stages[len(stages)-1].Code)
		}
	})
}

func TestDeployWithApprovalIncludesImagePushAndK8sStages(t *testing.T) {
	stages := ResolveStagesWithMode("java", "deploy_with_approval")
	expectedCodes := []string{"CLEAN_WORKSPACE", "GIT_CLONE", "GRADLE_BUILD", "BUILD_IMAGE", "PUSH_IMAGE", "K8S_DEPLOY", "NOTIFY"}
	assertStageCodes(t, stages, expectedCodes)
}

func TestArtifactDeploySkipsSourceBuildAndDeploysImage(t *testing.T) {
	stages := ResolveStagesWithMode("vue", "artifact_deploy")
	expectedCodes := []string{"BUILD_IMAGE", "PUSH_IMAGE", "K8S_DEPLOY", "NOTIFY"}
	assertStageCodes(t, stages, expectedCodes)
}

func assertStageCodes(t *testing.T, stages []StageDefinition, expected []string) {
	t.Helper()
	if len(stages) != len(expected) {
		t.Fatalf("expected %d stages, got %d", len(expected), len(stages))
	}
	for i, code := range expected {
		if stages[i].Code != code {
			t.Fatalf("stage %d should be %s, got %s", i, code, stages[i].Code)
		}
	}
}
