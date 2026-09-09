package stages

import (
	"crypto/sha256"
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

// webUpdatePlan contains only verified, explicitly supplied regions. A main ZIP
// can contain complete apps/{code} regions; incomplete embedded regions do not
// replace an existing child.
type webUpdatePlan struct {
	mainRoot string
	children map[string]string
	log      webLog
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
	return []model.FrontendArtifact{{FileName: pCtx.ArtifactName, Role: pCtx.VueRole, AppCode: pCtx.AppCode}}, nil
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
		if err := webFileStep(plan.log, "解压并规范化制品目录", zipPath, root, func() error { return extractWebZip(nil, zipPath, root, plan.log) }); err != nil {
			return nil, fmt.Errorf("制品 %s: %w", artifact.FileName, err)
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
		default:
			return nil, fmt.Errorf("无效的前端制品角色: %s", artifact.Role)
		}
	}
	if plan.mainRoot != "" {
		entries, err := os.ReadDir(filepath.Join(plan.mainRoot, "apps"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			code := entry.Name()
			child := filepath.Join(plan.mainRoot, "apps", code)
			if requireWebIndex(child) != nil {
				plan.log.printf("主应用包内 apps/%s 缺少有效入口，不作为完整子应用替换来源", code)
				continue
			}
			if !validWebAppCode(code) {
				return nil, fmt.Errorf("无效的子应用目录: %s", code)
			}
			if plan.children[code] != "" {
				return nil, fmt.Errorf("子应用 %s 同时存在于主应用 ZIP 和独立 ZIP 中，请只保留一份", code)
			}
			plan.children[code] = child
			plan.log.printf("识别主应用包内子应用：appCode=%s；来源=%s", code, child)
		}
	}
	if plan.mainRoot == "" && len(plan.children) == 0 {
		return nil, fmt.Errorf("未选择前端制品")
	}
	return plan, nil
}

func validWebAppCode(code string) bool {
	return code != "" && code != "." && code != ".." && filepath.Base(code) == code && !strings.ContainsAny(code, "/\\\r\n")
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
		if err := requireWebIndex(filepath.Join(root, "apps", entry.Name())); err != nil {
			return nil, err
		}
		codes = append(codes, entry.Name())
	}
	return codes, nil
}

// assemble builds into a separate directory. The live baseline and input ZIPs
// are never modified, including when a later copy or validation fails.
func (plan *webUpdatePlan) assemble(baseline, output string, initial bool) (updated, preserved []string, err error) {
	plan.log.printf("开始组装前端；首次部署=%t；本地基线=%s；本地输出=%s；所有清理仅作用于本地构建目录", initial, baseline, output)
	if initial && plan.mainRoot == "" {
		return nil, nil, fmt.Errorf("Deployment 不存在，首次部署必须包含主应用 ZIP")
	}
	if !initial {
		if err = requireWebIndex(baseline); err != nil {
			return
		}
		if _, err = webChildren(baseline); err != nil {
			return
		}
	}
	if plan.mainRoot != "" {
		err = webFileStep(plan.log, "写入新主应用（使用新目录替换主应用范围）", plan.mainRoot, output, func() error { return copyDirContents(plan.mainRoot, output) })
		updated = append(updated, "主应用")
	} else {
		err = webFileStep(plan.log, "复制完整基线（保留当前主应用）", baseline, output, func() error { return copyDirContents(baseline, output) })
	}
	if err != nil {
		return
	}
	if !initial {
		// Preserve the entire apps tree, including children absent from the current
		// application catalog. Only explicit uploaded children may replace it.
		baseApps := filepath.Join(baseline, "apps")
		if _, statErr := os.Stat(baseApps); statErr == nil {
			if err = webFileStep(plan.log, "清理本地 apps 目录以恢复线上子应用", "本地构建目录", filepath.Join(output, "apps"), func() error { return os.RemoveAll(filepath.Join(output, "apps")) }); err != nil {
				return
			}
			if err = webFileStep(plan.log, "复制基线全部子应用", baseApps, filepath.Join(output, "apps"), func() error { return copyDirContents(baseApps, filepath.Join(output, "apps")) }); err != nil {
				return
			}
		} else if !os.IsNotExist(statErr) {
			err = statErr
			return
		}
		var codes []string
		codes, err = webChildren(baseline)
		if err != nil {
			return
		}
		for _, code := range codes {
			if plan.children[code] == "" {
				preserved = append(preserved, "apps/"+code)
			}
		}
	}
	codes := make([]string, 0, len(plan.children))
	for code := range plan.children {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		dest := filepath.Join(output, "apps", code)
		if err = webFileStep(plan.log, "清空待更新子应用 apps/"+code, "本地构建目录", dest, func() error { return os.RemoveAll(dest) }); err != nil {
			return
		}
		if err = webFileStep(plan.log, "写入新子应用 apps/"+code, plan.children[code], dest, func() error { return copyDirContents(plan.children[code], dest) }); err != nil {
			return
		}
		updated = append(updated, "apps/"+code)
	}
	if err = requireWebIndex(output); err != nil {
		return
	}
	_, err = webChildren(output)
	if err == nil {
		logWebTree(plan.log, "组装完成，主应用和全部子应用入口校验通过", output)
	}
	return
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
