package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"df-build-server/internal/deploy/engine/exec"
)

func (e *ActionExecutor) runCommand(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Command == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "命令配置 command",
			Reason:     "命令为空",
			Detail:     "run_command.command 不能为空",
			Suggestion: "补充明确的命令和参数，不要把整段 shell 写成一个字符串。",
		}
	}
	opts := exec.RunOptions{Context: e.executionContext(), WorkDir: spec.WorkDir}
	if err := opts.Context.Err(); err != nil {
		return err
	}
	if spec.Timeout != "" {
		parsedTimeout, err := time.ParseDuration(spec.Timeout)
		if err != nil {
			return timeoutError(ctx, actionName, spec.Timeout, err)
		}
		opts.Timeout = parsedTimeout
	}
	// scope:control pins execution to the control node — used by
	// preflight tasks that probe offline resources sitting on the
	// deploy machine, not on the target host.
	cmd := e.backend.Cmd
	if spec.Scope == "control" {
		cmd = e.controlCmd
	}
	result := cmd.RunWithOptions(opts, spec.Command, spec.Args...)
	if result.Err != nil || result.ExitCode != 0 {
		detail := strings.TrimSpace(string(result.Stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(result.Stdout))
		}
		if detail == "" && result.Err != nil {
			detail = result.Err.Error()
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "命令 " + strings.Join(append([]string{spec.Command}, spec.Args...), " "),
			Reason:     "命令执行失败",
			Detail:     fmt.Sprintf("exit_code=%d output=%s", result.ExitCode, detail),
			Suggestion: "根据命令输出定位失败原因；必要时先单独在目标主机执行该命令确认环境。",
		}
	}
	return nil
}

func (e *ActionExecutor) systemdService(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Service == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "服务配置 service",
			Reason:     "服务名为空",
			Detail:     "systemd_service.service 不能为空",
			Suggestion: "补充明确的 systemd 服务名。",
		}
	}
	if spec.Enabled != nil {
		command := "disable"
		if *spec.Enabled {
			command = "enable"
		}
		if err := e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "systemctl", Args: []string{command, spec.Service}, Timeout: systemdTimeout(spec, command)}, actionName); err != nil {
			return err
		}
	}
	if spec.State == "" {
		return nil
	}
	commandByState := map[string]string{
		"started":   "start",
		"stopped":   "stop",
		"restarted": "restart",
		"reloaded":  "reload",
	}
	command, ok := commandByState[spec.State]
	if !ok {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "服务状态 " + spec.State,
			Reason:     "不支持的服务状态",
			Detail:     "systemd_service.state 仅支持 started/stopped/restarted/reloaded",
			Suggestion: "修改配置里的服务状态。",
		}
	}
	return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "systemctl", Args: []string{command, spec.Service}, Timeout: systemdTimeout(spec, command)}, actionName)
}

func systemdTimeout(spec ActionSpec, command string) string {
	if spec.Timeout != "" {
		return spec.Timeout
	}
	if command == "start" || command == "restart" {
		return "180s"
	}
	return "30s"
}

func (e *ActionExecutor) rpmPackage(ctx TaskContext, spec ActionSpec, actionName string) (string, error) {
	switch spec.State {
	case "installed", "present":
		sourcePath, err := e.resourcePath(spec.Source)
		if err != nil {
			return "", &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "离线 rpm " + spec.Source,
				Reason:     "rpm 路径非法",
				Detail:     err.Error(),
				Suggestion: "rpm_package.source 必须是离线资源目录下的相对路径。",
			}
		}
		// Source rpm lives on the control node; verify with os.*.
		if _, err := os.Stat(sourcePath); err != nil {
			return "", &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "离线 rpm " + spec.Source,
				Reason:     "rpm 文件不存在",
				Detail:     sourcePath + " 不存在",
				Suggestion: "检查离线资源目录是否完整。",
			}
		}
		// Push the rpm to a staging path on the target host, then
		// install it. /tmp on the node is fine for staging since rpm
		// reads the file once and we cleanup afterwards. The legacy
		// path assumed sourcePath was visible on the node via the
		// rsync'd /opt/his-deploy mirror — that mirror is going away.
		stagedPath := "/tmp/dfctl-rpm-" + filepath.Base(sourcePath)
		if err := e.backend.FS.PutFile(sourcePath, stagedPath, 0o644); err != nil {
			return "", &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "暂存 rpm " + stagedPath,
				Reason:     "推送 rpm 到目标节点失败",
				Detail:     err.Error(),
				Suggestion: "检查目标节点 /tmp 写权限和磁盘空间。",
			}
		}
		installErr := e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "rpm", Args: []string{"-Uvh", "--replacepkgs", stagedPath}}, actionName)
		// Best-effort cleanup; ignore errors so we don't mask the
		// install failure (or bother the operator if install
		// succeeded).
		_ = e.backend.FS.Remove(stagedPath)
		return sourcePath, installErr
	case "removed", "absent":
		if spec.Package == "" {
			return "", &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "rpm 包名 package",
				Reason:     "包名为空",
				Detail:     "rpm_package.package 不能为空",
				Suggestion: "清理 rpm 时只填写本组件主程序包名，不要填写系统核心依赖。",
			}
		}
		return spec.Package, e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "rpm", Args: []string{"-e", spec.Package}}, actionName)
	default:
		return "", &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "rpm 状态 " + spec.State,
			Reason:     "不支持的 rpm 状态",
			Detail:     "rpm_package.state 仅支持 installed/present/removed/absent",
			Suggestion: "修改配置里的 rpm state。",
		}
	}
}

func (e *ActionExecutor) yumPackage(ctx TaskContext, spec ActionSpec, actionName string) error {
	if len(spec.Packages) == 0 {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "软件包列表 packages",
			Reason:     "软件包列表为空",
			Detail:     "yum_package.packages 不能为空",
			Suggestion: "补充需要安装的系统依赖包；清理时不要删除系统核心依赖。",
		}
	}
	state := spec.State
	if state == "" {
		state = "installed"
	}
	switch state {
	case "installed", "present":
		args := append([]string{"install", "-y"}, spec.Packages...)
		return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "yum", Args: args}, actionName)
	default:
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "yum 状态 " + state,
			Reason:     "不支持的 yum 状态",
			Detail:     "yum_package 仅用于安装系统依赖，不用于清理系统包",
			Suggestion: "组件主程序包清理请使用 rpm_package，系统依赖不要在 rollback 中删除。",
		}
	}
}

func (e *ActionExecutor) cronLine(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Marker == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "cron marker",
			Reason:     "marker 为空",
			Detail:     "cron_line.marker 不能为空",
			Suggestion: "给每条定时任务配置唯一 marker，便于回滚时只删除本组件任务。",
		}
	}
	state := spec.State
	if state == "" {
		state = "present"
	}
	switch state {
	case "present":
		if spec.Line == "" {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "cron line",
				Reason:     "line 为空",
				Detail:     "cron_line.line 不能为空",
				Suggestion: "补充完整 crontab 行，并在末尾包含 marker 注释。",
			}
		}
		tmp := "/tmp/dfctl-cron-" + shellSafeToken(spec.Marker)
		listCommand, installCommand := cronCommands(spec.User)
		// IMPORTANT: marker matching uses '$' so e.g. marker
		// `cleanup-nexus` does NOT match `# cleanup-nexus-images`,
		// otherwise a redeploy with a longer marker would silently
		// delete the previous component's cron entries. Both line
		// and marker go through shellQuote because they're operator-
		// authored YAML and may legitimately contain single quotes,
		// shell metas, etc. Note: marker is treated as grep regex
		// — keep markers simple ([a-z0-9-]+) so regex metacharacters
		// don't surprise us.
		quotedMarkerEnd := shellQuote("# " + spec.Marker + "$")
		quotedLine := shellQuote(spec.Line)
		script := fmt.Sprintf("(%s -l 2>/dev/null || true) | grep -v %s > %s || true; printf '%%s\\n' %s >> %s && %s %s && rm -f %s",
			listCommand, quotedMarkerEnd, tmp, quotedLine, tmp, installCommand, tmp, tmp)
		return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "bash", Args: []string{"-lc", script}}, actionName)
	case "absent":
		listCommand, installCommand := cronCommands(spec.User)
		quotedMarkerEnd := shellQuote("# " + spec.Marker + "$")
		script := fmt.Sprintf("%s -l 2>/dev/null | grep -v %s | %s - 2>/dev/null || true",
			listCommand, quotedMarkerEnd, installCommand)
		return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "bash", Args: []string{"-lc", script}}, actionName)
	default:
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "cron 状态 " + state,
			Reason:     "不支持的 cron 状态",
			Detail:     "cron_line.state 仅支持 present/absent",
			Suggestion: "修改 cron_line.state。",
		}
	}
}

func cronCommands(user string) (string, string) {
	if user == "" || user == "root" {
		return "crontab", "crontab"
	}
	command := "crontab -u " + shellQuote(user)
	return command, command
}

func (e *ActionExecutor) sysctlSet(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Key == "" || spec.Value == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "sysctl key/value",
			Reason:     "key/value 为空",
			Detail:     "sysctl_set.key 和 sysctl_set.value 不能为空",
			Suggestion: "补充内核参数名和值。",
		}
	}
	// Backup file lives in the centralised control-side stateDir
	// (per design: state never goes to nodes).
	backupPath, err := e.sysctlBackupPath(ctx, spec.Key)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		// Read the runtime value from the *target node*.
		result := e.backend.Cmd.Run("sysctl", "-n", spec.Key)
		if result.ExitCode != 0 || result.Err != nil {
			return commandFailureFromBackend(ctx, actionName, "sysctl -n "+spec.Key, result)
		}
		if err := os.WriteFile(backupPath, []byte(strings.TrimSpace(string(result.Stdout))+"\n"), 0o644); err != nil {
			return err
		}
	}
	// Apply runtime + persist to /etc/sysctl.conf on the node.
	if err := e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "sysctl", Args: []string{"-w", spec.Key + "=" + spec.Value}}, actionName); err != nil {
		return err
	}
	configFile := sysctlConfigFile(spec)
	content, _ := e.backend.FS.ReadFile(configFile)
	next := removeSysctlLine(string(content), spec.Key)
	next = strings.TrimRight(next, "\n")
	if next != "" {
		next += "\n"
	}
	next += spec.Key + " = " + spec.Value + "\n"
	return e.backend.FS.WriteFile(configFile, []byte(next), 0o644)
}

func (e *ActionExecutor) sysctlRestore(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Key == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "sysctl key",
			Reason:     "key 为空",
			Detail:     "sysctl_restore.key 不能为空",
			Suggestion: "补充要恢复的内核参数名。",
		}
	}
	backupPath, err := e.sysctlBackupPath(ctx, spec.Key)
	if err != nil {
		return stateDirError(ctx, actionName, err)
	}
	backup, err := os.ReadFile(backupPath)
	if err == nil {
		value := strings.TrimSpace(string(backup))
		if value != "" {
			if err := e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "sysctl", Args: []string{"-w", spec.Key + "=" + value}}, actionName); err != nil {
				return err
			}
		}
		_ = os.Remove(backupPath)
	}
	configFile := sysctlConfigFile(spec)
	content, _ := e.backend.FS.ReadFile(configFile)
	next := removeSysctlLine(string(content), spec.Key)
	return e.backend.FS.WriteFile(configFile, []byte(next), 0o644)
}

// sysctlBackupPath returns a control-side path under stateDir keyed
// by host. State is intentionally centralised on the control side so
// rollback works without depending on per-node state files.
func (e *ActionExecutor) sysctlBackupPath(ctx TaskContext, key string) (string, error) {
	if e.stateDir == "" {
		return "", fmt.Errorf("state_dir 不能为空")
	}
	host := ctx.HostAddr
	if host == "" {
		host = ctx.HostName
	}
	if host == "" {
		host = "_"
	}
	return filepath.Join(e.stateDir, "runtime", host, strings.ReplaceAll(key, "/", "_")+".runtime"), nil
}

// commandFailureFromBackend wraps a Result from the new exec backend
// into a DeployError. Bridges to the legacy commandFailure helper
// while we migrate.
func commandFailureFromBackend(ctx TaskContext, actionName string, command string, result exec.Result) *DeployError {
	detail := strings.TrimSpace(string(result.Stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.Stdout))
	}
	if detail == "" && result.Err != nil {
		detail = result.Err.Error()
	}
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "命令 " + command,
		Reason:     "命令执行失败",
		Detail:     fmt.Sprintf("exit_code=%d output=%s", result.ExitCode, detail),
		Suggestion: "根据命令输出定位失败原因；必要时先单独在目标主机执行该命令确认环境。",
	}
}

func sysctlConfigFile(spec ActionSpec) string {
	if spec.ConfigFile != "" {
		return spec.ConfigFile
	}
	return "/etc/sysctl.conf"
}

func removeSysctlLine(content string, key string) string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			continue
		}
		if line == "" && len(lines) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func (e *ActionExecutor) systemGroup(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Group == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "用户组 group",
			Reason:     "用户组为空",
			Detail:     "system_group.group 不能为空",
			Suggestion: "补充当前组件要管理的用户组名。",
		}
	}
	switch spec.State {
	case "present", "created", "installed", "":
		// Idempotent: getent succeeds (rc=0) if the group already
		// exists, in which case we skip groupadd entirely. Without
		// this, redeploys fail with `groupadd: group 'X' already
		// exists` (rc=9).
		script := fmt.Sprintf("getent group %s >/dev/null || groupadd -r %s", shellQuote(spec.Group), shellQuote(spec.Group))
		return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "bash", Args: []string{"-lc", script}}, actionName)
	case "absent", "removed":
		return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "groupdel", Args: []string{spec.Group}}, actionName)
	default:
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "用户组状态 " + spec.State,
			Reason:     "不支持的用户组状态",
			Detail:     "system_group.state 仅支持 present/absent",
			Suggestion: "修改配置里的用户组状态。",
		}
	}
}

func (e *ActionExecutor) systemUser(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.User == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "用户 user",
			Reason:     "用户名为空",
			Detail:     "system_user.user 不能为空",
			Suggestion: "补充当前组件要管理的用户名。",
		}
	}
	switch spec.State {
	case "present", "created", "installed", "":
		args := []string{"-r"}
		if spec.Group != "" {
			args = append(args, "-g", spec.Group)
		}
		if spec.Home != "" {
			args = append(args, "-d", spec.Home)
		}
		if spec.Shell != "" {
			args = append(args, "-s", spec.Shell)
		}
		if spec.CreateHome != nil {
			if *spec.CreateHome {
				args = append(args, "-m")
			} else {
				args = append(args, "-M")
			}
		}
		args = append(args, spec.User)
		// Idempotent: id succeeds if the user already exists. Same
		// reasoning as systemGroup above.
		quotedArgs := make([]string, 0, len(args))
		for _, a := range args {
			quotedArgs = append(quotedArgs, shellQuote(a))
		}
		script := fmt.Sprintf("id %s >/dev/null 2>&1 || useradd %s",
			shellQuote(spec.User), strings.Join(quotedArgs, " "))
		return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "bash", Args: []string{"-lc", script}}, actionName)
	case "absent", "removed":
		return e.runCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "userdel", Args: []string{spec.User}}, actionName)
	default:
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "用户状态 " + spec.State,
			Reason:     "不支持的用户状态",
			Detail:     "system_user.state 仅支持 present/absent",
			Suggestion: "修改配置里的用户状态。",
		}
	}
}
