// Package handler exposes the deployment-management HTTP API under
// /api/deployment/* using Gin, the project's JWT middleware, unified response
// format, and the engine runtime's SSE hub.
package handler

import (
	"fmt"
	"strconv"

	"df-build-server/internal/deploy"
	"df-build-server/internal/deploy/conflict"
	"df-build-server/internal/deploy/engine"
	"df-build-server/internal/deploy/engine/runtime"
	"df-build-server/internal/deploy/engine/store"
	"df-build-server/internal/deploy/engine/virtualcomponents"
	"df-build-server/internal/deploy/validate"
	"df-build-server/internal/middleware"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

// Handler serves the deployment-management API.
type Handler struct {
	svc *deploy.Service
}

// New builds the handler from a deployment Service.
func New(svc *deploy.Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts all /deployment routes behind JWT auth.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/deployment")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/components", h.ListComponents)
		g.GET("/components/enabled", h.GetEnabled)
		g.PUT("/components/enabled", h.PutEnabled)

		g.GET("/global-config", h.GetGlobalConfig)
		g.PUT("/global-config", h.PutGlobalConfig)

		g.GET("/overrides", h.ListOverrides)
		g.GET("/overrides/:component", h.GetOverride)
		g.PUT("/overrides/:component", h.PutOverride)
		g.DELETE("/overrides/:component", h.DeleteOverride)

		g.GET("/targets", h.ListTargets)
		g.GET("/targets/:component", h.GetTargets)
		g.PUT("/targets/:component", h.PutTargets)
		g.POST("/host-checks", h.HostChecks)

		g.GET("/runs", h.ListRuns)
		g.GET("/runs/running", h.RunningRuns)
		g.POST("/runs", h.CreateRun)
		g.POST("/runs/preview", h.PreviewRun)
		g.GET("/runs/:id", h.GetRun)
		g.GET("/runs/:id/logs", h.RunLogs)
		g.GET("/runs/:id/logs.txt", h.RunLogsText)
		g.POST("/runs/:id/cancel", h.CancelRun)

		h.registerOffline(g)
		h.registerComponentExtras(g)
	}
	// SSE endpoint is registered outside the header-JWT group because the
	// browser EventSource API cannot set an Authorization header; it
	// authenticates via a ?token= query parameter instead.
	r.GET("/deployment/runs/:id/events", h.RunEvents)
}

// ---- components ----

func (h *Handler) ListComponents(c *gin.Context) {
	ctx := c.Request.Context()
	st := h.svc.Store()

	stateByName := map[string]string{}
	if states, err := st.ListComponentDeployStates(ctx); err == nil {
		for _, s := range states {
			stateByName[s.ComponentName] = s.Status
		}
	}
	enabledByPipeline := map[string]bool{}
	if enabled, err := st.ListEnabledComponents(ctx); err == nil {
		for _, e := range enabled {
			enabledByPipeline[e.Name] = true
		}
	}

	out := []vcSummary{}
	for _, vc := range virtualcomponents.All() {
		ownTargets, _ := st.ListTargetsForOwner(ctx, vc.Name)
		if ownTargets == nil {
			ownTargets = map[string][]int64{}
		}
		out = append(out, h.buildVCSummary(vc, enabledByPipeline, ownTargets, stateByName[vc.Name]))
	}
	response.OK(c, out)
}

func categoryOf(code string) string {
	if conflict.IsK8s(code) {
		return "k8s"
	}
	if conflict.IsBusiness(code) {
		return "business"
	}
	return "other"
}

func (h *Handler) GetEnabled(c *gin.Context) {
	enabled, err := h.svc.Store().ListEnabledComponents(c.Request.Context())
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	names := make([]string, 0, len(enabled))
	for _, e := range enabled {
		names = append(names, e.Name)
	}
	response.OK(c, names)
}

func (h *Handler) PutEnabled(c *gin.Context) {
	var body struct {
		Components []string `json:"components"` // virtual component names
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	// Expand enabled virtual components to their pipeline components.
	seen := map[string]bool{}
	pipeline := []string{}
	for _, vcName := range body.Components {
		vc, ok := virtualcomponents.Find(vcName)
		if !ok {
			continue
		}
		for _, m := range vc.Mappings {
			if !seen[m.Name] {
				seen[m.Name] = true
				pipeline = append(pipeline, m.Name)
			}
		}
	}
	if err := h.svc.Store().ReplaceEnabledComponents(c.Request.Context(), pipeline); err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"updated": true})
}

// ---- global config ----

func (h *Handler) GetGlobalConfig(c *gin.Context) {
	ctx := c.Request.Context()
	st := h.svc.Store()
	dep, err := st.GetDeploymentSettings(ctx)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	net, err := st.GetNetworkSettings(ctx)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	env, _ := st.ListEnv(ctx)
	response.OK(c, gin.H{"deployment": dep, "network": net, "env": env})
}

func (h *Handler) PutGlobalConfig(c *gin.Context) {
	var body struct {
		Deployment *store.DeploymentSettings `json:"deployment"`
		Network    *store.NetworkSettings    `json:"network"`
		Env        []*store.EnvEntry         `json:"env"`
		EnvReplace bool                      `json:"envReplace"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	// Validate password-like env values for shell metacharacters.
	for _, e := range body.Env {
		if e == nil {
			continue
		}
		if err := validate.PasswordParams(map[string]any{e.Key: e.Value}); err != nil {
			response.Fail(c, 4001, err.Error())
			return
		}
	}
	err := h.svc.Store().UpdateGlobalConfig(c.Request.Context(), store.GlobalConfigUpdate{
		Deployment: body.Deployment,
		Network:    body.Network,
		Env:        body.Env,
		EnvReplace: body.EnvReplace,
	})
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"updated": true})
}

// ---- overrides ----

func (h *Handler) ListOverrides(c *gin.Context) {
	list, err := h.svc.Store().ListOverrides(c.Request.Context())
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, list)
}

func (h *Handler) GetOverride(c *gin.Context) {
	component := c.Param("component")
	o, err := h.svc.Store().GetOverride(c.Request.Context(), component)
	if err != nil {
		// No override yet is a normal state — return an empty one so the editor
		// opens cleanly instead of surfacing a 404 error toast.
		response.OK(c, gin.H{"component_name": component, "params": gin.H{}})
		return
	}
	response.OK(c, o)
}

func (h *Handler) PutOverride(c *gin.Context) {
	var body struct {
		Params map[string]any `json:"params"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	if err := validate.PasswordParams(body.Params); err != nil {
		response.Fail(c, 4001, err.Error())
		return
	}
	o := &store.ComponentOverride{ComponentName: c.Param("component"), Params: body.Params}
	if err := h.svc.Store().UpsertOverride(c.Request.Context(), o); err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"updated": true})
}

func (h *Handler) DeleteOverride(c *gin.Context) {
	if err := h.svc.Store().DeleteOverride(c.Request.Context(), c.Param("component")); err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"deleted": true})
}

// ---- targets ----

func (h *Handler) ListTargets(c *gin.Context) {
	list, err := h.svc.Store().ListAllTargets(c.Request.Context())
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, list)
}

func (h *Handler) GetTargets(c *gin.Context) {
	name := c.Param("component")
	ctx := c.Request.Context()
	if vc, ok := virtualcomponents.Find(name); ok {
		own, _ := h.svc.Store().ListTargetsForOwner(ctx, vc.Name)
		if own == nil {
			own = map[string][]int64{}
		}
		hostIDs := vc.AggregateUserHosts(own)
		if hostIDs == nil {
			hostIDs = []int64{}
		}
		response.OK(c, gin.H{"component_name": name, "host_ids": hostIDs})
		return
	}
	t, err := h.svc.Store().GetTargets(ctx, name)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, t)
}

func (h *Handler) PutTargets(c *gin.Context) {
	name := c.Param("component")
	var body struct {
		ServerIDs []int64 `json:"serverIds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	ctx := c.Request.Context()
	st := h.svc.Store()

	if vc, isVirtual := virtualcomponents.Find(name); isVirtual {
		if err := vc.ValidateUserHostIDs(body.ServerIDs); err != nil {
			response.Fail(c, 4001, err.Error())
			return
		}
		allHosts, deployHost := h.deployContext()
		placements := vc.Expand(virtualcomponents.Inputs{
			UserSelectedHostIDs: body.ServerIDs,
			DeployHostID:        deployHost,
			AllHostIDs:          allHosts,
		})
		batch := make([]store.OwnerComponentHosts, 0, len(placements))
		for _, p := range placements {
			batch = append(batch, store.OwnerComponentHosts{Component: p.Component, OwnerVC: vc.Name, HostIDs: p.HostIDs})
		}
		if err := st.ReplaceTargetsForOwners(ctx, batch); err != nil {
			response.Fail(c, 5001, err.Error())
			return
		}
		// Conflict check on the resulting global snapshot (warn via 409).
		if all, err := st.ListAllTargets(ctx); err == nil {
			if v := conflict.Validate(all); len(v) > 0 {
				response.FailWithStatus(c, 409, 4091, conflictMessage(v))
				return
			}
		}
		response.OK(c, gin.H{"updated": true})
		return
	}

	all, err := st.ListAllTargets(ctx)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	proposed := replaceComponentTargets(all, name, body.ServerIDs)
	if violations := conflict.Validate(proposed); len(violations) > 0 {
		response.FailWithStatus(c, 409, 4091, conflictMessage(violations))
		return
	}
	if err := st.ReplaceTargets(ctx, name, body.ServerIDs); err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"updated": true})
}

func (h *Handler) HostChecks(c *gin.Context) {
	all, err := h.svc.Store().ListAllTargets(c.Request.Context())
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"conflicts": conflict.Validate(all)})
}

func replaceComponentTargets(all []*store.ComponentTargets, component string, serverIDs []int64) []*store.ComponentTargets {
	out := make([]*store.ComponentTargets, 0, len(all)+1)
	found := false
	for _, ct := range all {
		if ct.ComponentName == component {
			out = append(out, &store.ComponentTargets{ComponentName: component, HostIDs: serverIDs})
			found = true
		} else {
			out = append(out, ct)
		}
	}
	if !found {
		out = append(out, &store.ComponentTargets{ComponentName: component, HostIDs: serverIDs})
	}
	return out
}

func conflictMessage(vs []conflict.Conflict) string {
	if len(vs) == 0 {
		return ""
	}
	msg := "主机绑定冲突："
	for i, v := range vs {
		if i > 0 {
			msg += "；"
		}
		msg += v.String()
	}
	return msg
}

// ---- runs ----

// createRequest mirrors his-deploy's deployment create/preview body.
type createRequest struct {
	Mode      string  `json:"mode"`      // deploy | rollback | cleanup | phase
	Component string  `json:"component"` // pipeline or virtual component (scope=component)
	Phase     string  `json:"phase"`     // required when mode=phase
	Host      string  `json:"host"`      // optional single-host restriction
	HostIDs   []int64 `json:"host_ids"`  // selected target servers (scope=component)
	DryRun    bool    `json:"dry_run"`
}

// resolveOptions translates a createRequest into runtime.RunOptions. "cleanup"
// is a UI label for rollback + all-scope. Returns an error message when invalid.
func resolveOptions(body createRequest) (runtime.RunOptions, string) {
	switch body.Mode {
	case "cleanup":
		return runtime.RunOptions{Mode: runtime.ModeRollback, Scope: runtime.ScopeAll}, ""
	case "deploy", "rollback", "phase":
	default:
		return runtime.RunOptions{}, "无效的操作类型（应为 deploy/rollback/phase/cleanup）"
	}
	if body.Component == "" {
		return runtime.RunOptions{}, "请选择要操作的组件"
	}
	opts := runtime.RunOptions{
		Scope:     runtime.ScopeComponent,
		Component: body.Component,
		Host:      body.Host,
		DryRun:    body.DryRun,
	}
	switch body.Mode {
	case "deploy":
		opts.Mode = runtime.ModeDeploy
	case "rollback":
		opts.Mode = runtime.ModeRollback
	case "phase":
		opts.Mode = runtime.ModePhase
		if body.Phase == "" {
			return runtime.RunOptions{}, "单阶段模式必须选择阶段（phase）"
		}
		opts.Phase = body.Phase
	}
	return opts, ""
}

// CreateRun creates a deployment task (新建部署). Supports deploy / rollback /
// phase / cleanup, applies the selected target hosts before submit, and
// enforces the clean-before-redeploy hard constraint.
func (h *Handler) CreateRun(c *gin.Context) {
	var body createRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	opts, errMsg := resolveOptions(body)
	if errMsg != "" {
		response.Fail(c, 4001, errMsg)
		return
	}
	ctx := c.Request.Context()

	if opts.Scope == runtime.ScopeComponent {
		if opts.Mode == runtime.ModeDeploy && !opts.DryRun {
			if msg, blocked := h.deployStateBlocks(c, opts.Component); blocked {
				response.FailWithStatus(c, 409, 4090, msg)
				return
			}
		}
		// Apply the operator's host selection BEFORE submit so the engine sees
		// it (only after the state check passes). Skip virtual components.
		if body.HostIDs != nil {
			if _, isVirtual := virtualcomponents.Find(opts.Component); !isVirtual {
				if err := h.svc.Store().ReplaceTargets(ctx, opts.Component, body.HostIDs); err != nil {
					response.Fail(c, 5001, "写入目标主机失败: "+err.Error())
					return
				}
			}
		}
		if opts.Mode == runtime.ModeDeploy {
			if all, err := h.svc.Store().ListAllTargets(ctx); err == nil {
				if v := conflict.Validate(all); len(v) > 0 {
					response.FailWithStatus(c, 409, 4091, conflictMessage(v))
					return
				}
			}
		}
	}

	dep, conf, err := h.svc.Runtime().Submit(ctx, opts)
	if err != nil {
		if conf != nil {
			response.FailWithStatus(c, 409, 4090, "已有部署任务正在执行，无法启动新任务")
			return
		}
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"id": dep.ID})
}

// PreviewRun renders the current config and returns the planned tasks/actions
// without executing — the dialog's 预览 button.
func (h *Handler) PreviewRun(c *gin.Context) {
	var body createRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	opts, errMsg := resolveOptions(body)
	if errMsg != "" {
		response.Fail(c, 4001, errMsg)
		return
	}
	cfg, err := h.svc.RenderCurrent(c.Request.Context())
	if err != nil {
		response.Fail(c, 5001, "加载部署配置失败: "+err.Error())
		return
	}
	plan := h.buildPreviewPlan(cfg, opts)
	actionCount := 0
	for _, p := range plan {
		actionCount += p.Actions
	}
	response.OK(c, gin.H{
		"mode":         body.Mode,
		"component":    opts.Component,
		"plan":         plan,
		"task_count":   len(plan),
		"action_count": actionCount,
	})
}

type previewPlanItem struct {
	Component string `json:"component"`
	Phase     string `json:"phase"`
	TaskName  string `json:"task_name"`
	Host      string `json:"host"`
	Actions   int    `json:"actions"`
}

func (h *Handler) buildPreviewPlan(cfg *engine.Config, opts runtime.RunOptions) []previewPlanItem {
	out := []previewPlanItem{}
	for _, comp := range componentsForPreview(cfg, opts) {
		for _, phase := range phasesForPreview(opts) {
			plan, err := engine.BuildPlanForHost(cfg, comp, phase, opts.Host)
			if err != nil {
				continue
			}
			for _, item := range plan {
				out = append(out, previewPlanItem{
					Component: comp,
					Phase:     item.Task.Phase,
					TaskName:  item.Task.Name,
					Host:      item.Host.Name,
					Actions:   len(item.Task.Actions),
				})
			}
		}
	}
	return out
}

func componentsForPreview(cfg *engine.Config, opts runtime.RunOptions) []string {
	if opts.Scope == runtime.ScopeComponent {
		if vc, ok := virtualcomponents.Find(opts.Component); ok {
			set := map[string]bool{}
			for _, m := range vc.Mappings {
				if _, ok := cfg.Components[m.Name]; ok {
					set[m.Name] = true
				}
			}
			hint := "deploy"
			if opts.Mode == runtime.ModeRollback {
				hint = "rollback"
			}
			full, err := engine.ComponentOrderForPhase(cfg, hint)
			if err != nil {
				return []string{}
			}
			out := []string{}
			for _, name := range full {
				if set[name] {
					out = append(out, name)
				}
			}
			return out
		}
		return []string{opts.Component}
	}
	hint := "deploy"
	if opts.Mode == runtime.ModeRollback {
		hint = "rollback"
	}
	order, err := engine.ComponentOrderForPhase(cfg, hint)
	if err != nil {
		return []string{}
	}
	return order
}

func phasesForPreview(opts runtime.RunOptions) []string {
	switch opts.Mode {
	case runtime.ModePhase:
		return []string{opts.Phase}
	case runtime.ModeRollback:
		return []string{"rollback", "residue"}
	default:
		return []string{"preflight", "render", "deploy", "test"}
	}
}

// checkHasTargets returns ok=false with a friendly message when a non-virtual
// component has no bound target hosts. Virtual components are not checked here
// (their expansion is validated by the engine).
func (h *Handler) checkHasTargets(c *gin.Context, component string) (string, bool) {
	if component == "" {
		return "请选择要部署的组件", false
	}
	if _, isVirtual := virtualcomponents.Find(component); isVirtual {
		return "", true
	}
	t, err := h.svc.Store().GetTargets(c.Request.Context(), component)
	if err != nil {
		return "", true // let the engine surface any real error
	}
	if len(t.HostIDs) == 0 {
		return fmt.Sprintf("组件 %s 未绑定目标主机，请先在「主机绑定」页面选择主机", component), false
	}
	return "", true
}

// deployStateBlocks enforces the clean-before-redeploy hard constraint: a
// deployed or failed component must be rolled back before it can be deployed
// again. Returns (message, true) when the deploy should be blocked.
func (h *Handler) deployStateBlocks(c *gin.Context, component string) (string, bool) {
	st, err := h.svc.Store().GetComponentDeployState(c.Request.Context(), component)
	if err != nil || st == nil {
		return "", false // transient lookup error: don't wedge the operator
	}
	switch st.Status {
	case store.DeployStateDeployed:
		return fmt.Sprintf("组件「%s」已部署，重复部署可能因残留文件互相干扰。请先清理（回滚）后再部署。", component), true
	case store.DeployStateFailed:
		return fmt.Sprintf("组件「%s」上次部署失败，可能残留中间文件。请先分析问题并清理（回滚）后再部署。", component), true
	default:
		return "", false
	}
}

func (h *Handler) ListRuns(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	ctx := c.Request.Context()
	st := h.svc.Store()
	f := store.DeploymentFilter{Limit: pageSize, Offset: (page - 1) * pageSize}
	list, err := st.ListDeployments(ctx, f)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	total, _ := st.CountDeployments(ctx, store.DeploymentFilter{})
	response.OKWithPage(c, list, total, page, pageSize)
}

func (h *Handler) RunningRuns(c *gin.Context) {
	response.OK(c, h.svc.Runtime().Manager().SnapshotAll())
}

func (h *Handler) GetRun(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	dep, err := h.svc.Store().GetDeployment(c.Request.Context(), id)
	if err != nil {
		response.Fail(c, 4040, "部署运行不存在")
		return
	}
	response.OK(c, dep)
}

func (h *Handler) RunLogs(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	afterSeq, _ := strconv.ParseInt(c.DefaultQuery("afterSeq", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))
	logs, err := h.svc.Store().ListDeploymentLogs(c.Request.Context(), id, afterSeq, limit)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, logs)
}

func (h *Handler) RunLogsText(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	logs, err := h.svc.Store().ListDeploymentLogs(c.Request.Context(), id, 0, 0)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	for _, l := range logs {
		line := l.Timestamp.Format("2006-01-02 15:04:05") + " [" + l.Component + "/" + l.Host + "] " +
			l.ActionName + " " + l.Status
		if l.Detail != "" {
			line += " - " + l.Detail
		}
		c.Writer.WriteString(line + "\n")
	}
}

func (h *Handler) CancelRun(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	canceled := h.svc.Runtime().Manager().Cancel(id)
	response.OK(c, gin.H{"canceled": canceled})
}

func parseID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.Fail(c, 4001, "无效的运行 ID")
		return 0, false
	}
	return id, true
}
