package engine

import (
	"os"
	"strings"
	"testing"
)

// requiredActionTypes is the action library contract from Requirement 6.1.
// The deployment engine MUST dispatch every one of these types.
var requiredActionTypes = []string{
	"copy_file", "copy_path", "copy_dir", "write_file", "render_template",
	"extract_archive", "symlink", "ensure_dir", "chmod", "chown",
	"yum_package", "rpm_package", "systemd_service", "system_user", "system_group",
	"sysctl_set", "sysctl_restore", "cron_line",
	"kubectl_apply", "kubectl_delete", "kubernetes_artifacts", "kubernetes_artifacts_check",
	"http_check", "tcp_check", "resource_preflight",
	"record_path_state", "backup_file", "restore_file", "remove_path",
	"remove_path_if_created", "remove_path_if_untracked", "remove_path_if_created_or_untracked",
	"assert_path_absent_if_created", "slb_config", "run_command", "fetch_file",
}

// TestActionLibraryCompleteness verifies every required action type is handled
// by the executor dispatcher. This guards against a case being dropped during
// future refactors (Requirement 6.1). It is a source-level check so it has no
// side effects (executing real actions would shell out to systemctl/yum/etc).
func TestActionLibraryCompleteness(t *testing.T) {
	src, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatalf("read executor.go: %v", err)
	}
	body := string(src)
	for _, typ := range requiredActionTypes {
		if !strings.Contains(body, `"`+typ+`"`) {
			t.Errorf("action type %q is not dispatched by the executor", typ)
		}
	}
}

// TestUnknownActionRejected verifies an unrecognised action type is rejected
// with the "不支持的动作类型" DeployError rather than silently succeeding.
func TestUnknownActionRejected(t *testing.T) {
	exec := NewActionExecutor(ActionExecutorOptions{
		ResourceDir: t.TempDir(),
		StateDir:    t.TempDir(),
	})
	_, err := exec.Execute(TaskContext{Component: "test"}, ActionSpec{Type: "__definitely_unknown__", Name: "x"})
	if err == nil {
		t.Fatal("expected error for unknown action type")
	}
	var de *DeployError
	if !AsDeployError(err, &de) {
		t.Fatalf("expected *DeployError, got %T", err)
	}
	if !strings.Contains(de.Reason, "不支持") {
		t.Fatalf("expected unsupported-type reason, got %q", de.Reason)
	}
}
