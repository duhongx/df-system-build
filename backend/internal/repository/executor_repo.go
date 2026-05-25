package repository

import "df-build-server/internal/model"

type ExecutorRepo struct{}

func NewExecutorRepo() *ExecutorRepo { return &ExecutorRepo{} }

func (r *ExecutorRepo) List() ([]model.Executor, error) {
	var list []model.Executor
	err := DB.Order("id ASC").Find(&list).Error
	return list, err
}

func (r *ExecutorRepo) FindByID(id uint) (*model.Executor, error) {
	var e model.Executor
	err := DB.First(&e, id).Error
	return &e, err
}

func (r *ExecutorRepo) Create(e *model.Executor) error { return DB.Create(e).Error }

func (r *ExecutorRepo) Update(e *model.Executor) error { return DB.Save(e).Error }

func (r *ExecutorRepo) Delete(id uint) error { return DB.Delete(&model.Executor{}, id).Error }

func (r *ExecutorRepo) ExistsByName(name string) bool {
	var count int64
	DB.Model(&model.Executor{}).Where("name = ?", name).Count(&count)
	return count > 0
}
