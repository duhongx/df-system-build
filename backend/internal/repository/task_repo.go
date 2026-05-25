package repository

import (
	"df-build-server/internal/model"
)

type TaskRepo struct{}

func NewTaskRepo() *TaskRepo { return &TaskRepo{} }

type TaskListParams struct {
	Page     int
	PageSize int
	Search   string
	AppType  string
}

func (r *TaskRepo) List(params TaskListParams) ([]model.Task, int64, error) {
	var tasks []model.Task
	var total int64

	// Build filtered base query
	query := DB.Model(&model.Task{})

	if params.Search != "" {
		query = query.Joins("LEFT JOIN applications ON applications.id = tasks.application_id").
			Where("tasks.task_name LIKE ? OR applications.app_name LIKE ?", "%"+params.Search+"%", "%"+params.Search+"%")
	}
	if params.AppType != "" {
		query = query.Joins("LEFT JOIN applications a2 ON a2.id = tasks.application_id").
			Where("a2.app_type = ?", params.AppType)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if params.PageSize <= 0 {
		params.PageSize = 50
	}
	if params.Page <= 0 {
		params.Page = 1
	}
	offset := (params.Page - 1) * params.PageSize

	// Use the SAME filtered query for the actual fetch
	err := query.Preload("Application").Preload("BuildConfig").
		Order("tasks.id DESC").Offset(offset).Limit(params.PageSize).Find(&tasks).Error
	return tasks, total, err
}

func (r *TaskRepo) FindByID(id uint) (*model.Task, error) {
	var task model.Task
	err := DB.Preload("Application").Preload("BuildConfig").First(&task, id).Error
	return &task, err
}

func (r *TaskRepo) Create(task *model.Task) error {
	return DB.Create(task).Error
}

func (r *TaskRepo) Update(task *model.Task) error {
	return DB.Save(task).Error
}

func (r *TaskRepo) Delete(id uint) error {
	return DB.Delete(&model.Task{}, id).Error
}

func (r *TaskRepo) ExistsByName(name string) bool {
	var count int64
	DB.Model(&model.Task{}).Where("task_name = ?", name).Count(&count)
	return count > 0
}

func (r *TaskRepo) UpdateLastRun(id uint, status string, durationSeconds int) error {
	return DB.Model(&model.Task{}).Where("id = ?", id).Updates(map[string]interface{}{
		"last_status":           status,
		"last_run_time":         DB.NowFunc(),
		"last_duration_seconds": durationSeconds,
	}).Error
}
