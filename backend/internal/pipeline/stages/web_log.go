package stages

import (
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"df-build-server/internal/pipeline/types"
)

type webLog func(string, ...any)

// Pod tar diagnostics are read only after StreamWithContext returns.
type webDiagnosticBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *webDiagnosticBuffer) Len() int { return b.buffer.Len() }

func (b *webDiagnosticBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 64*1024 - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (b *webDiagnosticBuffer) String() string {
	if b.truncated {
		return b.buffer.String() + " [诊断输出超过 64 KiB，已截断]"
	}
	return b.buffer.String()
}

func webLogger(pCtx *types.PipelineContext) webLog {
	return func(format string, args ...any) {
		if pCtx != nil && pCtx.OnLog != nil {
			pCtx.OnLog(pCtx.PipelineID, 0, "[前端更新] "+fmt.Sprintf(format, args...), "stdout")
		}
	}
}

func (log webLog) printf(format string, args ...any) {
	if log != nil {
		log(format, args...)
	}
}

// Log the operation before touching files, and only report completion on success.
func webFileStep(log webLog, action, source, target string, run func() error) error {
	log.printf("开始%s；来源=%s；目标=%s", action, source, target)
	started := time.Now()
	if err := run(); err != nil {
		wrapped := fmt.Errorf("%s失败（来源=%s；目标=%s）: %w", action, source, target, err)
		log.printf("%s", wrapped)
		return wrapped
	}
	log.printf("%s完成；目标=%s；耗时=%s", action, target, time.Since(started).Round(time.Millisecond))
	return nil
}

func logWebTree(log webLog, label, root string) {
	var files, bytes int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	if err != nil {
		log.printf("%s目录统计未完成；路径=%s；原因=%v", label, root, err)
		return
	}
	log.printf("%s；路径=%s；文件数=%d；总大小=%d 字节", label, root, files, bytes)
}
