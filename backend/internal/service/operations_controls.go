package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"df-build-server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func OperationsJSON(value any) string { return mustJSON(value) }
func (s *OperationsService) HostCollecting(id uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collecting[id]
}
func (s *OperationsService) ValidateInspection(req InspectionRequest) error {
	return s.db.Transaction(func(tx *gorm.DB) error { _, err := s.createInspectionTx(tx, req, "", 0, "", true); return err })
}
func (s *OperationsService) ReconcileInspection(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var run model.InspectionRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, id).Error; err != nil {
			return err
		}
		if run.Status == "RUNNING" || run.Status == "QUEUED" {
			return fmt.Errorf("任务仍在处理，请等待结束")
		}
		var count int64
		if err := tx.Model(&model.InspectionTargetRun{}).Where("run_id=? AND status='UNKNOWN' AND remote_directory<>''", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("没有可核实的远端任务")
		}
		s.mu.Lock()
		rendering := s.active[id]
		s.mu.Unlock()
		if rendering {
			return fmt.Errorf("报告正在生成，请稍后核实")
		}
		return tx.Model(&run).Updates(map[string]any{"status": "QUEUED", "message": "等待核实原远端任务，不重复下发", "finished_at": nil, "html_status": "PENDING", "pdf_status": "PENDING", "report_snapshot_json": nil}).Error
	})
}
func (s *OperationsService) RegenerateReportsAsync(id uint) error {
	claimed := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var run model.InspectionRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, id).Error; err != nil {
			return err
		}
		if run.Status == "QUEUED" || run.Status == "RUNNING" {
			return fmt.Errorf("巡检仍在执行，请等待结果回收")
		}
		s.mu.Lock()
		if s.active[id] {
			s.mu.Unlock()
			return fmt.Errorf("报告正在生成，请稍后查看")
		}
		s.active[id] = true
		claimed = true
		s.mu.Unlock()
		return tx.Model(&run).Updates(map[string]any{"html_status": "PENDING", "pdf_status": "PENDING", "report_error": "", "message": "报告重新生成已接受"}).Error
	})
	if err != nil {
		if claimed {
			s.mu.Lock()
			delete(s.active, id)
			s.mu.Unlock()
		}
		return err
	}
	go func() {
		defer func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if s.GenerateInspectionReports(ctx, id) == nil {
			s.db.Model(&model.InspectionRun{}).Where("id=? AND status='PARTIAL' AND error_count=0 AND cancel_requested=false", id).Update("status", "COMPLETED")
		}
	}()
	return nil
}
func (s *OperationsService) DeleteInspection(id uint) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var run model.InspectionRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, id).Error; err != nil {
			return err
		}
		if run.Status == "QUEUED" || run.Status == "RUNNING" {
			return fmt.Errorf("进行中的巡检任务不能删除")
		}
		s.mu.Lock()
		rendering := s.active[id]
		s.mu.Unlock()
		if rendering {
			return fmt.Errorf("报告正在生成，不能删除")
		}
		if err := tx.Where("run_id=?", id).Delete(&model.InspectionTargetRun{}).Error; err != nil {
			return err
		}
		return tx.Delete(&run).Error
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.workspace, "reports", fmt.Sprintf("run-%d", id)))
}
