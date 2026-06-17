package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/deploy/engine"
	"df-build-server/internal/deploy/engine/render"
	"df-build-server/internal/deploy/engine/store"
	"df-build-server/internal/deploy/engine/virtualcomponents"
)

// ConfigLoader returns a freshly parsed deployment config for a given
// run directory. The render phase has already written config.yml /
// custom.yml into runDir before this is called.
type ConfigLoader func(runDir string) (*engine.Config, error)

// ActionExecutorFactory builds the engine.ActionExecutor used for a run.
// Tests use this seam to inject mocks; production injects an executor that
// targets the local filesystem (or, later, a remote SSH executor).
type ActionExecutorFactory func(cfg *engine.Config) *engine.ActionExecutor

// DefaultActionExecutorFactory returns a factory that produces a local
// ActionExecutor configured from cluster settings. It is the production
// default for runtime.New.
func DefaultActionExecutorFactory(cfg *engine.Config) *engine.ActionExecutor {
	return engine.NewActionExecutor(engine.ActionExecutorOptions{
		ResourceDir: cfg.Cluster.ResourceDir,
		StateDir:    cfg.Cluster.StateDir,
	})
}

// Options bundles the runtime construction inputs so handlers can build
// the runtime once and inject it everywhere.
type Options struct {
	Store           store.Store
	ConfigLoader    ConfigLoader
	ExecutorFactory ActionExecutorFactory
	// Timeout is the per-run deadline. Defaults to 30 minutes.
	Timeout time.Duration
	// RetainDeployments is the number of deployments to keep after a run
	// completes (LRU). Defaults to 100. Set to 0 to disable.
	RetainDeployments int

	// TargetMode controls how the engine reaches deployment hosts:
	//   - "ssh"   (default) open ssh+sftp from the control node and
	//             drive the action plan directly. No agent on nodes.
	//   - "local" runs everything on the deploy node itself; only used
	//             by tests / dev fixtures
	TargetMode string

	// ResourceDir is the offline-resource root forwarded into the
	// rendered config.yml's cluster.resource_dir.
	ResourceDir string

	// RunsDir is the parent directory holding per-deployment render
	// dirs. Each Submit creates RunsDir/<id>/{config,custom}.yml.
	RunsDir string

	// CredentialResolver, when set, supplies per-host SSH credentials from an
	// external registry (df-build-system: Server Management). When it returns
	// ok=false for a host, the runtime falls back to the global SSH settings.
	CredentialResolver CredentialResolver
}

// Runtime ties together the manager (concurrency), hub (live logs), and
// executor (engine driver). One Runtime per dfctlv2-web process.
type Runtime struct {
	store        store.Store
	loader       ConfigLoader
	factory      ActionExecutorFactory
	timeout      time.Duration
	retain       int
	manager      *Manager
	hub          *Hub
	pruneSem     chan struct{} // serialise prune jobs
	targetMode   string        // "ssh" | "local"
	resourceDir  string
	runsDir      string
	credResolver CredentialResolver
}

// New wires a fresh runtime. Defaults are applied for missing fields.
func New(opts Options) *Runtime {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Minute
	}
	if opts.RetainDeployments < 0 {
		opts.RetainDeployments = 100
	}
	if opts.ExecutorFactory == nil {
		opts.ExecutorFactory = DefaultActionExecutorFactory
	}
	if opts.TargetMode == "" {
		opts.TargetMode = "ssh"
	}
	if opts.ConfigLoader == nil {
		opts.ConfigLoader = func(runDir string) (*engine.Config, error) {
			return engine.LoadDeploymentConfigFiles(
				filepath.Join(runDir, "config.yml"),
				filepath.Join(runDir, "custom.yml"),
			)
		}
	}
	return &Runtime{
		store:        opts.Store,
		loader:       opts.ConfigLoader,
		factory:      opts.ExecutorFactory,
		timeout:      opts.Timeout,
		retain:       opts.RetainDeployments,
		manager:      NewManager(),
		hub:          NewHub(opts.Store),
		pruneSem:     make(chan struct{}, 1),
		targetMode:   opts.TargetMode,
		resourceDir:  opts.ResourceDir,
		runsDir:      opts.RunsDir,
		credResolver: opts.CredentialResolver,
	}
}

// Manager exposes the concurrency manager for HTTP handlers.
func (r *Runtime) Manager() *Manager { return r.manager }

// Hub exposes the SSE hub for HTTP handlers.
func (r *Runtime) Hub() *Hub { return r.hub }

// Submit acquires the lock, renders this run's YAML files, persists the
// deployment as "running", and kicks off the engine in a background
// goroutine. On conflict it returns ErrConflict together with a
// *Conflict snapshot. The returned deployment is fully populated
// (id assigned, started_at set, run_dir set).
func (r *Runtime) Submit(ctx context.Context, opts RunOptions) (*store.Deployment, *Conflict, error) {
	if err := validateOptions(opts); err != nil {
		return nil, nil, err
	}

	taskType := string(opts.Mode)
	scopeKind := string(opts.Scope)
	target := opts.Component
	if opts.Scope == ScopeAll {
		target = "all"
	}

	now := time.Now().UTC()
	dep := &store.Deployment{
		TaskType:        taskType,
		TargetComponent: target,
		TargetHost:      opts.Host,
		DryRun:          opts.DryRun,
		Status:          "running",
		StartedAt:       &now,
		ScopeKind:       scopeKind,
		Phase:           opts.Phase,
	}
	if err := r.store.CreateDeployment(ctx, dep); err != nil {
		return nil, nil, fmt.Errorf("create deployment: %w", err)
	}

	runCtx, cancelRun, release, conflict, err := r.manager.Acquire(context.Background(), dep.ID, taskType, opts)
	if err != nil {
		// failed to acquire; mark deployment failed so it doesn't sit as "running"
		_ = r.markFinished(ctx, dep, "failed", "并发冲突", time.Since(now))
		return nil, conflict, err
	}

	go r.runOne(runCtx, cancelRun, release, dep, opts)
	return dep, nil, nil
}

func validateOptions(opts RunOptions) error {
	switch opts.Mode {
	case ModeDeploy, ModeRollback, ModePhase:
	default:
		return fmt.Errorf("invalid mode %q", opts.Mode)
	}
	switch opts.Scope {
	case ScopeAll:
		if opts.Mode == ModePhase {
			return fmt.Errorf("phase mode requires scope=component")
		}
	case ScopeComponent:
		if opts.Component == "" {
			return fmt.Errorf("component is required when scope=component")
		}
	default:
		return fmt.Errorf("invalid scope %q", opts.Scope)
	}
	if opts.Mode == ModePhase && opts.Phase == "" {
		return fmt.Errorf("phase is required when mode=phase")
	}
	return nil
}

// runOne drives the engine for a single deployment. It owns the timeout,
// cancellation, render → load → run → close lifecycle. The deployment
// row's RunDir field is populated here, after the run dir is materialised.
func (r *Runtime) runOne(ctx context.Context, cancelRun context.CancelFunc, release func(), dep *store.Deployment, opts RunOptions) {
	defer release()

	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, r.effectiveTimeout(ctx))
	defer cancelTimeout()

	// Render database state into <runs_dir>/<deployment_id>/ so the
	// dfctl engine can keep treating "config + custom YAML in a
	// directory" as the source of truth.
	runDir := ""
	if r.runsDir != "" {
		runDir = filepath.Join(r.runsDir, strconv.FormatInt(dep.ID, 10))
		if _, err := render.FromStore(timeoutCtx, r.store, render.Inputs{
			RunDir:      runDir,
			ResourceDir: r.resourceDir,
		}); err != nil {
			r.failRun(timeoutCtx, dep, fmt.Sprintf("渲染部署文件失败: %v", err))
			return
		}
		dep.RunDir = runDir
		_ = r.store.UpdateDeployment(timeoutCtx, dep) // best effort; failure not fatal
	}

	cfg, err := r.loader(runDir)
	if err != nil {
		r.failRun(timeoutCtx, dep, fmt.Sprintf("加载配置失败: %v", err))
		return
	}

	r.hub.Publish(timeoutCtx, dep.ID, LogEntry{
		Component: dep.TargetComponent,
		Action:    "任务启动",
		Type:      "task_event",
		Status:    "running",
		Detail: fmt.Sprintf("mode=%s scope=%s component=%s phase=%s host=%s dry_run=%v",
			opts.Mode, opts.Scope, opts.Component, opts.Phase, opts.Host, opts.DryRun),
	})

	components, err := r.expandScope(cfg, opts)
	if err != nil {
		r.failRun(timeoutCtx, dep, err.Error())
		return
	}
	if len(components) == 0 {
		r.failRun(timeoutCtx, dep, "没有可执行的组件")
		return
	}

	// Dry-run short-circuits all dispatch paths: we build the plan,
	// publish it as a sequence of synthetic "dry_run_plan" log entries,
	// then mark the deployment success. No remote dispatch, no rsync, no
	// state mutation.
	if opts.DryRun {
		r.runDryRun(timeoutCtx, dep, cfg, opts, components)
		cancelRun()
		return
	}

	switch opts.Mode {
	case ModeDeploy:
		r.runDeploy(timeoutCtx, dep, cfg, opts, components)
	case ModeRollback:
		r.runRollback(timeoutCtx, dep, cfg, opts, components)
	case ModePhase:
		r.runPhase(timeoutCtx, dep, cfg, opts, components)
	}

	// Cancel the parent context so any goroutines watching the cancel
	// func tied to this run stop promptly.
	cancelRun()
}

// expandScope returns the component list to run, ordered as required.
//   - scope=component + virtual component name (e.g. "kubernetes-master"):
//     expand to its underlying pipeline components, ordered per the
//     pipeline's dependency graph.
//   - scope=component + pipeline component name: just [opts.Component]
//   - scope=all + mode=deploy: deploy order
//   - scope=all + mode=rollback: reverse deploy order (already done by
//     ComponentOrderForPhase("rollback"))
func (r *Runtime) expandScope(cfg *engine.Config, opts RunOptions) ([]string, error) {
	if opts.Scope == ScopeComponent {
		if vc, ok := virtualcomponents.Find(opts.Component); ok {
			// Walk the registered mappings and collect every pipeline
			// component name that exists in the loaded engine config.
			// We deliberately ignore mappings whose pipeline component
			// is missing from the engine — that would be a registry
			// drift and should produce a clear error elsewhere, not a
			// surprise empty deploy here.
			missing := []string{}
			set := map[string]bool{}
			for _, m := range vc.Mappings {
				if _, ok := cfg.Components[m.Name]; !ok {
					missing = append(missing, m.Name)
					continue
				}
				set[m.Name] = true
			}
			if len(set) == 0 {
				return nil, fmt.Errorf("虚拟组件 %s 展开后没有可执行的 pipeline 组件 (缺少: %v)", opts.Component, missing)
			}
			// Order by the engine's deploy/rollback sequence so
			// dependencies execute in the right direction.
			phaseHint := "deploy"
			if opts.Mode == ModeRollback {
				phaseHint = "rollback"
			}
			full, err := engine.ComponentOrderForPhase(cfg, phaseHint)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(set))
			for _, name := range full {
				if set[name] {
					out = append(out, name)
				}
			}
			return out, nil
		}
		if _, ok := cfg.Components[opts.Component]; !ok {
			return nil, fmt.Errorf("组件不存在: %s", opts.Component)
		}
		return []string{opts.Component}, nil
	}
	phaseHint := "deploy"
	if opts.Mode == ModeRollback {
		phaseHint = "rollback"
	}
	return engine.ComponentOrderForPhase(cfg, phaseHint)
}

func (r *Runtime) runDeploy(ctx context.Context, dep *store.Deployment, cfg *engine.Config, opts RunOptions, components []string) {
	succeeded := make([]string, 0, len(components))
	for _, comp := range components {
		if err := ctx.Err(); err != nil {
			r.failRunWithRollback(ctx, dep, cfg, opts, succeeded, ctxFailReason(err))
			return
		}
		if r.targetMode == "ssh" {
			// Remote path: open one ssh+sftp connection per host
			// and drive the action plan from the control node.
			// preflight / render / deploy / test all run through a
			// single dispatch call which expands them into the per-
			// host plan internally.
			if err := r.runRemoteComponent(ctx, dep.ID, cfg, comp, "deploy", opts.Host, false); err != nil {
				reason := fmt.Sprintf("组件 %s 远程执行失败: %v", comp, err)
				if ctxErr := ctx.Err(); ctxErr != nil {
					reason = ctxFailReason(ctxErr)
				}
				r.failRunWithRollback(ctx, dep, cfg, opts, succeeded, reason)
				return
			}
			succeeded = append(succeeded, comp)
			continue
		}
		// Local path (default for tests).
		plan, err := engine.BuildPlanForPhases(cfg, comp, engine.DeployPhases(), opts.Host)
		if err != nil {
			if isNoTask(err) {
				continue
			}
			r.failRunWithRollback(ctx, dep, cfg, opts, succeeded, fmt.Sprintf("组件 %s 计划生成失败: %v", comp, err))
			return
		}
		runner := engine.Runner{
			Executor: r.factory(cfg),
			Logger:   r.hub.Logger(dep.ID, comp),
		}
		if err := runner.ExecuteContext(ctx, plan); err != nil {
			reason := fmt.Sprintf("组件 %s 执行失败: %v", comp, err)
			if ctxErr := ctx.Err(); ctxErr != nil {
				reason = ctxFailReason(ctxErr)
			}
			r.failRunWithRollback(ctx, dep, cfg, opts, succeeded, reason)
			return
		}
		succeeded = append(succeeded, comp)
	}
	r.successRun(ctx, dep)
}

func (r *Runtime) runRollback(ctx context.Context, dep *store.Deployment, cfg *engine.Config, opts RunOptions, components []string) {
	// For all-scope cleanup, skip components that were never deployed
	// (state = not_deployed). We use the persisted deploy_state map as
	// the source of truth instead of e.g. checking targets, because
	// (a) state survives UI binding edits and (b) it already aggregates
	// virtualcomponent → pipeline-component ownership.
	if opts.Scope == ScopeAll {
		filtered, skipped := r.skipUndeployedComponents(ctx, components)
		for _, name := range skipped {
			r.hub.Publish(ctx, dep.ID, LogEntry{
				Component: name, Action: "跳过未部署组件", Type: "component_skip",
				Status: "skipped", Detail: "组件未部署,清理时跳过",
			})
		}
		components = filtered
		if len(components) == 0 {
			r.hub.Publish(ctx, dep.ID, LogEntry{
				Component: "all", Action: "全部跳过", Type: "phase_summary",
				Status: "success", Detail: "无已部署组件,无需清理",
			})
			r.successRun(ctx, dep)
			return
		}
	}

	var firstErr error
	// Track per-pipeline rollback success so we can mark
	// virtualcomponents as not_deployed only when ALL their pipelines
	// finished cleanly. Without this, cleanup-all is invisible to the
	// state machine and a re-run can't tell what's already clean.
	succeeded := make(map[string]bool, len(components))
	for _, comp := range components {
		if err := ctx.Err(); err != nil {
			firstErr = err
			break
		}
		// rollback phase
		if err := r.runComponentPhase(ctx, dep.ID, cfg, comp, "rollback", opts.Host, true); err != nil {
			r.hub.Publish(ctx, dep.ID, LogEntry{
				Component: comp, Action: "回滚", Type: "phase_error",
				Status: "失败", Detail: err.Error(), IsError: true,
			})
			if firstErr == nil {
				firstErr = err
			}
			// rollback continues across components per §4.5
		} else {
			succeeded[comp] = true
		}
		// auto residue per §4.3
		if err := r.runComponentPhase(ctx, dep.ID, cfg, comp, "residue", opts.Host, true); err != nil {
			// residue failures are reported but not fatal
			r.hub.Publish(ctx, dep.ID, LogEntry{
				Component: comp, Action: "残留检查", Type: "phase_error",
				Status: "失败", Detail: err.Error(), IsError: true,
			})
		}
	}
	if opts.Scope == ScopeAll {
		// Mark virtualcomponents as not_deployed where ALL their
		// pipeline children rolled back successfully. This makes
		// re-running cleanup idempotent: the all-scope skip filter
		// reads these state rows and excludes already-cleaned vc's
		// from the next pass — so a partial cleanup followed by a
		// retry won't double-rollback the prefix that already
		// succeeded (which previously broke things like plugin
		// rollback re-running after kubectl was already removed).
		r.markVirtualComponentsCleaned(ctx, dep, components, succeeded)
	}
	if firstErr != nil {
		r.failRun(ctx, dep, firstErr.Error())
		return
	}
	r.successRun(ctx, dep)
}

// markVirtualComponentsCleaned writes not_deployed for every
// virtualcomponent whose underlying pipeline components (those that
// were in this run's scope) all rolled back successfully, AND for
// every successfully-rolled-back pipeline component name itself.
// This makes the cleanup-all skip filter work for both keying styles
// (single-mapping vc/pipeline like redis/nacos use the pipeline name
// as state key; multi-mapping vc like kubernetes-master uses the vc
// name).
func (r *Runtime) markVirtualComponentsCleaned(ctx context.Context, dep *store.Deployment, components []string, succeeded map[string]bool) {
	if r.store == nil {
		return
	}
	// 1) Direct: for each succeeded pipeline component, write its own
	//    state row to not_deployed. This covers single-mapping
	//    components keyed by their own name.
	for _, comp := range components {
		if succeeded[comp] {
			_ = r.store.SetComponentDeployState(ctx, comp, store.DeployStateNotDeployed, dep.ID)
		}
	}
	// 2) Aggregate: for each registered vc, mark not_deployed only
	//    when ALL of its in-scope pipelines rolled back cleanly.
	inScope := make(map[string]bool, len(components))
	for _, c := range components {
		inScope[c] = true
	}
	for _, vc := range virtualcomponents.All() {
		anyInScope := false
		allOk := true
		for _, m := range vc.Mappings {
			if !inScope[m.Name] {
				continue
			}
			anyInScope = true
			if !succeeded[m.Name] {
				allOk = false
				break
			}
		}
		if anyInScope && allOk {
			_ = r.store.SetComponentDeployState(ctx, vc.Name, store.DeployStateNotDeployed, dep.ID)
		}
	}
}

// skipUndeployedComponents partitions the rollback all-scope component
// list into (toClean, skipped) using component_deploy_state as the
// source of truth. A pipeline component is kept when EITHER its own
// state row OR any of its owning virtualcomponents' state rows is not
// "not_deployed" — this handles both single-mapping components (redis,
// nacos: state keyed by the same name) and multi-mapping virtual
// components (kubernetes-master expands to etcd/master/node/...:
// state keyed by the vc name, not the pipeline names).
//
// Falls back to "keep all" on any store error so we never silently
// skip cleanup the operator asked for due to a transient lookup
// failure.
func (r *Runtime) skipUndeployedComponents(ctx context.Context, components []string) (toClean []string, skipped []string) {
	if r.store == nil {
		return components, nil
	}
	states, err := r.store.ListComponentDeployStates(ctx)
	if err != nil {
		return components, nil
	}
	byName := make(map[string]string, len(states))
	for _, s := range states {
		byName[s.ComponentName] = s.Status
	}
	isClean := func(state string) bool {
		// Empty (no row) defaults to not_deployed for our purposes.
		return state == "" || state == store.DeployStateNotDeployed
	}
	for _, comp := range components {
		// 1) direct hit (single-pipeline components like redis, nacos,
		//    rabbitmq use the same key for vc and pipeline)
		if !isClean(byName[comp]) {
			toClean = append(toClean, comp)
			continue
		}
		// 2) check every vc that owns this pipeline component (e.g.
		//    `etcd` is owned by kubernetes-master). If any owner shows
		//    a deployed/failed state, we must clean this pipeline too.
		owners := virtualcomponents.Owners(comp)
		anyDirty := false
		for _, vc := range owners {
			if !isClean(byName[vc.Name]) {
				anyDirty = true
				break
			}
		}
		if anyDirty {
			toClean = append(toClean, comp)
		} else {
			skipped = append(skipped, comp)
		}
	}
	return toClean, skipped
}

func (r *Runtime) runPhase(ctx context.Context, dep *store.Deployment, cfg *engine.Config, opts RunOptions, components []string) {
	if len(components) != 1 {
		r.failRun(ctx, dep, "phase 模式仅支持单组件")
		return
	}
	if err := r.runComponentPhase(ctx, dep.ID, cfg, components[0], opts.Phase, opts.Host, false); err != nil {
		r.failRun(ctx, dep, err.Error())
		return
	}
	r.successRun(ctx, dep)
}

func (r *Runtime) runComponentPhase(ctx context.Context, depID int64, cfg *engine.Config, comp, phase, host string, continueOnError bool) error {
	if r.targetMode == "ssh" {
		return r.runRemoteComponent(ctx, depID, cfg, comp, phase, host, continueOnError)
	}
	plan, err := engine.BuildPlanForHost(cfg, comp, phase, host)
	if err != nil {
		if isNoTask(err) {
			return nil
		}
		return err
	}
	runner := engine.Runner{
		Executor:        r.factory(cfg),
		Logger:          r.hub.Logger(depID, comp),
		ContinueOnError: continueOnError,
	}
	return runner.ExecuteContext(ctx, plan)
}

// failRunWithRollback aborts a deploy run, reverses the successful prefix
// per §3.6, then marks the deployment failed.
func (r *Runtime) failRunWithRollback(ctx context.Context, dep *store.Deployment, cfg *engine.Config, opts RunOptions, succeeded []string, reason string) {
	// Best-effort rollback of the succeeded prefix in reverse order. Only
	// fires for full deploys (§3.6). Component-scope failures keep state
	// alone — operators decide what to do.
	if opts.Mode == ModeDeploy && opts.Scope == ScopeAll && len(succeeded) > 0 {
		// Use an independent context so a deadline-exceeded or canceled
		// parent ctx doesn't poison every rollback attempt with the same
		// error and drown the real failure cause in noise. 10 minutes
		// is enough for a typical multi-component cleanup; if cleanup
		// itself wedges, the operator still sees the original failure
		// reason in the deployment summary.
		rbCtx, cancelRb := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancelRb()
		r.hub.Publish(rbCtx, dep.ID, LogEntry{
			Component: dep.TargetComponent,
			Action:    "自动回滚已成功组件",
			Type:      "phase_event",
			Status:    "running",
			Detail:    fmt.Sprintf("succeeded=%v (使用独立 10 分钟超时,与主任务超时无关)", succeeded),
		})
		for i := len(succeeded) - 1; i >= 0; i-- {
			comp := succeeded[i]
			if rerr := r.runComponentPhase(rbCtx, dep.ID, cfg, comp, "rollback", opts.Host, true); rerr != nil {
				r.hub.Publish(rbCtx, dep.ID, LogEntry{
					Component: comp, Action: "自动回滚", Type: "phase_error",
					Status: "失败", Detail: rerr.Error(), IsError: true,
				})
			}
		}
	}
	r.failRun(ctx, dep, reason)
}

func (r *Runtime) failRun(ctx context.Context, dep *store.Deployment, reason string) {
	finishCtx := finishContext(ctx)
	r.hub.Publish(finishCtx, dep.ID, LogEntry{
		Component: dep.TargetComponent,
		Action:    "任务失败",
		Type:      "task_event",
		Status:    "failed",
		Detail:    reason,
		IsError:   true,
	})
	if err := r.markFinished(finishCtx, dep, "failed", reason, durationSince(dep.StartedAt)); err != nil {
		// markFinished failures used to be silent, which lets a
		// deployment row stay at status='running' forever — the UI
		// shows a zombie task and the manager's per-component lock
		// blocks every retry. Surface the error to the SSE stream
		// so the operator at least sees something is wrong with the
		// store.
		r.hub.Publish(finishCtx, dep.ID, LogEntry{
			Component: dep.TargetComponent,
			Action:    "持久化任务终态失败",
			Type:      "task_event",
			Status:    "warning",
			Detail:    "无法将任务标记为 failed: " + err.Error() + " (任务可能在列表中持续显示为 running,需手工排查 PostgreSQL/磁盘)",
			IsError:   true,
		})
	}
	r.recordComponentDeployState(finishCtx, dep, false)
	r.hub.Close(dep.ID, "failed")
	r.afterFinish()
}

func (r *Runtime) successRun(ctx context.Context, dep *store.Deployment) {
	finishCtx := finishContext(ctx)
	r.hub.Publish(finishCtx, dep.ID, LogEntry{
		Component: dep.TargetComponent,
		Action:    "任务完成",
		Type:      "task_event",
		Status:    "success",
		Detail:    "执行成功",
	})
	if err := r.markFinished(finishCtx, dep, "success", "", durationSince(dep.StartedAt)); err != nil {
		// Same rationale as failRun — don't let store errors silently
		// strand a successful deployment in 'running' state.
		r.hub.Publish(finishCtx, dep.ID, LogEntry{
			Component: dep.TargetComponent,
			Action:    "持久化任务终态失败",
			Type:      "task_event",
			Status:    "warning",
			Detail:    "无法将任务标记为 success: " + err.Error() + " (任务可能在列表中持续显示为 running,需手工排查 PostgreSQL/磁盘)",
			IsError:   true,
		})
	}
	r.recordComponentDeployState(finishCtx, dep, true)
	r.hub.Close(dep.ID, "success")
	r.afterFinish()
}

// recordComponentDeployState advances the component deploy-state
// machine after a run reaches a terminal state. It is the single
// place that translates "a deploy/rollback finished" into the
// not_deployed/deployed/failed status the UI badge and the
// clean-before-redeploy constraint read.
//
// Skips:
//   - dry-run (no real changes)
//   - all-scope runs (state is tracked per single component; all-scope
//     is being phased out of the UI anyway)
//   - phase-mode runs (a single phase doesn't represent a full deploy)
//   - rollback failures (state stays whatever it was — operator must
//     analyse and re-run rollback; rollback is re-entrant)
//
// Transitions:
//   - deploy  success → deployed
//   - deploy  failure → failed (residue likely; must rollback to clean)
//   - rollback success → not_deployed
func (r *Runtime) recordComponentDeployState(ctx context.Context, dep *store.Deployment, success bool) {
	if r.store == nil || dep == nil {
		return
	}
	if dep.DryRun || dep.ScopeKind == string(ScopeAll) {
		return
	}
	component := strings.TrimSpace(dep.TargetComponent)
	if component == "" || component == "all" {
		return
	}
	var next string
	switch dep.TaskType {
	case string(ModeDeploy):
		if success {
			next = store.DeployStateDeployed
		} else {
			next = store.DeployStateFailed
		}
	case string(ModeRollback):
		if success {
			next = store.DeployStateNotDeployed
		} else {
			// Rollback failed — leave state as-is so the operator can
			// fix the problem and re-run rollback (re-entrant).
			return
		}
	default:
		// ModePhase or anything else: not a full deploy lifecycle event.
		return
	}
	_ = r.store.SetComponentDeployState(ctx, component, next, dep.ID)
}

// runDryRun walks the plan without executing any actions and publishes
// it as a series of LogEntry rows so operators can review what _would_
// have happened. It always finishes the run as success unless context
// cancellation intervenes.
func (r *Runtime) runDryRun(ctx context.Context, dep *store.Deployment, cfg *engine.Config, opts RunOptions, components []string) {
	phases := r.phasesFor(opts)
	totalActions := 0
	totalTasks := 0
	for _, comp := range components {
		if err := ctx.Err(); err != nil {
			r.failRun(ctx, dep, ctxFailReason(err))
			return
		}
		for _, phase := range phases {
			plan, err := engine.BuildPlanForHost(cfg, comp, phase, opts.Host)
			if err != nil {
				if isNoTask(err) {
					continue
				}
				r.failRun(ctx, dep, fmt.Sprintf("dry-run: 组件 %s 计划生成失败: %v", comp, err))
				return
			}
			for _, item := range plan {
				totalTasks++
				totalActions += len(item.Task.Actions)
				r.hub.Publish(ctx, dep.ID, LogEntry{
					Component: comp,
					Host:      item.Host.Name,
					Action:    item.Task.Name,
					Type:      "dry_run_plan",
					Status:    "skipped",
					Detail: fmt.Sprintf("phase=%s task_id=%s host=%s actions=%d",
						item.Task.Phase, item.Task.ID, item.Host.Address, len(item.Task.Actions)),
				})
			}
		}
	}
	finishCtx := finishContext(ctx)
	r.hub.Publish(finishCtx, dep.ID, LogEntry{
		Component: dep.TargetComponent,
		Action:    "dry-run 总结",
		Type:      "dry_run_summary",
		Status:    "success",
		Detail: fmt.Sprintf("将执行 %d 个任务,%d 个动作,涉及 %d 个组件",
			totalTasks, totalActions, len(components)),
	})
	_ = r.markFinished(finishCtx, dep, "success", "dry-run", durationSince(dep.StartedAt))
	r.hub.Close(dep.ID, "success")
	r.afterFinish()
}

// phasesFor returns the phase list a dry-run should walk for the given
// opts. Mirrors the live dispatch paths in runDeploy/runRollback/runPhase.
func (r *Runtime) phasesFor(opts RunOptions) []string {
	switch opts.Mode {
	case ModeDeploy:
		return engine.DeployPhases()
	case ModeRollback:
		return []string{"rollback", "residue"}
	case ModePhase:
		return []string{opts.Phase}
	}
	return nil
}

func (r *Runtime) markFinished(ctx context.Context, dep *store.Deployment, status, reason string, dur time.Duration) error {
	end := time.Now().UTC()
	dep.Status = status
	dep.EndedAt = &end
	dep.ErrorSummary = reason
	dep.DurationMS = dur.Milliseconds()
	if dep.DurationMS < 0 {
		dep.DurationMS = 0
	}
	return r.store.UpdateDeployment(ctx, dep)
}

// afterFinish kicks off LRU pruning asynchronously. Single-flight via a
// 1-slot semaphore — we don't care if a prune skips when one is already
// in flight, the next finish will trigger another.
func (r *Runtime) afterFinish() {
	if r.store == nil {
		return
	}
	retain := r.effectiveRetain(context.Background())
	if retain <= 0 {
		return
	}
	select {
	case r.pruneSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-r.pruneSem }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = r.store.PruneOldDeployments(ctx, retain)
	}()
}

func (r *Runtime) effectiveTimeout(ctx context.Context) time.Duration {
	timeout := r.timeout
	if r.store == nil {
		return timeout
	}
	settings, err := r.store.GetDeploymentSettings(ctx)
	if err != nil || settings == nil || settings.DefaultTimeoutSeconds <= 0 {
		return timeout
	}
	return time.Duration(settings.DefaultTimeoutSeconds) * time.Second
}

func (r *Runtime) effectiveRetain(ctx context.Context) int {
	retain := r.retain
	if r.store == nil {
		return retain
	}
	settings, err := r.store.GetDeploymentSettings(ctx)
	if err != nil || settings == nil {
		return retain
	}
	return settings.RetainDeployments
}

func finishContext(ctx context.Context) context.Context {
	if ctx == nil || ctx.Err() != nil {
		return context.Background()
	}
	return ctx
}

func ctxFailReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "执行超时"
	}
	if errors.Is(err, context.Canceled) {
		return "任务被取消"
	}
	return err.Error()
}

func durationSince(start *time.Time) time.Duration {
	if start == nil {
		return 0
	}
	return time.Since(*start)
}

func isNoTask(err error) bool {
	var de *engine.DeployError
	if !errors.As(err, &de) {
		return false
	}
	return de.Reason == "没有匹配任务" || de.Reason == "目标主机没有匹配任务"
}
