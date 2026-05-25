package service

import (
	"strings"
	"testing"

	"df-build-server/internal/model"

	"pgregory.net/rapid"
)

// Property 1: Artifact Naming Derivation
func TestPropertyArtifactNaming(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		appType := rapid.SampledFrom([]string{"java", "vue"}).Draw(t, "appType")
		appName := rapid.StringMatching(`[a-z][a-z0-9\-]{2,20}`).Draw(t, "appName")
		vueRole := rapid.SampledFrom([]string{"main", "sub", "standalone"}).Draw(t, "vueRole")
		appCode := rapid.StringMatching(`[a-z0-9]{2,6}`).Draw(t, "appCode")

		app := &model.Application{
			AppName: appName,
			AppType: appType,
			VueRole: vueRole,
			AppCode: appCode,
		}

		result := app.DeriveArtifactName()

		switch appType {
		case "java":
			if !strings.HasSuffix(result, ".jar") {
				t.Fatalf("Java app should produce .jar, got: %s", result)
			}
			if result != appName+".jar" {
				t.Fatalf("Java artifact should be %s.jar, got: %s", appName, result)
			}
		case "vue":
			if !strings.HasSuffix(result, ".zip") {
				t.Fatalf("Vue app should produce .zip, got: %s", result)
			}
			switch vueRole {
			case "main":
				if result != "web-main.zip" {
					t.Fatalf("Vue main app should produce web-main.zip, got: %s", result)
				}
			case "sub":
				if result != appCode+".zip" {
					t.Fatalf("Vue sub app with code %s should produce %s.zip, got: %s", appCode, appCode, result)
				}
			case "standalone":
				if result != appName+".zip" {
					t.Fatalf("Vue standalone app should produce %s.zip, got: %s", appName, result)
				}
			}
		}
	})
}
