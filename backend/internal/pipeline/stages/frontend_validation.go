package stages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	codes := make([]string, 0, len(plan.children))
	for code := range plan.children {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	var unconfirmed []string
	for _, code := range codes {
		app := byCode[code]
		if app.ID == 0 || !app.Enabled {
			return fmt.Errorf("整包子应用 %s 未登记或已停用", code)
		}
		if declared, ok := plan.declared[code]; ok && declared.AppName != app.AppName {
			return fmt.Errorf("整包区域 %s 的应用名称与登记不一致", code)
		}
		confirmed, err := confirmFrontendDirectory(pCtx, baselineDir, app)
		if err != nil {
			return err
		}
		if !confirmed {
			unconfirmed = append(unconfirmed, fmt.Sprintf("apps/%s（%s）", code, app.AppName))
		}
	}
	if len(unconfirmed) > 0 {
		return fmt.Errorf("现有目录尚未确认发布归属: %s；请在应用管理核对对应应用并勾选“接管已核对的线上同编号目录”后重新提交；尚未组装或构建镜像", strings.Join(unconfirmed, "、"))
	}
	// Assembly creates the content inventories later. Fix the exact identities
	// now, then have mergeRegion attach them to those inventories by region path.
	var regions []frontend.Region
	if plan.mainRoot != "" {
		role := "main"
		if plan.standalone {
			role = "standalone"
		}
		regions = append(regions, frontend.Region{Path: ".", Role: role})
	}
	for _, code := range codes {
		regions = append(regions, frontend.Region{Path: "apps/" + code, Role: "sub", AppCode: code})
	}
	if plan.cdnRoot != "" {
		regions = append(regions, frontend.Region{Path: "apps/cdn", Role: "cdn"})
	}
	plan.bindings = make(map[string]frontend.Region, len(regions))
	for _, region := range regions {
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
		if app.ID == 0 || !app.Enabled || app.VueRole != region.Role {
			return fmt.Errorf("区域 %s 未登记或已停用", region.Path)
		}
		region.AppName = name
		region.AppID = app.ID
		plan.bindings[region.Path] = region
	}
	data, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	pCtx.Pipeline.FrontendArtifactsJSON = string(data)
	return nil
}

// Missing publication history is not evidence of a different owner. Seeded
// legacy applications carry an explicit adoption marker; a matching build
// manifest can also identify a previously published directory. Unknown custom
// directories still require adoption rather than being claimed by their name.
func confirmFrontendDirectory(pCtx *types.PipelineContext, baselineDir string, app model.Application) (bool, error) {
	root := filepath.Join(baselineDir, "apps", app.AppCode)
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("apps/%s 不是普通目录，停止更新", app.AppCode)
	}
	if err := frontend.CheckIdentity(root, app.AppCode); err != nil {
		return false, fmt.Errorf("线上目录 apps/%s 身份校验失败: %w", app.AppCode, err)
	}
	identified := false
	if _, err := os.Lstat(filepath.Join(root, ".df-build.json")); err == nil {
		if err := frontend.ValidateBuildManifest(root, app.AppName, "sub", app.AppCode); err != nil {
			return false, fmt.Errorf("线上目录 apps/%s 构建清单校验失败: %w", app.AppCode, err)
		}
		identified = true
	} else if !os.IsNotExist(err) {
		return false, err
	}
	// A legacy build does not contain .df-build.json or a platform publication
	// record. Its original app name/code/repository binding AND the live package's
	// explicit xiTongId must agree. A missing xiTongId is not positive evidence.
	// Do not change the persisted adoption preference when recognizing ownership.
	if !identified && repository.IsLegacyFrontendRegistration(app) {
		data, err := os.ReadFile(filepath.Join(root, "config.json"))
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		var config struct {
			AppCode string `json:"xiTongId"`
		}
		identified = err == nil && json.Unmarshal(data, &config) == nil && config.AppCode == app.AppCode
	}
	if app.AdoptExistingDirectory || identified {
		webLogger(pCtx).printf("已核对线上子目录 apps/%s；应用=%s；登记接管=%t；目录身份已识别=%t", app.AppCode, app.AppName, app.AdoptExistingDirectory, identified)
		return true, nil
	}
	var published int64
	if err := repository.DB.Model(&model.Pipeline{}).Where("application_id = ? AND k8s_namespace = ? AND deployment_progress = ?", app.ID, pCtx.K8sNamespace, "completed").Count(&published).Error; err != nil {
		return false, err
	}
	if published == 0 {
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).
			Where("app_id = ? AND namespace = ? AND deployment_name = ? AND app_code = ? AND deploy_status = ?", app.ID, pCtx.K8sNamespace, "web-main", app.AppCode, "success").Count(&published).Error; err != nil {
			return false, err
		}
	}
	return published > 0, nil
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
// final F contains only the new package in replaced business regions. Never
// expose new HTML during C rollout; baseline resources exist only in C there.
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
		// A file/directory shape change cannot coexist with the old site in C.
		// Leave that path unchanged until the complete replacement in F.
		path, conflict := output, false
		parts := strings.Split(filepath.ToSlash(file), "/")
		for i, part := range parts {
			path = filepath.Join(path, part)
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			if !info.IsDir() || i == len(parts)-1 {
				conflict = true
				break
			}
		}
		if conflict {
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
