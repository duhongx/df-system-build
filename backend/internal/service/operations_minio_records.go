package service

import (
	"context"
	"df-build-server/internal/inspection"
	"df-build-server/internal/opsremote"
	"fmt"
	"path"
	"sort"
	"time"
)

func minioDailyObservation(data string) inspection.Observation {
	o := inspection.Observation{Code: "cloudhis-39", Name: "MinIO 每日记录", Status: "RECORDED", CollectedAt: time.Now(), Source: "客户已有逐日记录文件", Evidence: data, Advice: "inode 净变化不等于业务对象新增数量；不推算缺失日期。"}
	// Reuse the report parser so both entry points retain midnight timestamps,
	// duplicate conflicts and gaps with exactly the same meaning.
	o.Executions = []inspection.Execution{{}, {Stdout: data}}
	enrichDailyRecords(&o)
	o.Executions = nil
	o.Value = "未取得有效的逐日记录"
	if len(o.Tables) > 0 {
		t := o.Tables[len(o.Tables)-1]
		o.Value = fmt.Sprintf("取得 %d 天已有记录", len(t.Rows))
		if len(t.Rows) > 0 {
			last := t.Rows[len(t.Rows)-1]
			o.Value += "；最近日期 " + last[0] + "，inode " + last[1] + "，净变化 " + last[2]
		}
	}

	return o
}

func remoteBackupInventory(ctx context.Context, conn *opsremote.Conn, dir string) inspection.Observation {
	o := inspection.Observation{Code: "cloudhis-5", Name: "MinIO 备份", Status: "RECORDED", CollectedAt: time.Now(), Source: "备份目录实际文件元数据：" + dir}
	type file struct {
		name string
		size int64
		at   time.Time
	}
	var files []file
	var total int64
	var walk func(string, int) error
	walk = func(base string, depth int) error {
		entries, err := conn.SFTP.ReadDir(base)
		if err != nil {
			return err
		}
		for _, f := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if len(files) >= 20000 {
				return fmt.Errorf("文件超过 20000 个，目录结果未取得完整")
			}
			if f.IsDir() {
				if depth < 2 {
					if err := walk(path.Join(base, f.Name()), depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if !f.Mode().IsRegular() {
				continue
			}
			files = append(files, file{path.Join(base, f.Name()), f.Size(), f.ModTime()})
			total += f.Size()
		}
		return nil
	}
	err := walk(dir, 0)
	sort.Slice(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
	o.Value = fmt.Sprintf("已取得 %d 个备份文件，总量 %.2f GiB", len(files), float64(total)/(1<<30))
	if len(files) > 0 {
		o.Value += "；最近文件 " + files[0].at.Format(time.RFC3339)
	} else {
		o.Status = "WARNING"
		o.Reason = "登记的备份目录中没有备份文件"
	}
	t := inspection.Table{Columns: []string{"文件", "修改时间", "大小 MiB"}}
	for _, f := range files {
		t.Rows = append(t.Rows, []string{f.name, f.at.Format(time.RFC3339), fmt.Sprintf("%.2f", float64(f.size)/(1<<20))})
	}
	o.Tables = []inspection.Table{t}
	if err != nil {
		o.Status = "UNKNOWN"
		o.Reason = "备份目录未完整读取：" + err.Error()
	}
	return o
}
