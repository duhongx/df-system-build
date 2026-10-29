package opsprobe

import (
	"df-build-server/internal/inspection"
	"fmt"
	"time"
)

func FinalizeInspection(r *JobResult, kind string) {
	r.ExpectedChecks = inspection.RequiredCodes(kind)
	seen := map[string]bool{}
	for _, o := range r.ReportChecks {
		seen[o.Code] = true
	}
	for _, c := range r.Checks {
		if seen[c.Code] {
			continue
		}
		if c.Code == "host_identity" || c.Code == "host_cpu" || c.Code == "host_memory" {
			r.ReportChecks = append(r.ReportChecks, inspection.Observation{Code: c.Code, Name: c.Name, Status: c.Status, Value: c.Value, Reason: c.Error, Evidence: c.Evidence, Advice: c.Advice, CollectedAt: r.FinishedAt})
			seen[c.Code] = true
		}
	}
	if kind == "host" && r.Metrics != nil && !seen["host_filesystems"] {
		o := inspection.Observation{Code: "host_filesystems", Name: "实际文件系统容量与 inode", Status: "NORMAL", CollectedAt: r.Metrics.CollectedAt, Source: "本机文件系统只读采样"}
		count, bad := 0, 0
		for _, d := range r.Metrics.Disks {
			if !ReportableMountpoint(d.Mountpoint) {
				continue
			}
			count++
			if d.UsedPercent >= 80 || d.InodeUsedPercent >= 80 {
				bad++
				if d.UsedPercent >= 90 || d.InodeUsedPercent >= 90 {
					o.Status = "CRITICAL"
				} else if o.Status != "CRITICAL" {
					o.Status = "WARNING"
				}
			}
		}
		o.Value = fmt.Sprintf("实际文件系统 %d 个，容量或 inode 达到提醒阈值 %d 个", count, bad)
		if count == 0 {
			o.Status = "UNKNOWN"
			o.Reason = "未取得实际文件系统信息"
		}
		r.ReportChecks = append(r.ReportChecks, o)
	}
	r.ReportChecks = inspection.EnsureResults(r.ExpectedChecks, r.ReportChecks, r.Error)
	for i := range r.ReportChecks {
		if r.ReportChecks[i].CollectedAt.IsZero() {
			r.ReportChecks[i].CollectedAt = time.Now()
		}
	}
}
