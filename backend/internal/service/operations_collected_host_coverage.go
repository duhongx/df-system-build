package service

import (
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"encoding/json"
)

// Only facts collected for this run and this exact host can replace its
// component checks. A different registered Kubernetes cluster is never enough.
func (s *OperationsService) applyCollectedHostCoverage(runID, serverID uint, opt *opsprobe.ManualOptions) {
	opt.ConfiguredComponents = nil
	var rows []model.InspectionTargetRun
	if s.db.Where("run_id=? AND type IN ? AND status='COMPLETED'", runID, []string{"database", "component"}).Find(&rows).Error != nil {
		return
	}
	for _, row := range rows {
		var r opsprobe.JobResult
		if json.Unmarshal([]byte(row.ResultJSON), &r) != nil {
			continue
		}
		for _, h := range r.RelatedHosts {
			if h.ServerID == serverID && h.Result != nil && h.Result.Error == "" {
				kind := r.Type
				if kind == "database" {
					kind = "postgresql"
				}
				opt.ConfiguredComponents = append(opt.ConfiguredComponents, kind)
				break
			}
		}
	}
}
