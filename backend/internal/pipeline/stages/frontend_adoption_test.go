package stages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/frontend"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
)

func TestFrontendExistingDirectoryIdentity(t *testing.T) {
	app := model.Application{AppName: "web-child", AppType: "vue", VueRole: "sub", AppCode: "21", Enabled: true, AdoptExistingDirectory: true}
	for _, tc := range []struct {
		name, config, manifestOwner string
		adopt, wantError            bool
	}{
		{name: "explicit legacy adoption", config: `{"xiTongId":"21"}`, adopt: true},
		{name: "matching build manifest without platform history", manifestOwner: app.AppName},
		{name: "different application cannot be adopted implicitly", manifestOwner: "web-other", adopt: true, wantError: true},
		{name: "different legacy code", config: `{"xiTongId":"20"}`, adopt: true, wantError: true},
		{name: "malformed existing identity", config: `{`, adopt: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "apps/21")
			mustWriteFile(t, filepath.Join(root, "index.html"), "old")
			if tc.config != "" {
				mustWriteFile(t, filepath.Join(root, "config.json"), tc.config)
			}
			if tc.manifestOwner != "" {
				if err := frontend.WriteBuildManifest(root, frontend.BuildManifest{AppName: tc.manifestOwner, Role: "sub", AppCode: "21"}); err != nil {
					t.Fatal(err)
				}
			}
			selected := app
			selected.AdoptExistingDirectory = tc.adopt
			confirmed, err := confirmFrontendDirectory(&types.PipelineContext{}, base, selected)
			if (err != nil) != tc.wantError || (!tc.wantError && !confirmed) {
				t.Fatalf("confirmed=%t error=%v", confirmed, err)
			}
		})
	}
}

func TestFrontendLegacyBatchDirectoryOwnership(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Application{}, &model.Pipeline{}, &model.ArtifactDeployRecord{}); err != nil {
		t.Fatal(err)
	}
	repository.SeedApplications()
	// Reproduce the old migration-before-seeding gap for all 25 selected apps,
	// including the primary app whose failed build already updated its timestamp.
	if err := repository.DB.Model(&model.Application{}).Where("vue_role = ?", "sub").UpdateColumn("adopt_existing_directory", false).Error; err != nil {
		t.Fatal(err)
	}
	var apps []model.Application
	if err := repository.DB.Where("vue_role = ?", "sub").Find(&apps).Error; err != nil {
		t.Fatal(err)
	}
	byCode := map[string]model.Application{}
	for _, app := range apps {
		byCode[app.AppCode] = app
	}
	if err := repository.NewApplicationRepo().UpdateBuildStatus(byCode["00"].ID, "FAILED"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pCtx := &types.PipelineContext{PipelineID: 66, Pipeline: &model.Pipeline{ID: 66}, AppName: "web-bui", VueRole: "sub", K8sNamespace: "prod", Workspace: &types.Workspace{BaseDir: root, PipelineID: 66, AppName: "web-bui"}}
	if err := os.MkdirAll(pCtx.Workspace.SourceDir(), 0755); err != nil {
		t.Fatal(err)
	}
	base, out := filepath.Join(root, "baseline"), filepath.Join(root, "assembled")
	mustWriteFile(t, filepath.Join(base, "index.html"), "main-preserved")
	mustWriteFile(t, filepath.Join(base, "apps/gymk/index.html"), "gymk-preserved")
	codes := strings.Split("00,04,05,06,08,10,11,12,15,18,19,20,21,23,27,36,37,71,75,80,81,83,88,94,99", ",")
	var artifacts []model.FrontendArtifact
	for _, code := range codes {
		app := byCode[code]
		config := `{"xiTongId":"` + code + `"}`
		mustWriteFile(t, filepath.Join(base, "apps", code, "index.html"), "old-"+code)
		mustWriteFile(t, filepath.Join(base, "apps", code, "config.json"), config)
		writeTestZip(t, filepath.Join(pCtx.Workspace.SourceDir(), code+".zip"), map[string]string{"index.html": "new-" + code, "config.json": config})
		artifacts = append(artifacts, model.FrontendArtifact{AppID: app.ID, AppName: app.AppName, Role: "sub", AppCode: code, FileName: code + ".zip"})
	}
	before, err := hashWebTree(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepareWebUpdate(pCtx.Workspace.SourceDir(), filepath.Join(root, "inputs"), artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFrontendRegistrations(pCtx, plan.inputs, plan, base); err != nil {
		t.Fatalf("25-app legacy update rejected: %v", err)
	}
	if len(plan.bindings) != 25 {
		t.Fatalf("missing pre-assembly application bindings: %d", len(plan.bindings))
	}
	updated, preserved, err := plan.assemble(base, out, false)
	if err != nil || len(updated) != 25 || strings.Join(preserved, ",") != "apps/gymk" {
		t.Fatalf("unexpected batch assembly: %v %v %v", updated, preserved, err)
	}
	for _, code := range codes {
		assertWebContent(t, filepath.Join(out, "apps", code, "index.html"), "new-"+code)
	}
	assertWebContent(t, filepath.Join(out, "apps/gymk/index.html"), "gymk-preserved")
	assertWebContent(t, filepath.Join(out, "index.html"), "main-preserved")
	if after, err := hashWebTree(base); err != nil || after != before {
		t.Fatalf("live baseline changed: %v", err)
	}
	if len(plan.regions) != 25 {
		t.Fatalf("missing assembled regions: %d", len(plan.regions))
	}
	for _, region := range plan.regions {
		if region.AppID == 0 || region.AppCode == "" || region.AppID != byCode[region.AppCode].ID || region.AppName != byCode[region.AppCode].AppName {
			t.Fatalf("batch region assigned to primary app instead of its own registration: %+v", region)
		}
	}
	var persisted []model.FrontendArtifact
	if err := json.Unmarshal([]byte(pCtx.Pipeline.FrontendArtifactsJSON), &persisted); err != nil || len(persisted) != 25 {
		t.Fatalf("lost selected inputs: %v", err)
	}

	t.Run("unknown custom directories are reported together", func(t *testing.T) {
		for _, code := range []string{"42", "43"} {
			app := model.Application{AppName: "web-custom-" + code, AppType: "vue", VueRole: "sub", AppCode: code, Enabled: true}
			if err := repository.DB.Create(&app).Error; err != nil {
				t.Fatal(err)
			}
			mustWriteFile(t, filepath.Join(base, "apps", code, "config.json"), `{"xiTongId":"`+code+`"}`)
		}
		unknown := &webUpdatePlan{children: map[string]string{"43": "unused", "42": "unused"}}
		err := validateFrontendRegistrations(pCtx, nil, unknown, base)
		if err == nil || !strings.Contains(err.Error(), "apps/42（web-custom-42）、apps/43（web-custom-43）") || strings.Contains(err.Error(), "不属于") {
			t.Fatalf("wrong unknown ownership result: %v", err)
		}
	})
	t.Run("legacy identity cannot be reassigned by directory number", func(t *testing.T) {
		original := byCode["21"]
		for _, changed := range []model.Application{
			{AppName: "web-someone-else", AppType: "vue", VueRole: "sub", AppCode: "21", GitRepo: original.GitRepo},
			{AppName: original.AppName, AppType: "vue", VueRole: "sub", AppCode: "21", GitRepo: "ssh://custom/other.git"},
		} {
			if ok, err := confirmFrontendDirectory(pCtx, base, changed); err != nil || ok {
				t.Fatalf("unrelated binding claimed legacy directory: %+v %t %v", changed, ok, err)
			}
		}
	})
	t.Run("disabled and replaced registrations are rejected", func(t *testing.T) {
		app := byCode["21"]
		if err := repository.DB.Model(&app).UpdateColumn("enabled", false).Error; err != nil {
			t.Fatal(err)
		}
		if err := validateFrontendRegistrations(pCtx, plan.inputs, plan, base); err == nil || !strings.Contains(err.Error(), "21.zip") {
			t.Fatalf("disabled selected app accepted: %v", err)
		}
		if err := repository.DB.Model(&app).UpdateColumn("enabled", true).Error; err != nil {
			t.Fatal(err)
		}
		stale := append([]model.FrontendArtifact(nil), plan.inputs...)
		stale[0].AppID += 100000
		if err := validateFrontendRegistrations(pCtx, stale, plan, base); err == nil {
			t.Fatal("stale approval accepted")
		}
	})
	t.Run("unidentified legacy directory needs explicit adoption", func(t *testing.T) {
		app := byCode["21"]
		if err := os.Remove(filepath.Join(base, "apps/21/config.json")); err != nil {
			t.Fatal(err)
		}
		if ok, err := confirmFrontendDirectory(pCtx, base, app); err != nil || ok {
			t.Fatalf("missing identity treated as owned: %t %v", ok, err)
		}
		app.AdoptExistingDirectory = true
		if ok, err := confirmFrontendDirectory(pCtx, base, app); err != nil || !ok {
			t.Fatalf("explicit adoption rejected: %t %v", ok, err)
		}
		app.AdoptExistingDirectory = false
		record := model.ArtifactDeployRecord{DeployBatchNo: "history", VersionNo: "v1", AppID: app.ID, AppCode: app.AppCode, DeploymentName: "web-main", Namespace: "other", DeployStatus: "success"}
		if err := repository.DB.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
		if ok, err := confirmFrontendDirectory(pCtx, base, app); err != nil || ok {
			t.Fatalf("other namespace granted ownership: %t %v", ok, err)
		}
		if err := repository.DB.Model(&record).Update("namespace", "prod").Error; err != nil {
			t.Fatal(err)
		}
		if ok, err := confirmFrontendDirectory(pCtx, base, app); err != nil || !ok {
			t.Fatalf("region publication history ignored: %t %v", ok, err)
		}
	})
	t.Run("main standalone and CDN retain their own region binding", func(t *testing.T) {
		cdn := model.Application{AppName: "web-cdn", AppType: "vue", VueRole: "cdn", Enabled: true}
		if err := repository.DB.Create(&cdn).Error; err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"web-main", "web-opm", "web-cdn"} {
			var app model.Application
			if err := repository.DB.Where("app_name = ?", name).First(&app).Error; err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			files := map[string]string{"index.html": "new"}
			regionPath := "."
			if app.VueRole == "cdn" {
				files = map[string]string{"lib/1/library.js": "library"}
				regionPath = "apps/cdn"
			}
			writeTestZip(t, filepath.Join(dir, app.DeriveArtifactName()), files)
			p := &types.PipelineContext{AppName: name, K8sNamespace: "prod", Pipeline: &model.Pipeline{}}
			selected := []model.FrontendArtifact{{AppID: app.ID, AppName: name, Role: app.VueRole, FileName: app.DeriveArtifactName()}}
			update, err := prepareWebUpdate(dir, filepath.Join(dir, "inputs"), selected)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateFrontendRegistrations(p, update.inputs, update, base); err != nil {
				t.Fatal(err)
			}
			if _, _, err := update.assemble(base, filepath.Join(dir, "out"), false); err != nil {
				t.Fatal(err)
			}
			if len(update.regions) != 1 || update.regions[0].AppID != app.ID || update.regions[0].AppName != name || update.regions[0].Role != app.VueRole || update.regions[0].Path != regionPath {
				t.Fatalf("wrong binding for %s: %+v", name, update.regions)
			}
		}
	})
}
