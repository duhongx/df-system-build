package repository

import "df-build-server/internal/model"

type NotificationRepo struct{}

func NewNotificationRepo() *NotificationRepo { return &NotificationRepo{} }

func (r *NotificationRepo) List() ([]model.NotificationWebhook, error) {
	var list []model.NotificationWebhook
	err := DB.Order("id ASC").Find(&list).Error
	return list, err
}

func (r *NotificationRepo) FindByID(id uint) (*model.NotificationWebhook, error) {
	var n model.NotificationWebhook
	err := DB.First(&n, id).Error
	return &n, err
}

func (r *NotificationRepo) FindEnabled() ([]model.NotificationWebhook, error) {
	var list []model.NotificationWebhook
	err := DB.Where("enabled = ?", true).Find(&list).Error
	return list, err
}

func (r *NotificationRepo) Create(n *model.NotificationWebhook) error { return DB.Create(n).Error }

func (r *NotificationRepo) Update(n *model.NotificationWebhook) error { return DB.Save(n).Error }

func (r *NotificationRepo) Delete(id uint) error {
	return DB.Delete(&model.NotificationWebhook{}, id).Error
}
