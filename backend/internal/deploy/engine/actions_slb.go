package engine

import (
	"path/filepath"
	"strings"
)

// slbConfig writes haproxy.cfg into spec.Target. The legacy version of
// this action also generated keepalived.conf for VIP failover, but
// we've removed that path: the project now ships a single SLB host
// running haproxy alone (no keepalived, no VIP) — see commit history
// for context. As a result spec.Address/Line/Value are no longer read.
//
// spec.Target is the control-side staging directory (typically
// ${remote_root}/generated/current/slb). Routing through fsForTarget
// keeps the file on the control node so a downstream copy_path can
// pick it up and ship it to the SLB host.
func (e *ActionExecutor) slbConfig(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "slb_config.target")
	}
	fs := e.fsForTarget(spec.Target)
	if err := fs.MkdirAll(spec.Target, 0o755); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "SLB配置目录 " + spec.Target,
			Reason:     "创建配置目录失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘空间。",
		}
	}
	haproxy := spec.Content
	if strings.TrimSpace(haproxy) == "" {
		haproxy = "global\n        daemon\n\ndefaults\n        mode tcp\n"
	}
	if err := fs.WriteFile(filepath.Join(spec.Target, "haproxy.cfg"), []byte(haproxy), 0o644); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "haproxy.cfg",
			Reason:     "写入haproxy配置失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘空间。",
		}
	}
	return nil
}
