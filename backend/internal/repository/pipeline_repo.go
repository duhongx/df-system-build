package repository

import (
	"fmt"

	"df-build-server/internal/model"

	"gorm.io/gorm"
)

type PipelineRepo struct{}

func NewPipelineRepo() *PipelineRepo { return &PipelineRepo{} }

func (r *PipelineRepo) Create(p *model.Pipeline) error {
	return DB.Create(p).Error
}

func (r *PipelineRepo) Update(p *model.Pipeline) error {
	return DB.Save(p).Error
}

func (r *PipelineRepo) FindByID(id uint) (*model.Pipeline, error) {
	var p model.Pipeline
	err := DB.Preload("Stages", func(db *gorm.DB) *gorm.DB {
		return db.Order("stage_order ASC")
	}).First(&p, id).Error
	return &p, err
}

type PipelineListParams struct {
	Page     int
	PageSize int
	AppName  string
	Status   string
}

func (r *PipelineRepo) List(params PipelineListParams) ([]model.Pipeline, int64, error) {
	var list []model.Pipeline
	var total int64

	query := DB.Model(&model.Pipeline{})
	if params.AppName != "" {
		query = query.Where("app_name = ?", params.AppName)
	}
	if params.Status != "" {
		query = query.Where("status = ?", params.Status)
	}

	query.Count(&total)

	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.Page <= 0 {
		params.Page = 1
	}
	offset := (params.Page - 1) * params.PageSize

	err := DB.Order("id DESC").Offset(offset).Limit(params.PageSize).Find(&list).Error
	return list, total, err
}

func (r *PipelineRepo) GetRunning() ([]model.Pipeline, error) {
	var list []model.Pipeline
	err := DB.Preload("Stages").Where("status = ?", "RUNNING").Order("start_time ASC").Find(&list).Error
	return list, err
}

func (r *PipelineRepo) GetPending() ([]model.Pipeline, error) {
	var list []model.Pipeline
	err := DB.Where("status = ?", "PENDING").Order("created_at ASC").Find(&list).Error
	return list, err
}

func (r *PipelineRepo) CountRunning() int64 {
	var count int64
	DB.Model(&model.Pipeline{}).Where("status = ?", "RUNNING").Count(&count)
	return count
}

func (r *PipelineRepo) GenerateNo(appName string) string {
	var count int64
	DB.Model(&model.Pipeline{}).Where("app_name = ?", appName).Count(&count)
	return fmt.Sprintf("%s-%04d", appName, count+1)
}

func (r *PipelineRepo) UpdateStatus(id uint, status string) error {
	return DB.Model(&model.Pipeline{}).Where("id = ?", id).Update("status", status).Error
}

// CancelOldImageReady cancels all IMAGE_READY pipelines for a given app except the current one
func (r *PipelineRepo) CancelOldImageReady(applicationID uint, currentPipelineID uint) {
	DB.Model(&model.Pipeline{}).
		Where("application_id = ? AND status = ? AND id != ?", applicationID, "IMAGE_READY", currentPipelineID).
		Update("status", "CANCELED")
}
