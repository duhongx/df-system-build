package repository

import (
	"df-build-server/internal/model"
	"fmt"

	"gorm.io/gorm"
)

type ServerMgmtRepo struct {
	db *gorm.DB
}

func NewServerMgmtRepo() *ServerMgmtRepo {
	return &ServerMgmtRepo{db: DB}
}

func (r *ServerMgmtRepo) List(search string) ([]model.Server, error) {
	var servers []model.Server
	q := r.db.Order("sort_order ASC, id ASC")
	if search != "" {
		q = q.Where("host LIKE ? OR remark LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	err := q.Find(&servers).Error
	return servers, err
}

func (r *ServerMgmtRepo) FindByID(id uint) (*model.Server, error) {
	var server model.Server
	err := r.db.First(&server, id).Error
	return &server, err
}

func (r *ServerMgmtRepo) Create(server *model.Server) error {
	return r.db.Create(server).Error
}

func (r *ServerMgmtRepo) Update(server *model.Server) error {
	return r.db.Save(server).Error
}

func (r *ServerMgmtRepo) Delete(id uint) error {
	var linked int64
	if err := r.db.Model(&model.OperationsDatabase{}).Where("server_id=?", id).Count(&linked).Error; err != nil {
		return err
	}
	if linked > 0 {
		return fmt.Errorf("主机仍关联数据库诊断连接，请先解除关联")
	}
	if err := r.db.Model(&model.InspectionTargetRun{}).Joins("JOIN inspection_runs ON inspection_runs.id=inspection_target_runs.run_id").Where("inspection_target_runs.server_id=? AND inspection_runs.status IN ?", id, []string{"QUEUED", "RUNNING"}).Count(&linked).Error; err != nil {
		return err
	}
	if linked > 0 {
		return fmt.Errorf("主机仍有进行中的巡检，请等待结束")
	}
	return r.db.Delete(&model.Server{}, id).Error
}

// --- Server Logs ---

type ServerLogRepo struct {
	db *gorm.DB
}

func NewServerLogRepo() *ServerLogRepo {
	return &ServerLogRepo{db: DB}
}

func (r *ServerLogRepo) Create(log *model.ServerLog) error {
	return r.db.Create(log).Error
}

func (r *ServerLogRepo) ListByServer(serverID uint, logType string, page, pageSize int) ([]model.ServerLog, int64, error) {
	var logs []model.ServerLog
	var total int64

	q := r.db.Where("server_id = ?", serverID)
	if logType != "" {
		q = q.Where("type = ?", logType)
	}

	q.Model(&model.ServerLog{}).Count(&total)
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error
	return logs, total, err
}
