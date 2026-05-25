package repository

import (
	"time"

	"df-build-server/internal/model"
)

type StageRepo struct{}

func NewStageRepo() *StageRepo { return &StageRepo{} }

func (r *StageRepo) Create(stage *model.PipelineStage) error {
	return DB.Create(stage).Error
}

func (r *StageRepo) Update(stage *model.PipelineStage) error {
	return DB.Save(stage).Error
}

func (r *StageRepo) FindByPipelineID(pipelineID uint) ([]model.PipelineStage, error) {
	var stages []model.PipelineStage
	err := DB.Where("pipeline_id = ?", pipelineID).Order("stage_order ASC").Find(&stages).Error
	return stages, err
}

func (r *StageRepo) FindByID(id uint) (*model.PipelineStage, error) {
	var stage model.PipelineStage
	err := DB.First(&stage, id).Error
	return &stage, err
}

type StageLogRepo struct{}

func NewStageLogRepo() *StageLogRepo { return &StageLogRepo{} }

func (r *StageLogRepo) AppendLog(pipelineID, stageID uint, content, stream string) {
	var maxLine int
	DB.Model(&model.StageLog{}).Where("stage_id = ?", stageID).Select("COALESCE(MAX(line_number), 0)").Scan(&maxLine)

	log := &model.StageLog{
		PipelineID: pipelineID,
		StageID:    stageID,
		LineNumber: maxLine + 1,
		Content:    content,
		Stream:     stream,
		Timestamp:  time.Now(),
	}
	DB.Create(log)
}

func (r *StageLogRepo) GetByStageID(stageID uint) ([]model.StageLog, error) {
	var logs []model.StageLog
	err := DB.Where("stage_id = ?", stageID).Order("line_number ASC").Find(&logs).Error
	return logs, err
}

func (r *StageLogRepo) DeleteByPipelineID(pipelineID uint) error {
	return DB.Where("pipeline_id = ?", pipelineID).Delete(&model.StageLog{}).Error
}

func (r *StageLogRepo) DeleteOlderThan(before time.Time) (int64, error) {
	result := DB.Where("timestamp < ?", before).Delete(&model.StageLog{})
	return result.RowsAffected, result.Error
}
