package engine

import (
	"os"
	"path/filepath"
)

func (e *ActionExecutor) copyFile(ctx TaskContext, spec ActionSpec, actionName string) error {
	sourcePath, err := e.resourcePath(spec.Source)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线资源 " + spec.Source,
			Reason:     "源文件路径非法",
			Detail:     err.Error(),
			Suggestion: "检查配置里的 source，不能使用空路径或跳出离线资源目录。",
		}
	}
	// Source lives in the control-side offline-resource directory and
	// is always read via os.* — never through the backend.
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "离线资源 " + spec.Source,
				Reason:     "源文件不存在",
				Detail:     sourcePath + " 不存在",
				Suggestion: "检查离线资源目录是否完整，或重新同步 " + ctx.Component + " 组件资源。",
			}
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线资源 " + spec.Source,
			Reason:     "读取源文件失败",
			Detail:     err.Error(),
			Suggestion: "检查源文件权限和磁盘状态。",
		}
	}
	if spec.Target == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件",
			Reason:     "目标路径为空",
			Detail:     "copy_file.target 不能为空",
			Suggestion: "补充目标文件路径后重新执行。",
		}
	}
	mode, err := parseFileMode(spec.Mode)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "文件权限 " + spec.Mode,
			Reason:     "文件权限格式错误",
			Detail:     err.Error(),
			Suggestion: "使用类似 0644 或 0755 的八进制权限。",
		}
	}
	// Backend.FS.PutFile handles parent-dir creation, atomic rename,
	// and mode application in one shot — same semantics as the old
	// MkdirAll + copyRegularFile + Chmod sequence, but routable to
	// SFTP when the backend is a remote one.
	if err := e.backend.FS.PutFile(sourcePath, spec.Target, mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "复制文件失败",
			Detail:     err.Error(),
			Suggestion: "检查目标路径权限和磁盘空间。",
		}
	}
	return nil
}

func (e *ActionExecutor) copyDir(ctx TaskContext, spec ActionSpec, actionName string) error {
	// Two source modes, mirroring copy_file vs copy_path:
	//   - relative source  → offline-resource dir (resource_dir/...)
	//   - absolute source  → control-side render artefact, typically
	//     ${remote_root}/cloudhis/<comp>/... (PKI dirs etc.)
	// Both read from the control side via os.*; PutDir ships the tree
	// to the (possibly remote) backend.
	var sourcePath string
	if filepath.IsAbs(spec.Source) {
		sourcePath = spec.Source
	} else {
		resolved, err := e.resourcePath(spec.Source)
		if err != nil {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "离线资源目录 " + spec.Source,
				Reason:     "源目录路径非法",
				Detail:     err.Error(),
				Suggestion: "检查配置里的 source，不能使用空路径或跳出离线资源目录。",
			}
		}
		sourcePath = resolved
	}
	// Source dir lives in control-side offline resources — read via os.*.
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "离线资源目录 " + spec.Source,
				Reason:     "源目录不存在",
				Detail:     sourcePath + " 不存在",
				Suggestion: "检查离线资源目录是否完整，或重新同步 " + ctx.Component + " 组件资源。",
			}
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线资源目录 " + spec.Source,
			Reason:     "读取源目录失败",
			Detail:     err.Error(),
			Suggestion: "检查源目录权限和磁盘状态。",
		}
	}
	if !info.IsDir() {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线资源目录 " + spec.Source,
			Reason:     "源路径不是目录",
			Detail:     sourcePath + " 不是目录",
			Suggestion: "复制普通文件请使用 copy_file。",
		}
	}
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "copy_dir.target")
	}
	mode, err := parseFileMode(spec.Mode)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "文件权限 " + spec.Mode,
			Reason:     "文件权限格式错误",
			Detail:     err.Error(),
			Suggestion: "使用类似 0644 或 0755 的八进制权限。",
		}
	}
	// Pass spec.Mode through PutDir as the per-file override; an
	// empty spec.Mode parses to 0o644 — the legacy default — so the
	// behaviour matches the original copyDirectory implementation.
	if err := e.backend.FS.PutDir(sourcePath, spec.Target, mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标目录 " + spec.Target,
			Reason:     "复制目录失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘空间。",
		}
	}
	return nil
}

func (e *ActionExecutor) copyPath(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Source == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "源文件 source",
			Reason:     "源文件路径为空",
			Detail:     "copy_path.source 不能为空",
			Suggestion: "补充由 render 阶段生成的源文件路径。",
		}
	}
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "copy_path.target")
	}
	// Source is a render-stage artefact in <runDir>/... on the
	// control side — read with os.*.
	info, err := os.Stat(spec.Source)
	if err != nil {
		if os.IsNotExist(err) {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "源文件 " + spec.Source,
				Reason:     "源文件不存在",
				Detail:     spec.Source + " 不存在",
				Suggestion: "检查 render 阶段是否已生成该文件。",
			}
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "源文件 " + spec.Source,
			Reason:     "读取源文件失败",
			Detail:     err.Error(),
			Suggestion: "检查源文件权限和磁盘状态。",
		}
	}
	if !info.Mode().IsRegular() {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "源文件 " + spec.Source,
			Reason:     "只支持复制普通文件",
			Detail:     spec.Source + " 不是普通文件",
			Suggestion: "目录请使用 ensure_dir 或组件专用动作建模。",
		}
	}
	mode, err := parseFileMode(spec.Mode)
	if err != nil {
		return fileModeError(ctx, actionName, spec.Mode)
	}
	if err := e.backend.FS.PutFile(spec.Source, spec.Target, mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "复制文件失败",
			Detail:     err.Error(),
			Suggestion: "检查目标路径权限和磁盘空间。",
		}
	}
	return nil
}

func (e *ActionExecutor) ensureDir(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "ensure_dir.target")
	}
	mode, err := parseFileMode(spec.Mode)
	if err != nil {
		return fileModeError(ctx, actionName, spec.Mode)
	}
	fs := e.fsForTarget(spec.Target)
	if err := fs.MkdirAll(spec.Target, mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标目录 " + spec.Target,
			Reason:     "创建目录失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录上级路径权限和磁盘状态。",
		}
	}
	if err := fs.Chmod(spec.Target, mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标目录权限 " + spec.Target,
			Reason:     "设置目录权限失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和文件系统状态。",
		}
	}
	return nil
}

func (e *ActionExecutor) writeFile(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "write_file.target")
	}
	mode, err := parseFileMode(spec.Mode)
	if err != nil {
		return fileModeError(ctx, actionName, spec.Mode)
	}
	fs := e.fsForTarget(spec.Target)
	if err := fs.MkdirAll(filepath.Dir(spec.Target), 0o755); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标目录 " + filepath.Dir(spec.Target),
			Reason:     "创建目标目录失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘状态。",
		}
	}
	if err := fs.WriteFile(spec.Target, []byte(spec.Content), mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标文件 " + spec.Target,
			Reason:     "写入文件失败",
			Detail:     err.Error(),
			Suggestion: "检查目标路径权限和磁盘空间。",
		}
	}
	return nil
}

func (e *ActionExecutor) removePath(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "remove_path.target")
	}
	if err := e.backend.FS.RemoveAll(spec.Target); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标路径 " + spec.Target,
			Reason:     "删除路径失败",
			Detail:     err.Error(),
			Suggestion: "检查路径权限，确认该路径属于当前组件的清理范围。",
		}
	}
	return nil
}

func (e *ActionExecutor) chmod(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "chmod.target")
	}
	mode, err := parseFileMode(spec.Mode)
	if err != nil {
		return fileModeError(ctx, actionName, spec.Mode)
	}
	if err := e.backend.FS.Chmod(spec.Target, mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标路径 " + spec.Target,
			Reason:     "设置权限失败",
			Detail:     err.Error(),
			Suggestion: "检查目标路径是否存在以及权限是否允许修改。",
		}
	}
	return nil
}

func (e *ActionExecutor) chown(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "chown.target")
	}
	if spec.Owner == "" && spec.Group == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "属主属组",
			Reason:     "owner/group 为空",
			Detail:     "chown.owner 和 chown.group 至少填写一个",
			Suggestion: "补充 owner 或 group，例如 owner=root group=root。",
		}
	}
	ownerGroup := spec.Owner
	if spec.Group != "" {
		ownerGroup += ":" + spec.Group
	}
	return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "chown", Args: []string{ownerGroup, spec.Target}}, actionName)
}

func (e *ActionExecutor) symlink(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Source == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "软链源路径",
			Reason:     "source 为空",
			Detail:     "symlink.source 不能为空",
			Suggestion: "补充软链指向的源路径。",
		}
	}
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "symlink.target")
	}
	if err := e.backend.FS.MkdirAll(filepath.Dir(spec.Target), 0o755); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "软链目录 " + filepath.Dir(spec.Target),
			Reason:     "创建软链目录失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘状态。",
		}
	}
	if err := e.backend.FS.Remove(spec.Target); err != nil && !os.IsNotExist(err) {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "软链目标 " + spec.Target,
			Reason:     "清理已有目标失败",
			Detail:     err.Error(),
			Suggestion: "确认该路径属于当前组件，不要覆盖无关文件。",
		}
	}
	if err := e.backend.FS.Symlink(spec.Source, spec.Target); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "软链目标 " + spec.Target,
			Reason:     "创建软链失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和源路径是否合理。",
		}
	}
	return nil
}

// fetchFile is the inverse of copy_file: pull a regular file from the
// node back to a control-side path. dfctl's normal flow is control →
// node (push), but cluster components like elasticsearch generate
// secrets on one node (e.g. elastic-certificates.p12 via
// elasticsearch-certutil) that need to be redistributed to peers.
// fetch_file gives the seed-node task a way to deposit the artefact
// in cloudhis/<comp>/ so a follow-up copy_file action can push it to
// the rest of the cluster.
//
// spec.Source is the node-side path; spec.Target is the control-side
// destination (any absolute path, but conventionally
// ${cluster.resource_dir}/cloudhis/<comp>/...). spec.Mode applies to
// the control-side file. We deliberately do NOT chown — control-side
// permissions are dfctl-web's concern, and the file gets re-pushed
// to nodes with their own ownership downstream.
func (e *ActionExecutor) fetchFile(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Source == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "节点源文件",
			Reason:     "源路径为空",
			Detail:     "fetch_file.source 不能为空",
			Suggestion: "补充节点上要拉回的文件路径。",
		}
	}
	if spec.Target == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "控制端目标",
			Reason:     "目标路径为空",
			Detail:     "fetch_file.target 不能为空",
			Suggestion: "补充控制端目标路径,通常落在 ${cluster.resource_dir}/cloudhis/<comp>/ 下。",
		}
	}
	mode, err := parseFileMode(spec.Mode)
	if err != nil {
		return fileModeError(ctx, actionName, spec.Mode)
	}
	// Read from the node (SFTP for remote, local fs for deploy host).
	info, err := e.backend.FS.Stat(spec.Source)
	if err != nil {
		if os.IsNotExist(err) {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "节点源文件 " + spec.Source,
				Reason:     "节点上源文件不存在",
				Detail:     spec.Source + " 在节点 " + ctx.HostAddr + " 上不存在",
				Suggestion: "检查上一步是否成功生成该文件,或确认 only_host_address 限定的节点确实跑了生成动作。",
			}
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "节点源文件 " + spec.Source,
			Reason:     "读取节点源文件失败",
			Detail:     err.Error(),
			Suggestion: "检查节点上源文件权限和远端连接。",
		}
	}
	if !info.Mode().IsRegular() {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "节点源文件 " + spec.Source,
			Reason:     "只支持拉取普通文件",
			Detail:     spec.Source + " 不是普通文件",
			Suggestion: "fetch_file 当前只支持单个文件,目录请逐个文件 fetch。",
		}
	}
	body, err := e.backend.FS.ReadFile(spec.Source)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "节点源文件 " + spec.Source,
			Reason:     "读取节点源文件失败",
			Detail:     err.Error(),
			Suggestion: "检查节点上源文件权限和远端连接。",
		}
	}
	// Write to the control side via os.* — fetch_file is by definition
	// "land on the control host", so we never route through fsForTarget.
	if err := os.MkdirAll(filepath.Dir(spec.Target), 0o755); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "控制端目录 " + filepath.Dir(spec.Target),
			Reason:     "创建控制端目录失败",
			Detail:     err.Error(),
			Suggestion: "检查控制端目标路径权限和磁盘空间。",
		}
	}
	if err := os.WriteFile(spec.Target, body, mode); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "控制端目标文件 " + spec.Target,
			Reason:     "写入控制端文件失败",
			Detail:     err.Error(),
			Suggestion: "检查控制端目标路径权限和磁盘空间。",
		}
	}
	return os.Chmod(spec.Target, mode)
}
