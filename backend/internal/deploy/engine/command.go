package engine

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

type CommandRunner interface {
	Run(command string, args []string) CommandResult
}

type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

type TimeoutCommandRunner interface {
	RunWithTimeout(timeout time.Duration, command string, args []string) CommandResult
}

type TimeoutDirCommandRunner interface {
	RunInDirWithTimeout(timeout time.Duration, workDir string, command string, args []string) CommandResult
}

type ContextCommandRunner interface {
	RunContext(ctx context.Context, command string, args []string) CommandResult
}

type ContextTimeoutCommandRunner interface {
	RunWithContextTimeout(ctx context.Context, timeout time.Duration, command string, args []string) CommandResult
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(command string, args []string) CommandResult {
	cmd := exec.Command(command, args...)
	return runExecCommand(cmd)
}

func (ExecCommandRunner) RunContext(ctx context.Context, command string, args []string) CommandResult {
	cmd := exec.CommandContext(ctx, command, args...)
	return runExecCommandWithContext(cmd, ctx, 130)
}

func (ExecCommandRunner) RunInDir(workDir string, command string, args []string) CommandResult {
	cmd := exec.Command(command, args...)
	cmd.Dir = workDir
	return runExecCommand(cmd)
}

func (ExecCommandRunner) RunWithTimeout(timeout time.Duration, command string, args []string) CommandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	return runExecCommandWithContext(cmd, ctx, 124)
}

func (ExecCommandRunner) RunWithContextTimeout(parent context.Context, timeout time.Duration, command string, args []string) CommandResult {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	return runExecCommandWithContext(cmd, ctx, 124)
}

func (ExecCommandRunner) RunInDirWithTimeout(timeout time.Duration, workDir string, command string, args []string) CommandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workDir
	return runExecCommandWithContext(cmd, ctx, 124)
}

func runExecCommand(cmd *exec.Cmd) CommandResult {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CommandResult{
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Err:      err,
	}
	if err == nil {
		return result
	}
	result.ExitCode = 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	}
	return result
}

func runExecCommandWithContext(cmd *exec.Cmd, ctx context.Context, canceledExitCode int) CommandResult {
	result := runExecCommand(cmd)
	if err := ctx.Err(); err != nil {
		result.Err = err
		result.ExitCode = canceledExitCode
	}
	return result
}

func commandFailure(ctx TaskContext, actionName string, position string, result CommandResult) *DeployError {
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   position,
		Reason:     "命令执行失败",
		Detail:     commandResultDetail(result),
		Suggestion: "根据命令输出定位失败原因。",
	}
}

// commandResultDetail extracts a human-readable error detail from a
// CommandResult — used by executeCommandAction and (historically) the
// retired remote.go to surface the most relevant message in a
// DeployError.Detail field.
func commandResultDetail(result CommandResult) string {
	detail := result.Stderr
	if detail == "" {
		detail = result.Stdout
	}
	if detail == "" && result.Err != nil {
		detail = result.Err.Error()
	}
	return detail
}
