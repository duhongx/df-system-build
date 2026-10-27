package stages

import (
	"crypto/sha256"
	"df-build-server/internal/frontend"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
)

// Embedded regions require an explicit .df-release.json bundle manifest.
type webUpdatePlan struct {
	mainRoot   string
	children   map[string]string
	cdnRoot    string
	standalone bool
	regions    []frontend.Region
	bindings   map[string]frontend.Region // validated identities, fixed before assembly
	declared   map[string]frontend.Region
	inputs     []model.FrontendArtifact
	log        webLog
}

func frontendArtifacts(pCtx *types.PipelineContext) ([]model.FrontendArtifact, error) {
	if pCtx.Pipeline != nil && pCtx.Pipeline.FrontendArtifactsJSON != "" {
		var artifacts []model.FrontendArtifact
		if err := json.Unmarshal([]byte(pCtx.Pipeline.FrontendArtifactsJSON), &artifacts); err != nil || len(artifacts) == 0 {
			return nil, fmt.Errorf("前端制品清单无效，请重新提交更新")
		}
		return artifacts, nil
	}
	if pCtx.DeployMode == "artifact_image" || pCtx.DeployMode == "manual" || pCtx.DeployMode == "artifact_deploy" {
		return nil, fmt.Errorf("前端任务缺少制品清单，请重新提交更新")
	}
	var appID uint
	if pCtx.Pipeline != nil {
		appID = pCtx.Pipeline.ApplicationID
	}
	return []model.FrontendArtifact{{AppID: appID, FileName: pCtx.ArtifactName, Role: pCtx.VueRole, AppCode: pCtx.AppCode, AppName: pCtx.AppName}}, nil
}

func prepareWebUpdate(sourceDir, workDir string, artifacts []model.FrontendArtifact, logs ...webLog) (*webUpdatePlan, error) {
	plan := &webUpdatePlan{children: map[string]string{}}
	if len(logs) > 0 {
		plan.log = logs[0]
	}
	seen := map[string]bool{}
	for i, artifact := range artifacts {
		if artifact.FileName == "" || filepath.Base(artifact.FileName) != artifact.FileName || strings.ContainsAny(artifact.FileName, "\\\r\n") {
			return nil, fmt.Errorf("无效的前端制品名称")
		}
		if seen[artifact.FileName] {
			return nil, fmt.Errorf("重复的前端制品: %s", artifact.FileName)
		}
		seen[artifact.FileName] = true
		zipPath := filepath.Join(sourceDir, artifact.FileName)
		plan.log.printf("制品 %d/%d：角色=%s；appCode=%s；文件=%s", i+1, len(artifacts), artifact.Role, artifact.AppCode, zipPath)
		info, err := os.Stat(zipPath)
		if err != nil {
			return nil, fmt.Errorf("读取待更新制品 %s 失败: %w", zipPath, err)
		}
		plan.log.printf("待更新制品已就位；路径=%s；大小=%d 字节", zipPath, info.Size())
		inputHash, err := hashWebFile(zipPath)
		if err != nil {
			return nil, err
		}
		if artifact.SHA256 != "" && artifact.SHA256 != inputHash {
			return nil, fmt.Errorf("制品 %s 与提交时的校验和不一致，请重新提交", artifact.FileName)
		}
		artifact.SHA256 = inputHash
		plan.inputs = append(plan.inputs, artifact)
		if artifact.SHA256 != "" {
			plan.log.printf("开始校验制品 SHA256；文件=%s；期望=%s", zipPath, artifact.SHA256)
			hash, err := hashWebFile(zipPath)
			if err != nil {
				return nil, err
			}
			if hash != artifact.SHA256 {
				return nil, fmt.Errorf("制品 %s 与提交时的校验和不一致，请重新提交", artifact.FileName)
			}
		}
		if artifact.SHA256 != "" {
			plan.log.printf("制品 SHA256 校验通过；文件=%s；SHA256=%s", artifact.FileName, artifact.SHA256)
		} else {
			plan.log.printf("制品未提供 SHA256 快照，继续执行解压和入口校验；文件=%s", artifact.FileName)
		}
		root := filepath.Join(workDir, fmt.Sprintf("input-%d", i))
		if err := webFileStep(plan.log, "解压并规范化制品目录", zipPath, root, func() error { return extractFrontendZip(zipPath, root, artifact.Role == "cdn", plan.log) }); err != nil {
			return nil, fmt.Errorf("制品 %s: %w", artifact.FileName, err)
		}
		afterHash, err := hashWebFile(zipPath)
		if err != nil {
			return nil, err
		}
		if afterHash != inputHash {
			return nil, fmt.Errorf("解压期间制品发生变化: %s", artifact.FileName)
		}
		if err := frontend.ValidateBuildManifest(root, artifact.AppName, artifact.Role, artifact.AppCode); err != nil {
			return nil, err
		}
		switch artifact.Role {
		case "main":
			if plan.mainRoot != "" {
				return nil, fmt.Errorf("一次只能更新一个主应用")
			}
			plan.mainRoot = root
		case "sub":
			if !validWebAppCode(artifact.AppCode) {
				return nil, fmt.Errorf("无效的子应用 appCode: %q", artifact.AppCode)
			}
			if plan.children[artifact.AppCode] != "" {
				return nil, fmt.Errorf("重复的子应用: %s", artifact.AppCode)
			}
			plan.children[artifact.AppCode] = root
			if err := frontend.CheckIdentity(root, artifact.AppCode); err != nil {
				return nil, err
			}
		case "cdn":
			if plan.cdnRoot != "" {
				return nil, fmt.Errorf("重复的公共资源制品")
			}
			plan.cdnRoot = root
		case "standalone":
			if len(artifacts) != 1 {
				return nil, fmt.Errorf("独立应用不能合并到共享更新组")
			}
			plan.standalone, plan.mainRoot = true, root
			expected := strings.TrimPrefix(artifact.AppName, "web-")
			if err := frontend.CheckIdentity(root, expected); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("无效的前端制品角色: %s", artifact.Role)
		}
	}
	if plan.mainRoot != "" && !plan.standalone {
		var bundle struct {
			Mode    string            `json:"mode"`
			Regions []frontend.Region `json:"regions"`
		}
		bundleData, bundleErr := os.ReadFile(filepath.Join(plan.mainRoot, ".df-release.json"))
		if bundleErr != nil && !os.IsNotExist(bundleErr) {
			return nil, bundleErr
		}
		if bundleErr == nil {
			if err := json.Unmarshal(bundleData, &bundle); err != nil || bundle.Mode != "bundle" {
				return nil, fmt.Errorf("整包发布清单无效")
			}
		}
		declared := map[string]frontend.Region{}
		for _, region := range bundle.Regions {
			key := region.AppCode
			if region.Role == "main" {
				key = "main"
			}
			if region.Role == "cdn" {
				key = "cdn"
			}
			if declared[key].Role != "" || key == "" || (region.Role != "main" && region.Role != "sub" && region.Role != "cdn") {
				return nil, fmt.Errorf("整包发布清单区域重复或无效")
			}
			declared[key] = region
		}
		plan.declared = map[string]frontend.Region{}
		for key, region := range declared {
			plan.declared[key] = region
		}
		if bundle.Mode == "bundle" && declared["main"].Role != "main" {
			return nil, fmt.Errorf("整包清单必须声明主应用")
		}
		if bundle.Mode == "bundle" && declared["main"].AppName != "web-main" {
			return nil, fmt.Errorf("整包主应用必须明确绑定 web-main")
		}
		entries, err := os.ReadDir(filepath.Join(plan.mainRoot, "apps"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				return nil, fmt.Errorf("apps 下只能包含显式声明的应用或公共资源目录")
			}
			code := entry.Name()
			child := filepath.Join(plan.mainRoot, "apps", code)
			region := declared[code]
			if bundle.Mode != "bundle" || declared["main"].Role != "main" || region.Role == "" {
				return nil, fmt.Errorf("常规主包不得包含 apps；整包更新必须在 .df-release.json 显式声明全部区域（apps/%s）", code)
			}
			if code == "cdn" && region.Role == "cdn" {
				if plan.cdnRoot != "" {
					return nil, fmt.Errorf("公共资源存在重复来源")
				}
				plan.cdnRoot = child
				delete(declared, code)
				continue
			}
			if !validWebAppCode(code) {
				return nil, fmt.Errorf("无效的子应用目录: %s", code)
			}
			if region.Role != "sub" || requireWebIndex(child) != nil {
				return nil, fmt.Errorf("整包子应用 %s 缺少有效入口或角色不符", code)
			}
			if err := frontend.CheckIdentity(child, code); err != nil {
				return nil, err
			}
			if plan.children[code] != "" {
				return nil, fmt.Errorf("子应用 %s 同时存在于主应用 ZIP 和独立 ZIP 中，请只保留一份", code)
			}
			plan.children[code] = child
			delete(declared, code)
			plan.log.printf("识别主应用包内子应用：appCode=%s；来源=%s", code, child)
		}
		delete(declared, "main")
		if len(declared) != 0 {
			return nil, fmt.Errorf("整包清单声明了未提供的区域")
		}
		if err == nil && bundle.Mode != "bundle" {
			return nil, fmt.Errorf("常规主包不得包含 apps 目录，请拆包或使用显式整包清单")
		}
	}
	if plan.mainRoot == "" && len(plan.children) == 0 && plan.cdnRoot == "" {
		return nil, fmt.Errorf("未选择前端制品")
	}
	return plan, nil
}

func validWebAppCode(code string) bool {
	return model.ValidSubAppCode(code)
}

func requireWebIndex(root string) error {
	info, err := os.Lstat(filepath.Join(root, "index.html"))
	if err != nil {
		return fmt.Errorf("%s 缺少 index.html: %w", root, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s/index.html 必须是普通文件", root)
	}
	return nil
}

func webChildren(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "apps"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var codes []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		codes = append(codes, entry.Name())
	}
	return codes, nil
}

// assemble builds into a separate directory. The live baseline and input ZIPs
// are never modified, including when a later copy or validation fails.
func (plan *webUpdatePlan) assemble(baseline, output string, initial bool) (updated, preserved []string, err error) {
	if initial && plan.mainRoot == "" {
		return nil, nil, fmt.Errorf("Deployment 不存在，首次部署必须包含主应用 ZIP")
	}
	if !initial {
		if err = requireWebIndex(baseline); err != nil {
			return
		}
		if err = copyDirContents(baseline, output); err != nil {
			return
		}
	} else if err = os.MkdirAll(output, 0755); err != nil {
		return
	}
	if plan.mainRoot != "" {
		role := "main"
		if plan.standalone {
			role = "standalone"
		}
		if err = webFileStep(plan.log, "写入新主应用", plan.mainRoot, output, func() error { return plan.mergeRegion(plan.mainRoot, output, ".", role, !plan.standalone) }); err != nil {
			return
		}
		label := "主应用"
		if plan.standalone {
			label = "独立应用"
		}
		updated = append(updated, label)
	}
	codes := make([]string, 0, len(plan.children))
	for code := range plan.children {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		regionPath := "apps/" + code
		if err = webFileStep(plan.log, "写入新子应用 "+regionPath, plan.children[code], filepath.Join(output, "apps", code), func() error {
			return plan.mergeRegion(plan.children[code], filepath.Join(output, "apps", code), regionPath, "sub", false)
		}); err != nil {
			return
		}
		updated = append(updated, regionPath)
	}
	if plan.cdnRoot != "" {
		if err = plan.mergeRegion(plan.cdnRoot, filepath.Join(output, "apps", "cdn"), "apps/cdn", "cdn", false); err != nil {
			return
		}
		updated = append(updated, "apps/cdn")
	}
	if !initial && !plan.standalone {
		var beforeCodes []string
		beforeCodes, err = webChildren(baseline)
		if err != nil {
			return
		}
		for _, code := range beforeCodes {
			if plan.children[code] != "" || (code == "cdn" && plan.cdnRoot != "") {
				continue
			}
			before, e := hashWebTree(filepath.Join(baseline, "apps", code))
			if e != nil {
				err = e
				return
			}
			after, e := hashWebTree(filepath.Join(output, "apps", code))
			if e != nil {
				err = e
				return
			}
			if before != after {
				err = fmt.Errorf("未选择区域 apps/%s 内容发生变化", code)
				return
			}
			preserved = append(preserved, "apps/"+code)
		}
	}
	if err = requireWebIndex(output); err != nil {
		return
	}
	for code := range plan.children {
		if err = requireWebIndex(filepath.Join(output, "apps", code)); err != nil {
			return
		}
	}
	logWebTree(plan.log, "组装完成，所选应用已完整替换，未选区域保持不变", output)
	return
}

// Business packages replace their complete region. The main region excludes
// the whole apps tree; sub/standalone regions have no baseline files retained.
// Explicit CDN updates keep their separate, additive version-directory policy.
func (plan *webUpdatePlan) mergeRegion(source, dest, regionPath, role string, excludeApps bool) error {
	before := frontend.Files{}
	if _, err := os.Stat(dest); err == nil {
		var e error
		before, e = frontend.Inventory(dest)
		if e != nil {
			return e
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	current, err := frontend.Inventory(source)
	if err != nil {
		return err
	}
	for file := range current {
		if file == ".df-release.json" || (excludeApps && (file == "apps" || strings.HasPrefix(file, "apps/"))) {
			delete(current, file)
		}
	}
	for file, next := range current {
		if old, ok := before[file]; ok && old.SHA256 != next.SHA256 && file != ".df-build.json" && role == "cdn" {
			return fmt.Errorf("不可变资源同路径内容冲突: %s/%s；请使用新文件名或版本目录", regionPath, file)
		}
	}
	if role != "cdn" {
		if excludeApps {
			entries, err := os.ReadDir(dest)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			for _, entry := range entries {
				if entry.Name() == "apps" {
					continue
				}
				if err := os.RemoveAll(filepath.Join(dest, entry.Name())); err != nil {
					return err
				}
			}
		} else if err := os.RemoveAll(dest); err != nil {
			return err
		}
	}
	for file := range current {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dest, file)), 0755); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(source, file), filepath.Join(dest, file)); err != nil {
			return err
		}
	}
	retained := frontend.Files{}
	for file, old := range before {
		if _, ok := current[file]; !ok && role == "cdn" {
			retained[file] = old
		}
	}
	if excludeApps {
		for file := range before {
			if strings.HasPrefix(file, "apps/") {
				delete(before, file)
			}
		}
	}
	region := frontend.Region{Role: role, Path: regionPath, Before: before, Current: current, Retained: retained}
	if plan.bindings != nil {
		binding, ok := plan.bindings[regionPath]
		if !ok || binding.Role != role || binding.AppID == 0 {
			return fmt.Errorf("区域 %s 缺少已核对的应用登记", regionPath)
		}
		region.AppID, region.AppName, region.AppCode = binding.AppID, binding.AppName, binding.AppCode
	}
	plan.regions = append(plan.regions, region)
	return nil
}

func hashWebFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Hash paths and contents, independent of tar entry order, timestamps and modes.
func hashWebTree(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("前端目录包含非普通文件: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash, err := hashWebFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(relative), hash)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
