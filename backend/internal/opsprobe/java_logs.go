package opsprobe

import (
	"context"
	"df-build-server/internal/inspection"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func javaInspectionDirectory(args []string, cwd, stdout string) []string {
	set := map[string]bool{}
	add := func(dir string) {
		if dir == "" {
			return
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		if filepath.IsAbs(dir) && !strings.ContainsAny(dir, "\x00\r\n") {
			set[filepath.Clean(dir)] = true
		}
	}
	for _, a := range args {
		for _, prefix := range []string{"-Dlogging.file.path=", "--logging.file.path=", "-XX:HeapDumpPath="} {
			if strings.HasPrefix(a, prefix) {
				v := strings.TrimPrefix(a, prefix)
				if strings.HasSuffix(v, ".hprof") {
					v = filepath.Dir(v)
				}
				add(v)
			}
		}
		for _, prefix := range []string{"-Dlogging.file.name=", "--logging.file.name="} {
			if strings.HasPrefix(a, prefix) {
				add(filepath.Dir(strings.TrimPrefix(a, prefix)))
			}
		}
	}
	if filepath.IsAbs(stdout) && !strings.HasPrefix(stdout, "/dev/") {
		add(filepath.Dir(stdout))
	}
	for _, dir := range []string{filepath.Join(cwd, "logs"), filepath.Join(cwd, "log")} {
		if filepath.IsAbs(dir) {
			set[dir] = true
		}
	}
	var dirs []string
	for d := range set {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

func inspectJavaLogs(ctx context.Context) []inspection.Observation {
	logs := inspection.Observation{Code: "cloudhis-11", Name: "Java 服务日志大小", Status: "RECORDED", CollectedAt: time.Now(), Source: "实际 Java 进程参数、工作目录、标准输出及文件元数据"}
	heap := inspection.Observation{Code: "cloudhis-12", Name: "Java 内存溢出线索", Status: "RECORDED", CollectedAt: time.Now(), Source: logs.Source}
	logTable := inspection.Table{Columns: []string{"进程", "日志目录", "文件数", "大小 GiB"}}
	heapTable := inspection.Table{Columns: []string{"进程", "hprof 文件", "修改时间", "大小 MiB"}}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		logs.Status, heap.Status = "UNKNOWN", "UNKNOWN"
		logs.Reason = err.Error()
		heap.Reason = err.Error()
		return []inspection.Observation{logs, heap}
	}
	apps, console := 0, 0
	seen := map[string]bool{}
	for _, e := range entries {
		if ctx.Err() != nil {
			logs.Gaps = append(logs.Gaps, "检查已中断")
			heap.Gaps = append(heap.Gaps, "检查已中断")
			break
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		base := filepath.Join("/proc", e.Name())
		data, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
		if len(args) == 0 || !strings.HasPrefix(filepath.Base(args[0]), "java") {
			continue
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "org.elasticsearch.bootstrap.Elasticsearch") || strings.Contains(joined, "org.apache.skywalking.oap.server") || strings.Contains(joined, "skywalking-webapp") || inspectProcess(e.Name(), args, "").Home != "" {
			continue
		}
		apps++
		cwd, _ := os.Readlink(filepath.Join(base, "cwd"))
		stdout, _ := os.Readlink(filepath.Join(base, "fd/1"))
		dirs := javaInspectionDirectory(args, cwd, stdout)
		found := false
		for _, dir := range dirs {
			actual := filepath.Join(base, "root", dir)
			st, err := os.Stat(actual)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				logs.Gaps = append(logs.Gaps, fmt.Sprintf("进程 %s 的目录 %s 无法读取", e.Name(), dir))
				continue
			}
			if !st.IsDir() {
				continue
			}
			found = true
			identity := actual
			if stat, ok := st.Sys().(*syscall.Stat_t); ok {
				identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
			}
			if seen[identity] {
				continue
			}
			seen[identity] = true
			var count int
			var bytes int64
			walkErr := filepath.WalkDir(actual, func(name string, item os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				rel, _ := filepath.Rel(actual, name)
				if item.IsDir() && strings.Count(rel, string(os.PathSeparator)) >= 2 {
					return filepath.SkipDir
				}
				if item.IsDir() || !item.Type().IsRegular() {
					return nil
				}
				count++
				if count > 20000 {
					return fmt.Errorf("目录文件超过 20000 个")
				}
				info, err := item.Info()
				if err != nil {
					return err
				}
				bytes += info.Size()
				if strings.HasSuffix(strings.ToLower(name), ".hprof") {
					heapTable.Rows = append(heapTable.Rows, []string{e.Name(), filepath.Join(dir, rel), info.ModTime().Format(time.RFC3339), fmt.Sprintf("%.1f", float64(info.Size())/(1<<20))})
				}
				return nil
			})
			if walkErr != nil {
				logs.Gaps = append(logs.Gaps, fmt.Sprintf("目录 %s 检查不完整：%v", dir, walkErr))
				heap.Gaps = append(heap.Gaps, "hprof 范围未完整读取："+dir)
			}
			logTable.Rows = append(logTable.Rows, []string{e.Name(), dir, fmt.Sprint(count), fmt.Sprintf("%.2f", float64(bytes)/(1<<30))})
		}
		if !found {
			console++
			logs.Reason = "部分进程没有独立文件日志目录，可能由标准输出或容器日志驱动接管；未将此情况当作目录读取失败"
		}
	}
	logs.Tables = []inspection.Table{logTable}
	heap.Tables = []inspection.Table{heapTable}
	logs.Value = fmt.Sprintf("发现 %d 个业务 Java 进程，定位 %d 个实际目录，另有 %d 个未发现独立文件日志目录", apps, len(logTable.Rows), console)
	heap.Value = fmt.Sprintf("在本次可定位的应用目录发现 %d 个 hprof 文件", len(heapTable.Rows))
	heap.Threshold = "hprof 仅表示内存转储线索，结合时间和应用日志核对，不直接认定当前应用故障。"
	if len(heapTable.Rows) > 0 {
		heap.Status = "WARNING"
	}
	if apps == 0 {
		logs.Status, heap.Status = "NOT_APPLICABLE", "NOT_APPLICABLE"
		logs.Reason = "本机未发现业务 Java 进程"
		heap.Reason = logs.Reason
	}
	if len(logs.Gaps) > 0 {
		logs.Status = "UNKNOWN"
	}
	if len(heap.Gaps) > 0 {
		heap.Status = "UNKNOWN"
	}
	return []inspection.Observation{logs, heap}
}
