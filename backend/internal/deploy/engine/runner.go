package engine

import (
	"context"
	"strings"
	"time"
)

type Logger interface {
	TaskStart(ctx TaskContext)
	ActionResult(result ActionResult)
}

type Runner struct {
	Executor        *ActionExecutor
	NewExecutor     func(item PlannedTask) *ActionExecutor
	Logger          Logger
	ContinueOnError bool
}

func (r Runner) Execute(plan []PlannedTask) error {
	return r.ExecuteContext(context.Background(), plan)
}

func (r Runner) ExecuteContext(ctx context.Context, plan []PlannedTask) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var firstErr error
	for _, item := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.Logger != nil {
			r.Logger.TaskStart(item.Context)
		}
		executor := r.Executor
		if r.NewExecutor != nil {
			executor = r.NewExecutor(item)
		}
		for _, action := range item.Task.Actions {
			if err := ctx.Err(); err != nil {
				return err
			}
			action = replaceVariablesInAction(action, map[string]string{
				"host.name":    item.Host.Name,
				"host.address": item.Host.Address,
			})
			start := time.Now()
			result, err := executor.ExecuteContext(ctx, item.Context, action)
			if err != nil {
				if r.Logger != nil {
					r.Logger.ActionResult(failedResult(item.Context, action, err, start))
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					err = ctxErr
				}
				if !r.ContinueOnError {
					return err
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if r.Logger != nil {
				r.Logger.ActionResult(result)
			}
		}
	}
	return firstErr
}

func failedResult(ctx TaskContext, action ActionSpec, err error, start time.Time) ActionResult {
	actionName := actionDisplayName(action)
	target := actionTarget(action)
	// Pull the structured fields out of *DeployError so the UI gets
	// the full "为什么失败" instead of just the action target.
	// Without this, hubLogger.ActionResult ends up writing
	// Detail=target and operators see something like
	//   复制文件 [失败] /opt/his-deploy/foo
	// which is useless for triage. We want
	//   复制文件 [失败] reason: 命令执行失败 / detail: exit_code=1 ...
	detail := ""
	var deployErr *DeployError
	if AsDeployError(err, &deployErr) {
		if deployErr.Action != "" {
			actionName = deployErr.Action
		}
		parts := make([]string, 0, 3)
		if deployErr.Reason != "" {
			parts = append(parts, deployErr.Reason)
		}
		if deployErr.Detail != "" {
			parts = append(parts, deployErr.Detail)
		}
		if deployErr.Suggestion != "" {
			parts = append(parts, "建议: "+deployErr.Suggestion)
		}
		detail = strings.Join(parts, " | ")
	} else if err != nil {
		detail = err.Error()
	}
	return ActionResult{
		Context:  ctx,
		Action:   actionName,
		Target:   target,
		Status:   "失败",
		Duration: time.Since(start),
		Detail:   detail,
	}
}
