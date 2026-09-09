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

	err := query.Order("id DESC").Offset(offset).Limit(params.PageSize).Find(&list).Error
	return list, total, err
}

func (r *PipelineRepo) GetRunning() ([]model.Pipeline, error) {
	var list []model.Pipeline
	err := DB.Preload("Stages").Where("status = ?", "RUNNING").Order("start_time ASC").Find(&list).Error
	return list, err
}

func (r *PipelineRepo) GetPending() ([]model.Pipeline, error) {
	var list []model.Pipeline
	err := DB.Where("status IN ?", []string{"PENDING", "DEPLOYING"}).Order("created_at ASC").Find(&list).Error
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

func (r *PipelineRepo) StartRollout(id uint) (bool, error) {
	started := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Pipeline{}).Where("id = ? AND status = ?", id, "IMAGE_READY").Updates(map[string]any{"status": "DEPLOYING", "end_time": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err := tx.Model(&model.ArtifactDeployRecord{}).Where("pipeline_id = ?", id).Updates(map[string]any{"deploy_status": "queued", "status": "deploying"}).Error; err != nil {
			return err
		}
		started = true
		return nil
	})
	return started && err == nil, err
}

func (r *PipelineRepo) SaveImageCheckpoint(p *model.Pipeline) error {
	return DB.Model(&model.Pipeline{}).Where("id = ?", p.ID).Updates(map[string]any{
		"image_name": p.ImageName, "frontend_baseline_json": p.FrontendBaselineJSON,
	}).Error
}
