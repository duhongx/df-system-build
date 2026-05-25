package repository

import (
	"df-build-server/internal/model"

	"gorm.io/gorm"
)

// DeployPlanRepo handles deploy plan CRUD
type DeployPlanRepo struct{ db *gorm.DB }

func NewDeployPlanRepo() *DeployPlanRepo { return &DeployPlanRepo{db: DB} }

func (r *DeployPlanRepo) Create(plan *model.DeployPlan) error { return r.db.Create(plan).Error }
func (r *DeployPlanRepo) Update(plan *model.DeployPlan) error { return r.db.Save(plan).Error }
func (r *DeployPlanRepo) FindByID(id uint) (*model.DeployPlan, error) {
	var plan model.DeployPlan
	err := r.db.First(&plan, id).Error
	return &plan, err
}
func (r *DeployPlanRepo) GetLatest() (*model.DeployPlan, error) {
	var plan model.DeployPlan
	err := r.db.Order("id DESC").First(&plan).Error
	return &plan, err
}

func (r *DeployPlanRepo) UpdateComponentStatus(executionID uint, component, status, errMsg string) {
	// Update component status in execution record (simplified - in production use JSON patch)
}

// DeployLogRepo handles deploy logs
type DeployLogRepo struct{ db *gorm.DB }

func NewDeployLogRepo() *DeployLogRepo { return &DeployLogRepo{db: DB} }

func (r *DeployLogRepo) AppendLog(executionID uint, component, content string) {
	r.db.Create(&model.DeployLog{ExecutionID: executionID, Component: component, Content: content})
}

func (r *DeployLogRepo) GetLogs(executionID uint, component string) ([]model.DeployLog, error) {
	var logs []model.DeployLog
	q := r.db.Where("execution_id = ?", executionID)
	if component != "" {
		q = q.Where("component = ?", component)
	}
	err := q.Order("id ASC").Find(&logs).Error
	return logs, err
}

// DeployExecutionRepo handles execution records
type DeployExecutionRepo struct{ db *gorm.DB }

func NewDeployExecutionRepo() *DeployExecutionRepo { return &DeployExecutionRepo{db: DB} }

func (r *DeployExecutionRepo) Create(exec *model.DeployExecution) error { return r.db.Create(exec).Error }
func (r *DeployExecutionRepo) Update(exec *model.DeployExecution) error { return r.db.Save(exec).Error }
func (r *DeployExecutionRepo) FindByID(id uint) (*model.DeployExecution, error) {
	var exec model.DeployExecution
	err := r.db.First(&exec, id).Error
	return &exec, err
}
