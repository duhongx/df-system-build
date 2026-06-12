package engine

import (
	"errors"
	"fmt"
)

type DeployError struct {
	Context    TaskContext
	Action     string
	Position   string
	Reason     string
	Detail     string
	Suggestion string
}

func AsDeployError(err error, target **DeployError) bool {
	return errors.As(err, target)
}

func (e *DeployError) Error() string {
	return fmt.Sprintf(
		"组件=%s 主机=%s 任务=%s 动作=%s\n异常位置：%s\n异常原因：%s\n异常详情：%s\n处理建议：%s",
		e.Context.Component,
		e.Context.HostName,
		e.Context.TaskName,
		e.Action,
		e.Position,
		e.Reason,
		e.Detail,
		e.Suggestion,
	)
}
