package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
)

type InspectionCoverageIssue struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Object string `json:"object"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type InspectionCoverage struct {
	Summary          *inspection.Summary       `json:"summary"`
	Issues           []InspectionCoverageIssue `json:"issues"`
	Legacy           bool                      `json:"legacy"`
	ExecutionLabel   string                    `json:"executionLabel"`
	FailedTargets    int                       `json:"failedTargets"`
	UncertainTargets int                       `json:"uncertainTargets"`
}

type InspectionListSummary struct {
	Summary          *inspection.Summary
	UncertainTargets int
}

func InspectionExecutionLabel(status string, uncertainTargets int) string {
	if status == "QUEUED" {
		return "等待执行"
	}
	if status == "RUNNING" {
		return "正在检查"
	}
	if uncertainTargets > 0 || status == "UNKNOWN" {
		return "远端执行状态待核实"
	}
	if status == "CANCELED" {
		return "已取消"
	}
	if status == "FAILED" {
		return "执行失败"
	}
	return "检查已结束"
}

func (s *OperationsService) InspectionSummaries(ctx context.Context, ids []uint) (map[uint]InspectionListSummary, error) {
	out := map[uint]InspectionListSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID               uint
		Summary          string
		UncertainTargets int
	}
	if err := s.db.WithContext(ctx).Model(&model.InspectionRun{}).Select("id, COALESCE((report_snapshot_json->'summary')::text,'') AS summary, (SELECT count(*) FROM inspection_target_runs t WHERE t.run_id=inspection_runs.id AND t.status='UNKNOWN') AS uncertain_targets").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = InspectionListSummary{UncertainTargets: row.UncertainTargets}
		if row.Summary == "" || row.Summary == "null" {
			continue
		}
		var summary inspection.Summary
		if err := json.Unmarshal([]byte(row.Summary), &summary); err != nil {
			return nil, fmt.Errorf("检查统计读取失败: %w", err)
		}
		out[row.ID] = InspectionListSummary{Summary: &summary, UncertainTargets: row.UncertainTargets}
	}
	return out, nil
}

// Project only the summary and incomplete checks. Successful output, SQL text
// and result tables can be very large and are not needed to explain coverage.
const inspectionCoverageProjection = `SELECT jsonb_build_object(
 'summary', report_snapshot_json->'summary',
 'items', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
   'code',i->'code','name',i->'name','status',i->'status','reason',i->'reason',
   'evidence',(SELECT COALESCE(jsonb_agg(jsonb_build_object(
     'objectName',e->'objectName','status',e->'status','reason',e->'reason','gaps',e->'gaps',
     'executions',(SELECT COALESCE(jsonb_agg(jsonb_build_object(
       'exitCode',x->'exitCode','error',x->'error','stderr',CASE WHEN length(x->>'stderr')>2000 THEN left(x->>'stderr',2000)||'…（完整错误见对象详情）' ELSE x->>'stderr' END,'truncated',x->'truncated'
     )),'[]'::jsonb) FROM jsonb_array_elements(COALESCE(NULLIF(e->'executions','null'::jsonb),'[]'::jsonb)) x)
   )),'[]'::jsonb) FROM jsonb_array_elements(COALESCE(NULLIF(i->'evidence','null'::jsonb),'[]'::jsonb)) e)
 )),'[]'::jsonb) FROM jsonb_array_elements(COALESCE(NULLIF(report_snapshot_json->'items','null'::jsonb),'[]'::jsonb)) i
 WHERE i->>'complete'='false' OR i->>'status' IN ('UNKNOWN','NOT_RUN'))
) AS document FROM inspection_runs WHERE id=? AND report_snapshot_json IS NOT NULL`

func (s *OperationsService) InspectionCoverage(ctx context.Context, run model.InspectionRun, targets []model.InspectionTargetRun) (*InspectionCoverage, error) {
	out := &InspectionCoverage{Issues: []InspectionCoverageIssue{}}
	for _, target := range targets {
		if target.Status == "FAILED" {
			out.FailedTargets++
		}
		if target.Status == "UNKNOWN" {
			out.UncertainTargets++
		}
	}
	out.ExecutionLabel = InspectionExecutionLabel(run.Status, out.UncertainTargets)
	var row struct{ Document string }
	if err := s.db.WithContext(ctx).Raw(inspectionCoverageProjection, run.ID).Scan(&row).Error; err != nil {
		return nil, err
	}
	if row.Document != "" {
		var doc struct {
			Summary *inspection.Summary `json:"summary"`
			Items   []inspection.Item   `json:"items"`
		}
		if err := json.Unmarshal([]byte(row.Document), &doc); err != nil {
			return nil, fmt.Errorf("检查覆盖情况读取失败: %w", err)
		}
		out.Summary = doc.Summary
		for _, item := range doc.Items {
			added := false
			for _, evidence := range item.Evidence {
				if evidence.Status != "UNKNOWN" && len(evidence.Gaps) == 0 && item.Status != "UNKNOWN" {
					continue
				}
				reason := inspectionCoverageReason(evidence.Observation)
				if item.Reason != "" {
					reason = strings.TrimSpace(item.Reason + "；" + reason)
				}
				out.Issues = append(out.Issues, InspectionCoverageIssue{item.Code, item.Name, evidence.ObjectName, "UNKNOWN", reason})
				added = true
			}
			if !added {
				reason := item.Reason
				if reason == "" {
					reason = "未取得该检查项目的完整结果"
				}
				out.Issues = append(out.Issues, InspectionCoverageIssue{item.Code, item.Name, "", item.Status, reason})
			}
		}
	} else {
		out.Legacy = true
		// Older tasks have no frozen checklist. Read only unknown observations
		// from their saved results, without returning raw command output.
		var saved []struct {
			Name string
			Data string
		}
		if err := s.db.WithContext(ctx).Raw(`SELECT name,jsonb_build_object(
 'error',result_json->'error',
 'checks',(SELECT COALESCE(jsonb_agg(jsonb_build_object('code',c->'code','name',c->'name','reason',c->'error')),'[]'::jsonb)
 FROM jsonb_array_elements(COALESCE(NULLIF(result_json->'checks','null'::jsonb),'[]'::jsonb)) c WHERE c->>'status'='UNKNOWN')
 ) AS data FROM inspection_target_runs WHERE run_id=?`, run.ID).Scan(&saved).Error; err != nil {
			return nil, err
		}
		for _, row := range saved {
			var result struct {
				Error  string                   `json:"error"`
				Checks []inspection.Observation `json:"checks"`
			}
			if err := json.Unmarshal([]byte(row.Data), &result); err != nil {
				return nil, fmt.Errorf("历史检查结果读取失败: %w", err)
			}
			for _, check := range result.Checks {
				out.Issues = append(out.Issues, InspectionCoverageIssue{check.Code, check.Name, row.Name, "UNKNOWN", inspectionCoverageReason(check)})
			}
			if result.Error != "" && len(result.Checks) == 0 {
				out.Issues = append(out.Issues, InspectionCoverageIssue{"", "对象采集", row.Name, "UNKNOWN", inspection.Redact(result.Error)})
			}
		}
	}
	for _, target := range targets {
		if target.Status == "COMPLETED" || target.Status == "QUEUED" || target.Status == "RUNNING" {
			continue
		}
		found := false
		for _, issue := range out.Issues {
			if issue.Object == target.Name {
				found = true
				break
			}
		}
		if !found {
			reason := strings.TrimSpace(target.Message)
			if reason == "" {
				reason = "未回收到该对象的完整检查结果"
			}
			out.Issues = append(out.Issues, InspectionCoverageIssue{"", "对象采集", target.Name, "UNKNOWN", inspection.Redact(reason)})
		}
	}
	return out, nil
}

func inspectionCoverageReason(observation inspection.Observation) string {
	parts := []string{}
	add := func(text string) {
		text = strings.TrimSpace(inspection.Redact(text))
		if text == "" {
			return
		}
		for _, prior := range parts {
			if prior == text {
				return
			}
		}
		parts = append(parts, text)
	}
	add(observation.Reason)
	for _, gap := range observation.Gaps {
		add(gap)
	}
	for _, entry := range observation.Executions {
		if entry.ExitCode == 0 && entry.Error == "" && !entry.Truncated {
			continue
		}
		add(entry.Stderr)
		add(entry.Error)
		if entry.Truncated {
			add("采集输出被截断，无法取得完整结果")
		}
	}
	if len(parts) == 0 {
		return "未取得完整结果，请查看对象检查详情"
	}
	return strings.Join(parts, "；")
}
