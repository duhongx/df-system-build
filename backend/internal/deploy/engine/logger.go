package engine

import (
	"fmt"
	"io"
	"time"
)

type TextLogger struct {
	w io.Writer
}

func NewTextLogger(w io.Writer) *TextLogger {
	return &TextLogger{w: w}
}

func (l *TextLogger) TaskStart(ctx TaskContext) {
	fmt.Fprintf(l.w, "开始执行：%s\n", ctx.TaskName)
}

func (l *TextLogger) ActionResult(result ActionResult) {
	fmt.Fprintf(
		l.w,
		"组件=%s 主机=%s 动作=%s 目标=%s 结果=%s 耗时=%s\n",
		result.Context.Component,
		result.Context.HostName,
		result.Action,
		result.Target,
		result.Status,
		formatDuration(result.Duration),
	)
}

func formatDuration(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Millisecond).String()
}
