package service

import (
	"df-build-server/internal/inspection"
	"df-build-server/internal/opsprobe"
	"fmt"
	"strings"
)

func applyMonitoringFacts(r *opsprobe.JobResult) {
	if r == nil {
		return
	}
	if r.Metrics != nil {
		r.Checks = opsprobe.HostChecks(r.Metrics)
	}
	for i := range r.ReportChecks {
		o := &r.ReportChecks[i]
		if o.Attributes == nil {
			o.Attributes = map[string]string{}
		}
		delete(o.Attributes, "spaceExceeded")
		delete(o.Attributes, "spaceReason")
		switch o.Code {
		case "host_filesystems", "cloudhis-6", "cloudhis-15", "cloudhis-36":
			m := r.Metrics
			if o.Origin != nil {
				for _, h := range r.RelatedHosts {
					if h.ServerID == o.Origin.ID && h.Result != nil {
						m = h.Result.Metrics
					}
				}
			}
			if m != nil {
				var reasons []string
				for _, d := range m.Disks {
					if opsprobe.ReportableMountpoint(d.Mountpoint) {
						if d.UsedPercent > 75 {
							reasons = append(reasons, fmt.Sprintf("%s 容量 %.1f%%", d.Mountpoint, d.UsedPercent))
						}
						if d.Inodes > 0 && d.InodeUsedPercent > 75 {
							reasons = append(reasons, fmt.Sprintf("%s inode %.1f%%", d.Mountpoint, d.InodeUsedPercent))
						}
					}
				}
				if len(reasons) > 0 {
					o.Attributes["spaceExceeded"] = "true"
					o.Attributes["spaceReason"] = strings.Join(reasons, "；")
				}
			}
		}
		if maximum, ok := o.Measurements["maxDiskPercent"]; ok && maximum > 75 {
			o.Attributes["spaceExceeded"] = "true"
			o.Attributes["spaceReason"] = fmt.Sprintf("最高磁盘容量使用率 %.1f%%，超过 75%%", maximum)
		}
		o.Reason = reportDataReason(o.Reason)
		inspection.FactualObservation(o)
	}
	for i := range r.RelatedHosts {
		applyMonitoringFacts(r.RelatedHosts[i].Result)
	}
}
