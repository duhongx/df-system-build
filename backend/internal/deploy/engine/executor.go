package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"df-build-server/internal/deploy/engine/exec"
)

type ActionExecutorOptions struct {
	ResourceDir   string
	StateDir      string
	CommandRunner CommandRunner
	// Backend is the per-target-host execution backend used by the
	// new "control-side single binary" path. When nil, the executor
	// falls back to a LocalBackend so existing call sites that don't
	// supply one keep working unchanged.
	Backend *exec.Backend
	// ControlCloudhisRoot is the absolute path on the control node
	// where render-phase actions deposit intermediate artefacts +
	// generated PKI / kubeconfig files (typically <remote_root>/cloudhis).
	// Targets that fall under this prefix bypass the per-host backend
	// and use the control side's own filesystem — without it,
	// multi-host deploys would scatter render output across nodes
	// and downstream copy_path actions would fail to find their source.
	//
	// Historically two roots existed: <remote_root>/generated for
	// host-specific intermediates and <resource_dir>/cloudhis for
	// PKI / kubeconfig. Both moved under <remote_root>/cloudhis to
	// align with the ansible-era layout and to keep resource_dir
	// truly read-only.
	ControlCloudhisRoot string
}

type ActionExecutor struct {
	resourceDir         string
	stateDir            string
	commandRunner       CommandRunner
	backend             exec.Backend
	runCtx              context.Context
	controlCloudhisRoot string
	// controlFS is the control-side filesystem (always LocalBackend).
	// Used for render-phase intermediates that must not travel to a
	// remote host.
	controlFS exec.Filesystem
	// controlCmd runs commands on the control node (where dfctl-web
	// lives), regardless of what backend points at. Used for
	// kubectl, cfssl, and other tools that — by design — execute on
	// the control side and dial out to clusters/nodes themselves.
	controlCmd exec.Commander
}

func NewActionExecutor(opts ActionExecutorOptions) *ActionExecutor {
	runner := opts.CommandRunner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	backend := exec.Backend{}
	switch {
	case opts.Backend != nil:
		// Caller provided a fully-formed backend; trust it.
		backend = *opts.Backend
	case opts.CommandRunner != nil:
		// Bridge mode: legacy tests inject a CommandRunner mock to
		// observe what commands an Action issued. Adapt it to the
		// new Commander surface so those tests keep working without
		// changes. FS half stays on the local disk because mocks
		// don't supply one, which matches the old behaviour.
		backend = exec.Backend{
			FS:  exec.NewLocalBackend().FS,
			Cmd: commandRunnerAsCommander{runner: opts.CommandRunner},
		}
	default:
		backend = exec.NewLocalBackend()
	}
	// controlCmd ALWAYS points at the control node. With a real
	// RemoteBackend wired in, backend.Cmd would dispatch over SSH to
	// the target host, but actions tagged scope:control (kubectl,
	// cfssl, preflight resource probes) must execute on the deploy
	// machine itself. The bridge mode exception below preserves
	// legacy CommandRunner-injected tests where a single mock is
	// expected to observe every command regardless of scope.
	var controlCmd exec.Commander
	switch {
	case opts.CommandRunner != nil:
		controlCmd = backend.Cmd // shared mock — see comment above
	default:
		controlCmd = exec.NewLocalBackend().Cmd
	}
	// controlFS, on the other hand, ALWAYS points at the local disk.
	// Render-phase actions write generated/* artefacts here so a
	// subsequent copy_path can read them on its way to the node.
	controlFS := exec.NewLocalBackend().FS
	return &ActionExecutor{
		resourceDir:         opts.ResourceDir,
		stateDir:            opts.StateDir,
		commandRunner:       runner,
		backend:             backend,
		controlCmd:          controlCmd,
		controlFS:           controlFS,
		controlCloudhisRoot: opts.ControlCloudhisRoot,
	}
}

// fsForTarget chooses between the control-side FS and the per-target
// backend FS based on where target points. Anything under the
// resource_dir or the configured control-side cloudhis root is treated
// as a control-side intermediate (render-phase output) and stays
// local; everything else is a node-side path and routes through the
// backend (which may be local or SSH+SFTP).
func (e *ActionExecutor) fsForTarget(target string) exec.Filesystem {
	if e.isControlSidePath(target) {
		return e.controlFS
	}
	return e.backend.FS
}

func (e *ActionExecutor) isControlSidePath(target string) bool {
	if target == "" {
		return false
	}
	if e.resourceDir != "" && hasPathPrefix(target, e.resourceDir) {
		return true
	}
	if e.controlCloudhisRoot != "" && hasPathPrefix(target, e.controlCloudhisRoot) {
		return true
	}
	return false
}

// hasPathPrefix is filepath.HasPrefix without the deprecation
// warning — it checks whether path is rooted at prefix without
// false-positives like "/optfoo".
func hasPathPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.HasPrefix(path, prefix)
}

// commandRunnerAsCommander adapts a legacy CommandRunner into an
// exec.Commander. Detects the optional TimeoutCommandRunner /
// TimeoutDirCommandRunner / RunInDir capabilities at runtime and
// dispatches to the most specific one — same logic the old
// ActionExecutor.runCommand had inline, now centralised here so the
// per-Action code is single-path.
type commandRunnerAsCommander struct {
	runner CommandRunner
}

func (c commandRunnerAsCommander) Run(name string, args ...string) exec.Result {
	return c.RunWithOptions(exec.RunOptions{}, name, args...)
}

func (c commandRunnerAsCommander) RunWithStdin(stdin []byte, name string, args ...string) exec.Result {
	return c.RunWithOptions(exec.RunOptions{Stdin: stdin}, name, args...)
}

func (c commandRunnerAsCommander) RunWithOptions(opts exec.RunOptions, name string, args ...string) exec.Result {
	var res CommandResult
	switch {
	case opts.WorkDir != "" && opts.Timeout > 0 && opts.Context != nil:
		if r, ok := c.runner.(ContextTimeoutCommandRunner); ok {
			res = r.RunWithContextTimeout(opts.Context, opts.Timeout, name, args)
		} else if r, ok := c.runner.(TimeoutDirCommandRunner); ok {
			res = r.RunInDirWithTimeout(opts.Timeout, opts.WorkDir, name, args)
		} else if r, ok := c.runner.(TimeoutCommandRunner); ok {
			res = r.RunWithTimeout(opts.Timeout, name, args)
		} else {
			res = c.runner.Run(name, args)
		}
	case opts.WorkDir != "" && opts.Timeout > 0:
		if r, ok := c.runner.(TimeoutDirCommandRunner); ok {
			res = r.RunInDirWithTimeout(opts.Timeout, opts.WorkDir, name, args)
		} else if r, ok := c.runner.(TimeoutCommandRunner); ok {
			res = r.RunWithTimeout(opts.Timeout, name, args)
		} else {
			res = c.runner.Run(name, args)
		}
	case opts.WorkDir != "":
		if r, ok := c.runner.(interface {
			RunInDir(string, string, []string) CommandResult
		}); ok {
			res = r.RunInDir(opts.WorkDir, name, args)
		} else {
			res = c.runner.Run(name, args)
		}
	case opts.Timeout > 0 && opts.Context != nil:
		if r, ok := c.runner.(ContextTimeoutCommandRunner); ok {
			res = r.RunWithContextTimeout(opts.Context, opts.Timeout, name, args)
		} else if r, ok := c.runner.(TimeoutCommandRunner); ok {
			res = r.RunWithTimeout(opts.Timeout, name, args)
		} else {
			res = c.runner.Run(name, args)
		}
	case opts.Timeout > 0:
		if r, ok := c.runner.(TimeoutCommandRunner); ok {
			res = r.RunWithTimeout(opts.Timeout, name, args)
		} else {
			res = c.runner.Run(name, args)
		}
	case opts.Context != nil:
		if r, ok := c.runner.(ContextCommandRunner); ok {
			res = r.RunContext(opts.Context, name, args)
		} else {
			res = c.runner.Run(name, args)
		}
	default:
		res = c.runner.Run(name, args)
	}
	return exec.Result{
		Stdout:   []byte(res.Stdout),
		Stderr:   []byte(res.Stderr),
		ExitCode: res.ExitCode,
		Err:      res.Err,
	}
}

func (e *ActionExecutor) Execute(ctx TaskContext, spec ActionSpec) (ActionResult, error) {
	return e.ExecuteContext(context.Background(), ctx, spec)
}

func (e *ActionExecutor) ExecuteContext(runCtx context.Context, ctx TaskContext, spec ActionSpec) (ActionResult, error) {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return ActionResult{}, err
	}
	execWithCtx := *e
	execWithCtx.runCtx = runCtx
	return execWithCtx.execute(ctx, spec)
}

func (e *ActionExecutor) execute(ctx TaskContext, spec ActionSpec) (ActionResult, error) {
	start := time.Now()
	spec = replaceVariablesInAction(spec, map[string]string{
		"component":    ctx.Component,
		"host.name":    ctx.HostName,
		"host.address": ctx.HostAddr,
		"task.id":      ctx.TaskID,
		"task.name":    ctx.TaskName,
	})
	actionName := actionDisplayName(spec)
	if spec.OnlyHostAddress != "" && ctx.HostAddr != spec.OnlyHostAddress {
		return ActionResult{
			Context:  ctx,
			Action:   actionName,
			Target:   actionTarget(spec),
			Status:   "条件不匹配跳过",
			Duration: time.Since(start),
		}, nil
	}
	if spec.OnlyWhen != "" {
		// Simple left=right equality after variable expansion.
		// "single=single" → run; "single=ha" → skip; missing "=" or
		// either side empty → skip (defensive: a typo'd condition
		// shouldn't accidentally fire the action).
		idx := strings.Index(spec.OnlyWhen, "=")
		if idx < 0 || strings.TrimSpace(spec.OnlyWhen[:idx]) != strings.TrimSpace(spec.OnlyWhen[idx+1:]) {
			return ActionResult{
				Context:  ctx,
				Action:   actionName,
				Target:   actionTarget(spec),
				Status:   "条件不匹配跳过",
				Duration: time.Since(start),
			}, nil
		}
	}
	if spec.Creates != "" {
		// `creates` is the idempotency check: skip the Action if the
		// marker file already exists. Route the Stat through
		// fsForTarget so a control-side marker (e.g. a generated cert
		// under resource_dir) resolves on the control node and a
		// node-side one resolves over SSH.
		if _, err := e.fsForTarget(spec.Creates).Stat(spec.Creates); err == nil {
			return ActionResult{
				Context:  ctx,
				Action:   actionName,
				Target:   spec.Creates,
				Status:   "已存在跳过",
				Duration: time.Since(start),
			}, nil
		} else if !os.IsNotExist(err) {
			return ActionResult{}, &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "creates 路径 " + spec.Creates,
				Reason:     "检查跳过条件失败",
				Detail:     err.Error(),
				Suggestion: "检查 creates 路径权限和文件系统状态。",
			}
		}
	}
	result, err := e.executeStrict(ctx, spec, actionName, start)
	if err != nil && spec.IgnoreError {
		return ignoredResult(ctx, actionName, actionTarget(spec), start), nil
	}
	return result, err
}

func (e *ActionExecutor) executionContext() context.Context {
	if e.runCtx != nil {
		return e.runCtx
	}
	return context.Background()
}

func (e *ActionExecutor) executeStrict(ctx TaskContext, spec ActionSpec, actionName string, start time.Time) (ActionResult, error) {
	switch spec.Type {
	case "copy_file":
		if err := e.copyFile(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "copy_dir":
		if err := e.copyDir(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "copy_path":
		if err := e.copyPath(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "fetch_file":
		if err := e.fetchFile(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "ensure_dir":
		if err := e.ensureDir(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "write_file":
		if err := e.writeFile(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "remove_path":
		if err := e.removePath(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "backup_file":
		if err := e.backupFile(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "record_path_state":
		if err := e.recordPathState(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "remove_path_if_created":
		if err := e.removePathIfCreated(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "remove_path_if_created_or_untracked":
		if err := e.removePathIfCreatedOrUntracked(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "remove_path_if_untracked":
		if err := e.removePathIfUntracked(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "assert_path_absent_if_created":
		if err := e.assertPathAbsentIfCreated(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "restore_file":
		if err := e.restoreFile(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "run_command":
		if err := e.runCommand(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Command, start), nil
	case "systemd_service":
		if err := e.systemdService(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Service, start), nil
	case "rpm_package":
		target, err := e.rpmPackage(ctx, spec, actionName)
		if err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, target, start), nil
	case "extract_archive":
		if err := e.extractArchive(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "render_template":
		if err := e.renderTemplate(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "http_check":
		if err := e.httpCheck(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.URL, start), nil
	case "tcp_check":
		if err := e.tcpCheck(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Address, start), nil
	case "system_group":
		if err := e.systemGroup(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Group, start), nil
	case "system_user":
		if err := e.systemUser(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.User, start), nil
	case "resource_preflight":
		if err := e.resourcePreflight(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Manifest, start), nil
	case "kubernetes_artifacts":
		if err := e.kubernetesArtifacts(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "kubernetes_artifacts_check":
		if err := e.kubernetesArtifactsCheck(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "slb_config":
		if err := e.slbConfig(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "chmod":
		if err := e.chmod(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "chown":
		if err := e.chown(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "symlink":
		if err := e.symlink(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Target, start), nil
	case "kubectl_apply":
		if err := e.kubectl(ctx, spec, actionName, "apply"); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.File, start), nil
	case "kubectl_delete":
		if err := e.kubectl(ctx, spec, actionName, "delete"); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.File, start), nil
	case "yum_package":
		if err := e.yumPackage(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, strings.Join(spec.Packages, ","), start), nil
	case "cron_line":
		if err := e.cronLine(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Marker, start), nil
	case "sysctl_set":
		if err := e.sysctlSet(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Key, start), nil
	case "sysctl_restore":
		if err := e.sysctlRestore(ctx, spec, actionName); err != nil {
			return ActionResult{}, err
		}
		return successResult(ctx, actionName, spec.Key, start), nil
	default:
		return ActionResult{}, &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "任务动作类型",
			Reason:     "不支持的动作类型",
			Detail:     fmt.Sprintf("action type %q 当前未实现", spec.Type),
			Suggestion: "确认配置文件里的 action.type 是否正确，或先在 dfctl v2 中实现该动作。",
		}
	}
}

func successResult(ctx TaskContext, actionName string, target string, start time.Time) ActionResult {
	return ActionResult{
		Context:  ctx,
		Action:   actionName,
		Target:   target,
		Status:   "成功",
		Duration: time.Since(start),
	}
}

func ignoredResult(ctx TaskContext, actionName string, target string, start time.Time) ActionResult {
	return ActionResult{
		Context:  ctx,
		Action:   actionName,
		Target:   target,
		Status:   "已忽略失败",
		Duration: time.Since(start),
	}
}

func actionTarget(spec ActionSpec) string {
	switch spec.Type {
	case "run_command":
		return strings.Join(append([]string{spec.Command}, spec.Args...), " ")
	case "systemd_service":
		return spec.Service
	case "rpm_package":
		if spec.Package != "" {
			return spec.Package
		}
		return spec.Source
	case "yum_package":
		return strings.Join(spec.Packages, ",")
	case "http_check":
		return spec.URL
	case "tcp_check":
		return spec.Address
	case "kubectl_apply", "kubectl_delete":
		return spec.File
	case "resource_preflight":
		return spec.Manifest
	case "kubernetes_artifacts":
		return spec.Target
	case "kubernetes_artifacts_check":
		return spec.Target
	case "slb_config":
		return spec.Target
	case "cron_line":
		return spec.Marker
	case "sysctl_set", "sysctl_restore":
		return spec.Key
	case "system_user":
		return spec.User
	case "system_group":
		return spec.Group
	default:
		return spec.Target
	}
}

func actionDisplayName(spec ActionSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	switch spec.Type {
	case "copy_file":
		return "复制文件"
	case "copy_path":
		return "复制生成文件"
	case "fetch_file":
		return "拉取节点文件到控制端"
	case "ensure_dir":
		return "创建目录"
	case "write_file":
		return "写入文件"
	case "remove_path":
		return "删除路径"
	case "backup_file":
		return "备份文件"
	case "record_path_state":
		return "记录路径状态"
	case "remove_path_if_created":
		return "删除新增路径"
	case "assert_path_absent_if_created":
		return "确认新增路径已删除"
	case "restore_file":
		return "恢复文件"
	case "run_command":
		return "执行命令"
	case "systemd_service":
		return "管理服务"
	case "rpm_package":
		return "管理主程序包"
	case "extract_archive":
		return "解压文件"
	case "render_template":
		return "渲染模板"
	case "http_check":
		return "HTTP检查"
	case "tcp_check":
		return "TCP检查"
	case "system_group":
		return "管理用户组"
	case "system_user":
		return "管理用户"
	case "resource_preflight":
		return "检查离线资源"
	case "kubernetes_artifacts":
		return "生成Kubernetes证书和kubeconfig"
	case "kubernetes_artifacts_check":
		return "校验K8s节点快照"
	case "slb_config":
		return "生成SLB配置"
	case "chmod":
		return "设置权限"
	case "chown":
		return "设置属主"
	case "symlink":
		return "创建软链"
	case "kubectl_apply":
		return "应用K8s资源"
	case "kubectl_delete":
		return "删除K8s资源"
	case "yum_package":
		return "安装系统依赖"
	case "cron_line":
		return "管理定时任务"
	case "sysctl_set":
		return "设置内核参数"
	case "sysctl_restore":
		return "恢复内核参数"
	default:
		return spec.Type
	}
}
