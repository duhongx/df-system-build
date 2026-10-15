package stages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"df-build-server/internal/frontend"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
)

// ValidateFrontendArtifact is also used before claiming an uploaded file.
func ValidateFrontendArtifact(zip string, app model.Application) error {
	if !app.Enabled {
		return fmt.Errorf("应用已停用")
	}
	if err := app.ValidateIdentity(); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "df-web-preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	name := filepath.Base(zip)
	if name != app.DeriveArtifactName() {
		return fmt.Errorf("制品名称与应用登记不一致")
	}
	// Resolve uploads without assuming they are physically at their display name.
	input := filepath.Join(tmp, "source")
	if err := os.MkdirAll(input, 0755); err != nil {
		return err
	}
	if err := copyFile(zip, filepath.Join(input, name)); err != nil {
		return err
	}
	plan, err := prepareWebUpdate(input, filepath.Join(tmp, "inputs"), []model.FrontendArtifact{{FileName: name, Role: app.VueRole, AppCode: app.AppCode, AppName: app.AppName}})
	if err != nil {
		return err
	}
	for code, root := range plan.children {
		if err := validateInputReferences(root, "/apps/"+code+"/"); err != nil {
			return err
		}
	}
	if plan.mainRoot != "" {
		if err := validateInputReferences(plan.mainRoot, "/"); err != nil {
			return err
		}
	}
	return nil
}

func validateInputReferences(root, prefix string) error {
	refs, err := frontend.References(root, prefix)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if strings.HasPrefix(ref, "apps/cdn/") {
			continue
		}
		local := strings.TrimPrefix("/"+ref, prefix)
		info, err := os.Lstat(filepath.Join(root, local))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("制品引用的本地资源缺失: /%s", ref)
		}
	}
	return nil
}

func ValidateFrontendSelection(sourceDir string, artifacts []model.FrontendArtifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	tmp, err := os.MkdirTemp("", "df-web-selection-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	plan, err := prepareWebUpdate(sourceDir, tmp, artifacts)
	if err != nil {
		return err
	}
	pCtx := &types.PipelineContext{Pipeline: &model.Pipeline{}, Workspace: &types.Workspace{}}
	// Uploaded inputs already have fixed checksums. No workspace lookups needed.
	return validateFrontendRegistrations(pCtx, artifacts, plan, filepath.Join(tmp, "no-baseline"))
}

func validateFrontendRegistrations(pCtx *types.PipelineContext, artifacts []model.FrontendArtifact, plan *webUpdatePlan, baselineDir string) error {
	apps, err := repository.NewApplicationRepo().ListAll()
	if err != nil {
		return err
	}
	byName := map[string]model.Application{}
	byCode := map[string]model.Application{}
	for _, app := range apps {
		byName[app.AppName] = app
		if app.AppType == "vue" && app.VueRole == "sub" {
			if _, ok := byCode[app.AppCode]; ok {
				return fmt.Errorf("子应用编号 %s 存在重复登记", app.AppCode)
			}
			byCode[app.AppCode] = app
		}
	}
	for i := range artifacts {
		artifact := &artifacts[i]
		var app model.Application
		if artifact.Role == "sub" {
			app = byCode[artifact.AppCode]
		} else if artifact.Role == "main" {
			app = byName["web-main"]
		} else if artifact.Role == "cdn" {
			app = byName["web-cdn"]
		} else {
			app = byName[pCtx.AppName]
		}
		if app.ID == 0 || !app.Enabled || app.VueRole != artifact.Role || app.DeriveArtifactName() != artifact.FileName || (artifact.AppID != 0 && artifact.AppID != app.ID) {
			return fmt.Errorf("前端制品登记或启用状态已变化: %s", artifact.FileName)
		}
		if err := app.ValidateIdentity(); err != nil {
			return err
		}
		artifact.AppID, artifact.AppName = app.ID, app.AppName
		if artifact.SHA256 == "" {
			artifact.SHA256, err = hashWebFile(filepath.Join(pCtx.Workspace.SourceDir(), artifact.FileName))
			if err != nil {
				return err
			}
		}
	}
	// Embedded children are not granted publication rights merely by appearing
	// in a ZIP. Bind every declared region to the live application registry.
	if plan.cdnRoot != "" {
		app := byName["web-cdn"]
		if app.ID == 0 || !app.Enabled || app.VueRole != "cdn" {
			return fmt.Errorf("公共资源应用 web-cdn 未登记或已停用")
		}
		if declared, ok := plan.declared["cdn"]; ok && declared.AppName != "web-cdn" {
			return fmt.Errorf("整包公共资源必须绑定 web-cdn")
		}
	}
	for code := range plan.children {
		app := byCode[code]
		if app.ID == 0 || !app.Enabled {
			return fmt.Errorf("整包子应用 %s 未登记或已停用", code)
		}
		if declared, ok := plan.declared[code]; ok && declared.AppName != app.AppName {
			return fmt.Errorf("整包区域 %s 的应用名称与登记不一致", code)
		}
		if !app.AdoptExistingDirectory {
			if _, err := os.Stat(filepath.Join(baselineDir, "apps", code)); err == nil {
				var published int64
				if err := repository.DB.Model(&model.Pipeline{}).Where("application_id = ? AND deployment_progress = ?", app.ID, "completed").Count(&published).Error; err != nil {
					return err
				}
				if published == 0 {
					if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("app_id = ? AND deploy_status = ?", app.ID, "success").Count(&published).Error; err != nil {
						return err
					}
				}
				if published == 0 {
					return fmt.Errorf("apps/%s 已存在但不属于本应用的已发布目录；请核对后明确选择接管已有目录", code)
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	for i := range plan.regions {
		region := &plan.regions[i]
		name := pCtx.AppName
		if region.Role == "main" && !plan.standalone {
			name = "web-main"
		}
		if region.Role == "sub" {
			region.AppCode = strings.TrimPrefix(region.Path, "apps/")
			name = byCode[region.AppCode].AppName
		}
		if region.Role == "cdn" {
			name = "web-cdn"
		}
		app := byName[name]
		if app.ID == 0 || !app.Enabled {
			return fmt.Errorf("区域 %s 未登记或已停用", region.Path)
		}
		region.AppName = name
	}
	data, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	pCtx.Pipeline.FrontendArtifactsJSON = string(data)
	return nil
}

func InspectFrontendArtifact(zip string, app model.Application) ([]frontend.Region, error) {
	if err := ValidateFrontendArtifact(zip, app); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "df-web-inspect-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	plan, err := prepareWebUpdate(filepath.Dir(zip), tmp, []model.FrontendArtifact{{FileName: filepath.Base(zip), Role: app.VueRole, AppCode: app.AppCode, AppName: app.AppName}})
	if err != nil {
		return nil, err
	}
	var regions []frontend.Region
	if plan.mainRoot != "" {
		regions = append(regions, frontend.Region{AppName: app.AppName, Role: app.VueRole, Path: "."})
	}
	for code := range plan.children {
		name := app.AppName
		if declared, ok := plan.declared[code]; ok {
			name = declared.AppName
		}
		regions = append(regions, frontend.Region{AppName: name, Role: "sub", AppCode: code, Path: "apps/" + code})
	}
	if plan.cdnRoot != "" {
		regions = append(regions, frontend.Region{AppName: "web-cdn", Role: "cdn", Path: "apps/cdn"})
	}
	return regions, nil
}

// C contains the old entry/config set plus new immutable resources only. The
// final F already contains the union. Never expose new HTML during C rollout.
func prepareResourceSite(baseline, final, output string) error {
	if err := copyDirContents(baseline, output); err != nil {
		return err
	}
	old, err := frontend.Inventory(baseline)
	if err != nil {
		return err
	}
	next, err := frontend.Inventory(final)
	if err != nil {
		return err
	}
	for file := range next {
		if _, exists := old[file]; exists || (!frontend.IsImmutable(file) && !strings.HasPrefix(file, "apps/cdn/")) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(output, file)), 0755); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(final, file), filepath.Join(output, file)); err != nil {
			return err
		}
	}
	return nil
}
