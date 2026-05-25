package repository

import "df-build-server/internal/model"

type TemplateRepo struct{}

func NewTemplateRepo() *TemplateRepo { return &TemplateRepo{} }

func (r *TemplateRepo) List() ([]model.Template, error) {
	var list []model.Template
	err := DB.Preload("Defaults").Order("id ASC").Find(&list).Error
	return list, err
}

func (r *TemplateRepo) FindByID(id uint) (*model.Template, error) {
	var t model.Template
	err := DB.Preload("Defaults").First(&t, id).Error
	return &t, err
}

func (r *TemplateRepo) Create(t *model.Template) error { return DB.Create(t).Error }

func (r *TemplateRepo) Update(t *model.Template) error { return DB.Save(t).Error }

func (r *TemplateRepo) Delete(id uint) error {
	DB.Where("template_id = ?", id).Delete(&model.TemplateDefault{})
	return DB.Delete(&model.Template{}, id).Error
}

func (r *TemplateRepo) ReplaceDefaults(templateID uint, defaults []model.TemplateDefault) error {
	DB.Where("template_id = ?", templateID).Delete(&model.TemplateDefault{})
	for i := range defaults {
		defaults[i].TemplateID = templateID
		defaults[i].ID = 0
	}
	if len(defaults) > 0 {
		return DB.Create(&defaults).Error
	}
	return nil
}
