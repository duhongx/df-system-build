package opsprobe

import (
	"context"
	"df-build-server/internal/inspection"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func archiveDirectory(command string) string {
	// Recognise local file-copy destinations only. Never execute archive_command,
	// guess a remote backup path, or infer a directory from an arbitrary script.
	if !regexp.MustCompile(`(?:^|[;&|\s])(?:/\S+/)?(?:cp|install|rsync)\s`).MatchString(command) {
		return ""
	}
	match := regexp.MustCompile(`(?:^|\s)["']?(/[^\s"';|&]*%f)["']?(?:\s|$)`).FindStringSubmatch(command)
	if len(match) < 2 || strings.Contains(match[1], ":") || strings.Contains(match[1], "$") {
		return ""
	}
	return filepath.Dir(match[1])
}

func inspectFileInventory(ctx context.Context, dir, code, name string, days int) inspection.Observation {
	o := inspection.Observation{Code: code, Name: name, Status: "RECORDED", CollectedAt: time.Now(), Source: "本机实际目录元数据：" + dir}
	if !filepath.IsAbs(dir) {
		o.Status = "UNKNOWN"
		o.Reason = "未定位到实际绝对目录"
		return o
	}
	type file struct {
		path string
		size int64
		at   time.Time
	}
	var files []file
	var total int64
	old := 0
	var walk func(string, int) error
	walk = func(base string, depth int) error {
		f, err := os.Open(base)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			entries, err := f.ReadDir(256)
			for _, e := range entries {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if len(files) >= 20000 {
					return fmt.Errorf("文件超过 20000 个，本轮未取得完整目录")
				}
				name := filepath.Join(base, e.Name())
				if e.IsDir() {
					if depth < 2 {
						if err := walk(name, depth+1); err != nil {
							return err
						}
					}
					continue
				}
				if !e.Type().IsRegular() {
					continue
				}
				info, err := e.Info()
				if err != nil {
					return err
				}
				files = append(files, file{name, info.Size(), info.ModTime()})
				total += info.Size()
				if days > 0 && time.Since(info.ModTime()) > time.Duration(days)*24*time.Hour {
					old++
				}
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	err := walk(dir, 0)
	sort.Slice(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
	o.Value = fmt.Sprintf("目录 %s，文件 %d 个，总量 %.2f GiB", dir, len(files), float64(total)/(1<<30))
	if len(files) > 0 {
		o.Value += "；最近文件 " + files[0].at.Format(time.RFC3339)
	}
	t := inspection.Table{Title: name, Columns: []string{"文件", "修改时间", "大小 MiB"}}
	for _, f := range files {
		t.Rows = append(t.Rows, []string{f.path, f.at.Format(time.RFC3339), fmt.Sprintf("%.2f", float64(f.size)/(1<<20))})
	}
	o.Tables = []inspection.Table{t}
	if days > 0 {
		o.Value += fmt.Sprintf("；修改时间超过 %d 天的文件 %d 个", days, old)
		o.Threshold = fmt.Sprintf("按指导书核对文件修改时间，超过 %d 天需关注；仅列事实，不删除文件。", days)
		if old > 0 {
			o.Status = "WARNING"
		}
	}
	if len(files) == 0 && code == "cloudhis-4" {
		o.Status = "WARNING"
		o.Reason = "本次读取的备份目录没有备份文件"
	}
	if err != nil {
		o.Status = "UNKNOWN"
		o.Reason = inspection.Redact(err.Error())
		o.Gaps = []string{"目录未完整取得：" + o.Reason}
	}
	return o
}

func nativeArchiveFiles(ctx context.Context, m ManualOptions, f nativePostgreSQLFacts, err error) inspection.Observation {
	if err != nil {
		return inspection.Observation{Code: "cloudhis-19", Name: "PostgreSQL 归档目录", Status: "UNKNOWN", Reason: "未能读取归档配置，无法定位文件目录：" + err.Error()}
	}
	dir := m.ArchiveDirectory
	if dir == "" {
		dir = archiveDirectory(f.ArchiveCommand)
	}
	if dir != "" {
		return inspectFileInventory(ctx, dir, "cloudhis-19", "PostgreSQL 归档文件", 7)
	}
	o := inspection.Observation{Code: "cloudhis-19", Name: "PostgreSQL 归档目录", CollectedAt: time.Now(), Source: "本机 PostgreSQL 实际归档配置"}
	if f.ArchiveMode == "off" || !f.ArchiveConfigured {
		o.Status = "NOT_APPLICABLE"
		o.Value = "未开启有效归档，不检查归档文件保留"
		o.Reason = "archive_mode 关闭或 archive_command 未配置；配置事实见归档状态项"
	} else if strings.Contains(f.ArchiveCommand, "pgbackrest") {
		o.Status = "NOT_APPLICABLE"
		o.Value = "归档由 pgBackRest 管理"
		o.Reason = "不适用本地平铺目录的七天文件保留规则；备份与工具状态单独记录"
	} else {
		o.Status = "UNKNOWN"
		o.Value = "归档已配置，但尚未定位本机文件目录"
		o.Reason = "当前归档命令不是可安全识别的本地复制形式；需在连接中补充实际目录或确认归档方式"
	}
	return o
}
