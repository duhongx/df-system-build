package repository

import (
	"df-build-server/internal/model"
)

type UserRepo struct{}

func NewUserRepo() *UserRepo {
	return &UserRepo{}
}

func (r *UserRepo) FindByUsername(username string) (*model.User, error) {
	var user model.User
	err := DB.Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepo) FindByID(id uint) (*model.User, error) {
	var user model.User
	err := DB.First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepo) Update(user *model.User) error {
	return DB.Save(user).Error
}

func (r *UserRepo) UpdatePassword(id uint, passwordHash string) error {
	return DB.Model(&model.User{}).Where("id = ?", id).Update("password_hash", passwordHash).Error
}

func (r *UserRepo) SetMustChangePassword(id uint, must bool) error {
	return DB.Model(&model.User{}).Where("id = ?", id).Update("must_change_password", must).Error
}
