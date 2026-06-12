package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (e *ActionExecutor) backupFile(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "backup_file.target")
	}
	backupPath, absentPath, err := e.backupPaths(spec.Target)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	// Backup files live in the centralised control-side stateDir.
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "备份目录 " + filepath.Dir(backupPath),
			Reason:     "创建备份目录失败",
			Detail:     err.Error(),
			Suggestion: "检查 state_dir 权限和磁盘状态。",
		}
	}
	// Inspect the target on the node — IsNotExist there means we
	// record an "absent" marker so rollback knows to delete the file
	// on its way back rather than restore.
	info, err := e.backend.FS.Stat(spec.Target)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(absentPath, []byte("absent\n"), 0o644)
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "读取待备份文件失败",
			Detail:     err.Error(),
			Suggestion: "检查目标文件权限和文件系统状态。",
		}
	}
	if !info.Mode().IsRegular() {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "只支持备份普通文件",
			Detail:     spec.Target + " 不是普通文件",
			Suggestion: "目录清理请使用 remove_path，复杂路径恢复需要单独建模。",
		}
	}
	// Cap backup_file at 100 MiB. Pipelines should only back up
	// configuration files (typically <100 KB); anything bigger is
	// usually a misconfigured target (e.g. log file or data dir
	// pointed at backup_file by mistake) and reading it whole into
	// memory would OOM the dfctl-web process.
	const maxBackupBytes = 100 * 1024 * 1024
	if info.Size() > maxBackupBytes {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "待备份文件过大",
			Detail:     fmt.Sprintf("%s 大小为 %d 字节，超过 backup_file 上限 %d 字节 (100 MiB)", spec.Target, info.Size(), maxBackupBytes),
			Suggestion: "backup_file 仅用于备份配置文件；如需备份大文件请使用专门工具。",
		}
	}
	// Read from the node, write to the control-side backup.
	body, err := e.backend.FS.ReadFile(spec.Target)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "读取目标文件失败",
			Detail:     err.Error(),
			Suggestion: "检查目标文件权限和远端连接。",
		}
	}
	if err := os.WriteFile(backupPath, body, info.Mode().Perm()); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "备份文件 " + backupPath,
			Reason:     "备份文件失败",
			Detail:     err.Error(),
			Suggestion: "检查 state_dir 权限和磁盘空间。",
		}
	}
	return os.Chmod(backupPath, info.Mode().Perm())
}

func (e *ActionExecutor) recordPathState(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "record_path_state.target")
	}
	statePath, err := e.pathStatePath(spec.Target)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "状态目录 " + filepath.Dir(statePath),
			Reason:     "创建状态目录失败",
			Detail:     err.Error(),
			Suggestion: "检查 state_dir 权限和磁盘状态。",
		}
	}
	state := "present\n"
	// Probe path on the node, not the control side.
	if _, err := e.backend.FS.Stat(spec.Target); os.IsNotExist(err) {
		state = "absent\n"
	}
	return os.WriteFile(statePath, []byte(state), 0o644)
}

func (e *ActionExecutor) removePathIfCreated(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "remove_path_if_created.target")
	}
	statePath, err := e.pathStatePath(spec.Target)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	// State file lives on the control side. The marker is
	// transactional: deploy writes it, rollback consumes it. If a
	// previous run left a stale marker behind, record_path_state
	// would miss the clean-baseline detection and pin every future
	// rollback to "skip" — leaving residue on the node forever.
	defer removeControlSideFile(statePath)
	content, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "状态文件 " + statePath,
			Reason:     "读取路径状态失败",
			Detail:     err.Error(),
			Suggestion: "检查 state_dir 是否完整。",
		}
	}
	if strings.TrimSpace(string(content)) != "absent" {
		return nil
	}
	return e.removePath(ctx, spec, actionName)
}

func (e *ActionExecutor) removePathIfCreatedOrUntracked(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "remove_path_if_created_or_untracked.target")
	}
	statePath, err := e.pathStatePath(spec.Target)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	// Same transactional contract as remove_path_if_created.
	defer removeControlSideFile(statePath)
	content, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return e.removePath(ctx, spec, actionName)
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "状态文件 " + statePath,
			Reason:     "读取路径状态失败",
			Detail:     err.Error(),
			Suggestion: "检查 state_dir 是否完整。",
		}
	}
	if strings.TrimSpace(string(content)) != "absent" {
		return nil
	}
	return e.removePath(ctx, spec, actionName)
}

func (e *ActionExecutor) removePathIfUntracked(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "remove_path_if_untracked.target")
	}
	backupPath, absentPath, err := e.backupPaths(spec.Target)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	// Both markers live on the control side. Whichever branch we
	// take below, the markers are no longer useful after this
	// rollback step — clear them so the next deploy starts from a
	// clean baseline. Without this, a leftover backup from a half-
	// failed rollback would mask future "untracked" detection and
	// stale binaries would never be removed.
	defer removeControlSideFile(backupPath)
	defer removeControlSideFile(absentPath)
	if _, err := os.Stat(backupPath); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return stateDirError(ctx, actionName, err)
	}
	if _, err := os.Stat(absentPath); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return stateDirError(ctx, actionName, err)
	}
	return e.removePath(ctx, spec, actionName)
}

func (e *ActionExecutor) assertPathAbsentIfCreated(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "assert_path_absent_if_created.target")
	}
	statePath, err := e.pathStatePath(spec.Target)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	// Residue check is the last consumer of the marker. Whether we
	// pass or fail, the marker has done its job — clear it so the
	// next deploy/rollback cycle starts fresh.
	defer removeControlSideFile(statePath)
	content, err := os.ReadFile(statePath)
	if err != nil && !os.IsNotExist(err) {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "状态文件 " + statePath,
			Reason:     "读取路径状态失败",
			Detail:     err.Error(),
			Suggestion: "检查 state_dir 是否完整。",
		}
	}
	if err == nil && strings.TrimSpace(string(content)) == "present" {
		return nil
	}
	if _, statErr := e.backend.FS.Stat(spec.Target); os.IsNotExist(statErr) {
		return nil
	} else if statErr != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标路径 " + spec.Target,
			Reason:     "检查路径状态失败",
			Detail:     statErr.Error(),
			Suggestion: "检查目标路径权限和远端连接。",
		}
	}
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "目标路径 " + spec.Target,
		Reason:     "部署新增路径仍存在",
		Detail:     spec.Target + " 部署前不存在，回滚后仍然存在",
		Suggestion: "检查回滚日志，确认该路径是否被其他进程重新创建；必要时手动清理后重试 residue。",
	}
}

func (e *ActionExecutor) restoreFile(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "restore_file.target")
	}
	backupPath, absentPath, err := e.backupPaths(spec.Target)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	// Markers are transactional: backup_file writes them at deploy
	// time, restore_file consumes them at rollback time. Once we've
	// either deleted the on-node file (absent branch) or pushed the
	// backup back (backup branch), the markers should not stick
	// around to confuse the next deploy/rollback cycle.
	defer removeControlSideFile(backupPath)
	defer removeControlSideFile(absentPath)
	// "absent" marker on control side → file was created by deploy
	// → remove it on the node.
	if _, err := os.Stat(absentPath); err == nil {
		if err := e.backend.FS.Remove(spec.Target); err != nil && !os.IsNotExist(err) {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "目标文件 " + spec.Target,
				Reason:     "删除部署新增文件失败",
				Detail:     err.Error(),
				Suggestion: "检查目标文件权限，确认该文件属于当前组件。",
			}
		}
		return nil
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "备份文件 " + backupPath,
			Reason:     "读取备份文件失败",
			Detail:     err.Error(),
			Suggestion: "检查 state_dir 是否完整。",
		}
	}
	// Push the control-side backup back onto the node — PutFile
	// handles parent dir creation, atomic rename, and mode in one
	// call.
	if err := e.backend.FS.PutFile(backupPath, spec.Target, info.Mode().Perm()); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "恢复文件失败",
			Detail:     err.Error(),
			Suggestion: "检查目标路径权限和磁盘空间。",
		}
	}
	return nil
}

// removeControlSideFile drops a marker file on the control side. It is
// only called from rollback/residue actions where the marker has just
// finished serving its purpose. We swallow errors deliberately:
//   - IsNotExist is fine, the marker may already have been cleared.
//   - any other error means the on-node operation already succeeded;
//     surfacing a marker-cleanup error here would mask the real result
//     and abort otherwise-clean rollbacks. The leftover marker would
//     only mislead the *next* deploy, not break the current one, so we
//     log via the action layer if needed and move on.
func removeControlSideFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}
