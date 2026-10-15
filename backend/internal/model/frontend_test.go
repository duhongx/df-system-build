package model

import "testing"

func TestFrontendIdentityAndTargetRules(t *testing.T) {
	for _, code := range []string{"00", "04", "42", "gymk", "new_app-1"} {
		if !ValidSubAppCode(code) {
			t.Fatal(code)
		}
	}
	for _, code := range []string{"cdn", "../04", "/04", "04/other", "", "ABC", ".", "a b"} {
		if ValidSubAppCode(code) {
			t.Fatal(code)
		}
	}
	for _, app := range []Application{{AppName: "web-main", AppType: "vue", VueRole: "main"}, {AppName: "web-example", AppType: "vue", VueRole: "sub", AppCode: "42"}, {AppName: "web-cdn", AppType: "vue", VueRole: "cdn"}, {AppName: "web-opm", AppType: "vue", VueRole: "standalone"}} {
		if err := app.ValidateIdentity(); err != nil {
			t.Fatal(err)
		}
		if app.VueRole != "standalone" && app.DeploymentTarget() != "web-main" {
			t.Fatal(app)
		}
	}
	if err := (Application{AppName: "other-main", AppType: "vue", VueRole: "main"}).ValidateIdentity(); err == nil {
		t.Fatal("second main accepted")
	}
}
