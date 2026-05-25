package repository

import "df-build-server/internal/model"

type NotificationMsgRepo struct{}

func NewNotificationMsgRepo() *NotificationMsgRepo { return &NotificationMsgRepo{} }

func (r *NotificationMsgRepo) Create(msg *model.NotificationMsg) error {
	return DB.Create(msg).Error
}

func (r *NotificationMsgRepo) List(page, pageSize int) ([]model.NotificationMsg, int64, error) {
	var list []model.NotificationMsg
	var total int64
	DB.Model(&model.NotificationMsg{}).Count(&total)
	err := DB.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

func (r *NotificationMsgRepo) UnreadCount() int64 {
	var count int64
	DB.Model(&model.NotificationMsg{}).Where("read = ?", false).Count(&count)
	return count
}

func (r *NotificationMsgRepo) MarkRead(id uint) error {
	return DB.Model(&model.NotificationMsg{}).Where("id = ?", id).Update("read", true).Error
}

func (r *NotificationMsgRepo) MarkAllRead() error {
	return DB.Model(&model.NotificationMsg{}).Where("read = ?", false).Update("read", true).Error
}
