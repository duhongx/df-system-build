package repository

import "df-build-server/internal/model"

type ServerRepo struct{}

func NewServerRepo() *ServerRepo { return &ServerRepo{} }

func (r *ServerRepo) List() ([]model.RemoteServer, error) {
	var list []model.RemoteServer
	err := DB.Order("id ASC").Find(&list).Error
	return list, err
}

func (r *ServerRepo) FindByID(id uint) (*model.RemoteServer, error) {
	var s model.RemoteServer
	err := DB.First(&s, id).Error
	return &s, err
}

func (r *ServerRepo) Create(s *model.RemoteServer) error { return DB.Create(s).Error }

func (r *ServerRepo) Update(s *model.RemoteServer) error { return DB.Save(s).Error }

func (r *ServerRepo) Delete(id uint) error { return DB.Delete(&model.RemoteServer{}, id).Error }

func (r *ServerRepo) ExistsByName(name string) bool {
	var count int64
	DB.Model(&model.RemoteServer{}).Where("name = ?", name).Count(&count)
	return count > 0
}

func (r *ServerRepo) FindByIDs(ids []uint) ([]model.RemoteServer, error) {
	var list []model.RemoteServer
	err := DB.Where("id IN ?", ids).Find(&list).Error
	return list, err
}
