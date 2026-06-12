package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func shellSafeToken(value string) string {
	replacer := strings.NewReplacer("/", "-", " ", "-", "#", "", "'", "", "\"", "")
	return replacer.Replace(value)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (e *ActionExecutor) resourcePath(source string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("source 不能为空")
	}
	if filepath.IsAbs(source) {
		return "", fmt.Errorf("source 必须是相对离线资源目录的路径: %s", source)
	}
	clean := filepath.Clean(source)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("source 不能跳出离线资源目录: %s", source)
	}
	return filepath.Join(e.resourceDir, clean), nil
}

func copyRegularFile(source string, target string) error {
	same, err := regularFileContentEqual(source, target)
	if err != nil {
		return err
	}
	if same {
		return nil
	}

	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := out.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

func regularFileContentEqual(source string, target string) (bool, error) {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return false, err
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !sourceInfo.Mode().IsRegular() || !targetInfo.Mode().IsRegular() {
		return false, nil
	}
	if sourceInfo.Size() != targetInfo.Size() {
		return false, nil
	}

	sourceFile, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer sourceFile.Close()
	targetFile, err := os.Open(target)
	if err != nil {
		return false, err
	}
	defer targetFile.Close()

	sourceBuf := make([]byte, 32*1024)
	targetBuf := make([]byte, 32*1024)
	for {
		sourceN, sourceErr := sourceFile.Read(sourceBuf)
		targetN, targetErr := targetFile.Read(targetBuf)
		if sourceN != targetN || !bytes.Equal(sourceBuf[:sourceN], targetBuf[:targetN]) {
			return false, nil
		}
		if sourceErr == io.EOF && targetErr == io.EOF {
			return true, nil
		}
		if sourceErr != nil && sourceErr != io.EOF {
			return false, sourceErr
		}
		if targetErr != nil && targetErr != io.EOF {
			return false, targetErr
		}
	}
}

func copyDirectory(source string, target string, fileMode os.FileMode) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		outPath := filepath.Join(target, relative)
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(outPath, info.Mode().Perm())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		if err := copyRegularFile(path, outPath); err != nil {
			return err
		}
		return os.Chmod(outPath, fileMode)
	})
}

func writeReaderToFile(reader io.Reader, target string, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, reader); err != nil {
		return err
	}
	return out.Close()
}

func safeExtractPath(target string, entryName string) (string, error) {
	clean := filepath.Clean(entryName)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("非法路径: %s", entryName)
	}
	outPath := filepath.Join(target, clean)
	cleanTarget := filepath.Clean(target)
	cleanOut := filepath.Clean(outPath)
	if cleanOut != cleanTarget && !strings.HasPrefix(cleanOut, cleanTarget+string(filepath.Separator)) {
		return "", fmt.Errorf("路径跳出目标目录: %s", entryName)
	}
	return outPath, nil
}

func archiveError(ctx TaskContext, actionName string, position string, reason string, err error) *DeployError {
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   position,
		Reason:     reason,
		Detail:     err.Error(),
		Suggestion: "检查离线包是否完整、格式是否正确、目标目录权限和磁盘空间是否正常。",
	}
}

func parseActionTimeout(value string) (time.Duration, error) {
	if value == "" {
		return 5 * time.Second, nil
	}
	return time.ParseDuration(value)
}

func parseActionInterval(value string) (time.Duration, error) {
	if value == "" {
		return time.Second, nil
	}
	return time.ParseDuration(value)
}

func timeoutError(ctx TaskContext, actionName string, value string, err error) *DeployError {
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "超时时间 " + value,
		Reason:     "超时时间格式错误",
		Detail:     err.Error(),
		Suggestion: "使用类似 1s、30s、2m 的 Go duration 格式。",
	}
}

func (e *ActionExecutor) backupPaths(target string) (string, string, error) {
	if e.stateDir == "" {
		return "", "", fmt.Errorf("state_dir 不能为空")
	}
	clean := filepath.Clean(target)
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	if clean == "." || clean == "" {
		return "", "", fmt.Errorf("target 路径非法: %s", target)
	}
	backupPath := filepath.Join(e.stateDir, "backups", clean)
	return backupPath, backupPath + ".absent", nil
}

func (e *ActionExecutor) pathStatePath(target string) (string, error) {
	if e.stateDir == "" {
		return "", fmt.Errorf("state_dir 不能为空")
	}
	clean := filepath.Clean(target)
	clean = strings.TrimPrefix(clean, string(filepath.Separator))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("target 路径非法: %s", target)
	}
	return filepath.Join(e.stateDir, "paths", clean+".state"), nil
}

func pathRequiredError(ctx TaskContext, actionName string, field string) *DeployError {
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "目标路径",
		Reason:     "目标路径为空",
		Detail:     field + " 不能为空",
		Suggestion: "补充目标路径后重新执行。",
	}
}

func fileModeError(ctx TaskContext, actionName string, mode string) *DeployError {
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "文件权限 " + mode,
		Reason:     "文件权限格式错误",
		Detail:     "无法解析八进制权限: " + mode,
		Suggestion: "使用类似 0644 或 0755 的八进制权限。",
	}
}

func stateDirError(ctx TaskContext, actionName string, err error) *DeployError {
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "状态目录 state_dir",
		Reason:     "状态目录配置错误",
		Detail:     err.Error(),
		Suggestion: "配置 cluster.state_dir 或执行参数里的 state_dir，用于记录部署前状态。",
	}
}

func parseFileMode(value string) (os.FileMode, error) {
	if value == "" {
		return 0o644, nil
	}
	value = strings.TrimPrefix(value, "0")
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, err
	}
	return os.FileMode(parsed), nil
}
