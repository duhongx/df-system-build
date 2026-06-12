package handler

import (
	"fmt"
	"strings"

	"df-build-server/internal/deploy/defaults"
	"df-build-server/internal/deploy/engine"
	"df-build-server/internal/deploy/engine/virtualcomponents"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

// registerComponentExtras mounts the component defaults/tasks endpoints
// (parity with his-deploy's /components/defaults and /components/{name}/tasks).
func (h *Handler) registerComponentExtras(g *gin.RouterGroup) {
	g.GET("/components/defaults", h.ComponentDefaults)
	g.GET("/components/:name/tasks", h.ComponentTasks)
}

// ComponentDefaults returns the factory default parameters per component so the
// UI can offer a "restore defaults" action.
func (h *Handler) ComponentDefaults(c *gin.Context) {
	all, err := defaults.All()
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, all)
}

type taskActionDTO struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type taskRowDTO struct {
	Component string          `json:"component"`
	Phase     string          `json:"phase"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Actions   []taskActionDTO `json:"actions"`
}

// ComponentTasks returns the planned pipeline tasks (and their actions, with a
// human-readable command summary) for a component or virtual component.
func (h *Handler) ComponentTasks(c *gin.Context) {
	name := c.Param("name")
	cfg, err := engine.LoadDefaultPipelineConfig()
	if err != nil {
		response.Fail(c, 5001, "加载 pipeline 失败: "+err.Error())
		return
	}

	var pipelineNames []string
	if vc, ok := virtualcomponents.Find(name); ok {
		for _, m := range vc.Mappings {
			pipelineNames = append(pipelineNames, m.Name)
		}
	} else if _, ok := cfg.Components[name]; ok {
		pipelineNames = []string{name}
	} else {
		response.Fail(c, 4040, "组件不存在: "+name)
		return
	}

	rows := []taskRowDTO{}
	for _, pname := range pipelineNames {
		comp, ok := cfg.Components[pname]
		if !ok {
			continue
		}
		for _, t := range comp.Tasks {
			actions := make([]taskActionDTO, 0, len(t.Actions))
			for _, a := range t.Actions {
				actions = append(actions, taskActionDTO{
					Type:    a.Type,
					Name:    a.Name,
					Summary: summariseAction(a),
				})
			}
			rows = append(rows, taskRowDTO{
				Component: pname,
				Phase:     t.Phase,
				ID:        t.ID,
				Name:      t.Name,
				Actions:   actions,
			})
		}
	}
	response.OK(c, gin.H{"name": name, "pipelineComponents": pipelineNames, "items": rows})
}

// summariseAction renders an ActionSpec as a single human-readable line
// modelled on the equivalent shell command (ported from his-deploy).
func summariseAction(a engine.ActionSpec) string {
	switch a.Type {
	case "run_command":
		parts := []string{}
		if a.Command != "" {
			parts = append(parts, a.Command)
		}
		parts = append(parts, a.Args...)
		return joinNonEmpty(parts, " ")
	case "copy_file", "copy_path":
		if a.Source != "" {
			return fmt.Sprintf("cp %s %s", a.Source, a.Target)
		}
		return fmt.Sprintf("write %s", a.Target)
	case "fetch_file":
		return fmt.Sprintf("fetch %s (node) → %s (control)", a.Source, a.Target)
	case "render_template":
		return fmt.Sprintf("render %s → %s", a.Source, a.Target)
	case "write_file":
		return fmt.Sprintf("write %s (mode=%s)", a.Target, defaultStr(a.Mode, "0644"))
	case "ensure_dir":
		return fmt.Sprintf("mkdir -p %s (mode=%s)", a.Target, defaultStr(a.Mode, "0755"))
	case "remove_path", "remove_path_if_created", "remove_path_if_created_or_untracked", "remove_path_if_untracked":
		return fmt.Sprintf("rm -rf %s", a.Target)
	case "assert_path_absent_if_created":
		return fmt.Sprintf("assert removed if created %s", a.Target)
	case "backup_file":
		return fmt.Sprintf("backup %s", a.Target)
	case "restore_file":
		return fmt.Sprintf("restore %s", a.Target)
	case "yum_package":
		state := defaultStr(a.State, "present")
		pkgs := append([]string{}, a.Packages...)
		if a.Package != "" {
			pkgs = append([]string{a.Package}, pkgs...)
		}
		return fmt.Sprintf("yum %s %s", state, strings.Join(pkgs, " "))
	case "systemd_service":
		return fmt.Sprintf("systemctl %s %s", defaultStr(a.State, "started"), a.Service)
	case "system_user":
		return fmt.Sprintf("user %s %s (group=%s, shell=%s)",
			defaultStr(a.State, "present"), a.User, a.Group, defaultStr(a.Shell, "/bin/bash"))
	case "chown":
		return fmt.Sprintf("chown %s:%s %s", a.Owner, a.Group, a.Target)
	case "chmod":
		return fmt.Sprintf("chmod %s %s", a.Mode, a.Target)
	case "http_check":
		return fmt.Sprintf("curl -fsS %s (expect %d)", a.URL, a.ExpectedStatus)
	case "cron_line":
		return fmt.Sprintf("cron %s %q (marker=%s)", defaultStr(a.State, "present"), a.Line, a.Marker)
	case "record_path_state":
		return fmt.Sprintf("snapshot %s", a.Target)
	case "resource_preflight":
		return fmt.Sprintf("verify manifest %s", a.Manifest)
	case "kubernetes_artifacts":
		return fmt.Sprintf("generate K8s PKI → %s", a.Target)
	case "kubernetes_artifacts_check":
		return fmt.Sprintf("verify K8s host snapshot ← %s", a.Target)
	case "slb_config":
		return fmt.Sprintf("write haproxy.cfg → %s", a.Target)
	case "extract_archive":
		return fmt.Sprintf("tar xf %s → %s", a.Source, a.Target)
	case "tcp_check":
		return fmt.Sprintf("tcp-check %s", a.Address)
	case "rpm_package":
		state := defaultStr(a.State, "installed")
		if a.Package != "" {
			return fmt.Sprintf("rpm %s %s", state, a.Package)
		}
		return fmt.Sprintf("rpm %s %s", state, a.Source)
	case "system_group":
		return fmt.Sprintf("group %s %s", defaultStr(a.State, "present"), a.Group)
	case "kubectl_apply":
		return fmt.Sprintf("kubectl apply -f %s", a.File)
	case "kubectl_delete":
		return fmt.Sprintf("kubectl delete -f %s", a.File)
	case "sysctl_set":
		return fmt.Sprintf("sysctl set %s", a.Key)
	case "sysctl_restore":
		return fmt.Sprintf("sysctl restore %s", a.Key)
	case "symlink":
		return fmt.Sprintf("ln -s %s %s", a.Source, a.Target)
	case "copy_dir":
		return fmt.Sprintf("cp -r %s %s", a.Source, a.Target)
	default:
		if a.Target != "" {
			return fmt.Sprintf("[%s] %s", a.Type, a.Target)
		}
		return fmt.Sprintf("[%s] %s", a.Type, a.Name)
	}
}

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
