// Package handler exposes the deployment-management HTTP API under
// /api/deployment/* using Gin, the project's JWT middleware, unified response
// format, and the engine runtime's SSE hub.
package handler

import (
	"fmt"
	"strconv"

	"df-build-server/internal/deploy"
	"df-build-server/internal/deploy/conflict"
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
		g.POST("/runs/rollback", h.Rollback)
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

type componentDTO struct {
	Code     string `json:"code"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Enabled  bool   `json:"enabled"`
}

func (h *Handler) ListComponents(c *gin.Context) {
	ctx := c.Request.Context()
	st := h.svc.Store()

	states, err := st.ListComponentDeployStates(ctx)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	stateByName := map[string]string{}
	for _, s := range states {
		stateByName[s.ComponentName] = s.Status
	}
	enabled, _ := st.ListEnabledComponents(ctx)
	enabledSet := map[string]bool{}
	for _, e := range enabled {
		enabledSet[e.Name] = true
	}

	out := []componentDTO{}
	for _, name := range virtualcomponents.PipelineComponents() {
		status := stateByName[name]
		if status == "" {
			status = store.DeployStateNotDeployed
		}
		out = append(out, componentDTO{
			Code:     name,
			Category: categoryOf(name),
			Status:   status,
			Enabled:  enabledSet[name],
		})
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
		Components []string `json:"components"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	if err := h.svc.Store().ReplaceEnabledComponents(c.Request.Context(), body.Components); err != nil {
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
	t, err := h.svc.Store().GetTargets(c.Request.Context(), c.Param("component"))
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, t)
}

func (h *Handler) PutTargets(c *gin.Context) {
	component := c.Param("component")
	var body struct {
		ServerIDs []int64 `json:"serverIds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	ctx := c.Request.Context()
	st := h.svc.Store()

	// Conflict pre-check: simulate the proposed binding against current state.
	all, err := st.ListAllTargets(ctx)
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	proposed := replaceComponentTargets(all, component, body.ServerIDs)
	if violations := conflict.Validate(proposed); len(violations) > 0 {
		response.FailWithStatus(c, 409, 4091, conflictMessage(violations))
		return
	}

	if err := st.ReplaceTargets(ctx, component, body.ServerIDs); err != nil {
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

func (h *Handler) CreateRun(c *gin.Context) {
	h.submit(c, runtime.ModeDeploy)
}

func (h *Handler) Rollback(c *gin.Context) {
	h.submit(c, runtime.ModeRollback)
}

func (h *Handler) PreviewRun(c *gin.Context) {
	// Preview is a dry-run: builds and publishes the plan without dispatch.
	var body struct {
		Component string `json:"component"`
		All       bool   `json:"all"`
	}
	_ = c.ShouldBindJSON(&body)
	if !body.All {
		if msg, ok := h.checkHasTargets(c, body.Component); !ok {
			response.Fail(c, 4001, msg)
			return
		}
	}
	opts := runtime.RunOptions{Mode: runtime.ModeDeploy, DryRun: true}
	if body.All {
		opts.Scope = runtime.ScopeAll
	} else {
		opts.Scope = runtime.ScopeComponent
		opts.Component = body.Component
	}
	dep, conf, err := h.svc.Runtime().Submit(c.Request.Context(), opts)
	if err != nil {
		if conf != nil {
			response.FailWithStatus(c, 409, 4090, "已有进行中的部署")
			return
		}
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"runId": dep.ID, "dryRun": true})
}

func (h *Handler) submit(c *gin.Context, mode runtime.Mode) {
	var body struct {
		Component string `json:"component"`
		All       bool   `json:"all"`
		Host      string `json:"host"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	// Mirror his-deploy: all-scope deploy is intentionally disabled (its success
	// state isn't recorded per-component, defeating the clean-before-redeploy
	// guard). All-scope rollback remains allowed.
	if body.All && mode == runtime.ModeDeploy {
		response.Fail(c, 4001, "全量部署已关闭，请按组件依次部署；全量清理（回滚）仍可使用")
		return
	}
	if !body.All {
		if msg, ok := h.checkHasTargets(c, body.Component); !ok {
			response.Fail(c, 4001, msg)
			return
		}
		// Clean-before-redeploy hard constraint (mirrors his-deploy
		// deployStateBlocks): a component-scope deploy is only allowed when the
		// component is not_deployed. A deployed/failed component must be rolled
		// back first — re-deploying on residue is a whole class of bugs.
		if mode == runtime.ModeDeploy {
			if msg, blocked := h.deployStateBlocks(c, body.Component); blocked {
				response.FailWithStatus(c, 409, 4090, msg)
				return
			}
		}
	}
	// Conflict pre-check before any deploy run.
	if mode == runtime.ModeDeploy {
		all, err := h.svc.Store().ListAllTargets(c.Request.Context())
		if err == nil {
			if v := conflict.Validate(all); len(v) > 0 {
				response.FailWithStatus(c, 409, 4091, conflictMessage(v))
				return
			}
		}
	}
	opts := runtime.RunOptions{Mode: mode, Host: body.Host}
	if body.All {
		opts.Scope = runtime.ScopeAll
	} else {
		opts.Scope = runtime.ScopeComponent
		opts.Component = body.Component
	}
	dep, conf, err := h.svc.Runtime().Submit(c.Request.Context(), opts)
	if err != nil {
		if conf != nil {
			response.FailWithStatus(c, 409, 4090, "已有进行中的部署，请稍后再试")
			return
		}
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, gin.H{"runId": dep.ID})
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
