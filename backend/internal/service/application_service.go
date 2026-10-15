package service

import (
	"errors"
	"fmt"
	"sync"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

var applicationIdentityMu sync.Mutex

func validateApplicationIdentity(app *model.Application) error {
	if err := app.ValidateIdentity(); err != nil {
		return err
	}
	if app.AppType == "vue" && app.VueRole == "sub" {
		var count int64
		if err := repository.DB.Model(&model.Application{}).Where("app_type = ? AND vue_role = ? AND app_code = ? AND id <> ?", "vue", "sub", app.AppCode, app.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("应用编号 %s 已被占用", app.AppCode)
		}
	}
	return nil
}

type ApplicationService struct {
	repo *repository.ApplicationRepo
}

func NewApplicationService() *ApplicationService {
	return &ApplicationService{
		repo: repository.NewApplicationRepo(),
	}
}

type CreateAppRequest struct {
	AdoptExistingDirectory bool   `json:"adoptExistingDirectory"`
	AppName                string `json:"appName" binding:"required"`
	AppType                string `json:"appType" binding:"required"`
	GitRepo                string `json:"gitRepo"`
	VueRole                string `json:"vueRole"`
	IsGateway              bool   `json:"isGateway"`
	AppCode                string `json:"appCode"`
	AppDesc                string `json:"appDesc"`
	NodePort               int    `json:"nodePort"`
	IngressHost            string `json:"ingressHost"`
	Ingresses              string `json:"ingresses"`
	ConfigMapContent       string `json:"configMapContent"`
	EnvTags                string `json:"envTags"`
}

type UpdateAppRequest struct {
	AdoptExistingDirectory *bool  `json:"adoptExistingDirectory"`
	AppType                string `json:"appType"`
	GitRepo                string `json:"gitRepo"`
	VueRole                string `json:"vueRole"`
	IsGateway              *bool  `json:"isGateway"`
	AppCode                string `json:"appCode"`
	AppDesc                string `json:"appDesc"`
	NodePort               *int   `json:"nodePort"`
	IngressHost            string `json:"ingressHost"`
	Ingresses              string `json:"ingresses"`
	ConfigMapContent       string `json:"configMapContent"`
	EnvTags                string `json:"envTags"`
}

func (s *ApplicationService) List(params repository.AppListParams) ([]model.Application, int64, error) {
	return s.repo.List(params)
}

func (s *ApplicationService) ListAll() ([]model.Application, error) {
	return s.repo.ListAll()
}

func (s *ApplicationService) GetByID(id uint) (*model.Application, error) {
	return s.repo.FindByID(id)
}

func (s *ApplicationService) Create(req *CreateAppRequest) (*model.Application, error) {
	applicationIdentityMu.Lock()
	defer applicationIdentityMu.Unlock()
	// Validate unique name
	if s.repo.ExistsByName(req.AppName) {
		return nil, errors.New("应用名称已存在")
	}

	// Validate app type
	if req.AppType != "java" && req.AppType != "vue" {
		return nil, errors.New("项目类型必须是 java 或 vue")
	}

	// Validate Vue role
	if req.AppType == "vue" {
		if req.VueRole == "" {
			return nil, errors.New("Vue 项目必须指定应用角色")
		}
		if req.VueRole != "main" && req.VueRole != "sub" && req.VueRole != "standalone" && req.VueRole != "cdn" {
			return nil, errors.New("应用角色必须是 main/sub/standalone/cdn")
		}
		if req.VueRole == "sub" && req.AppCode == "" {
			return nil, errors.New("子应用必须指定应用编号")
		}
	}

	// IsGateway only meaningful for java apps
	isGateway := req.IsGateway
	if req.AppType != "java" {
		isGateway = false
	}

	app := &model.Application{
		AdoptExistingDirectory: req.AdoptExistingDirectory,
		AppName:                req.AppName,
		AppType:                req.AppType,
		GitRepo:                req.GitRepo,
		VueRole:                req.VueRole,
		IsGateway:              isGateway,
		AppCode:                req.AppCode,
		AppDesc:                req.AppDesc,
		NodePort:               req.NodePort,
		IngressHost:            req.IngressHost,
		Ingresses:              req.Ingresses,
		ConfigMapContent:       req.ConfigMapContent,
		EnvTags:                req.EnvTags,
		Enabled:                true,
	}

	// Derive artifact name
	app.ArtifactName = app.DeriveArtifactName()
	if err := validateApplicationIdentity(app); err != nil {
		return nil, err
	}

	if err := s.repo.Create(app); err != nil {
		return nil, err
	}
	return app, nil
}

func (s *ApplicationService) Update(id uint, req *UpdateAppRequest) (*model.Application, error) {
	applicationIdentityMu.Lock()
	defer applicationIdentityMu.Unlock()
	app, err := s.repo.FindByID(id)
	if err != nil {
		return nil, errors.New("应用不存在")
	}
	oldType, oldRole, oldCode := app.AppType, app.VueRole, app.AppCode
	if req.AdoptExistingDirectory != nil {
		app.AdoptExistingDirectory = *req.AdoptExistingDirectory
	}

	if req.AppType != "" {
		app.AppType = req.AppType
	}
	if req.GitRepo != "" {
		app.GitRepo = req.GitRepo
	}
	if req.VueRole != "" {
		app.VueRole = req.VueRole
	}
	if app.AppType == "java" {
		app.VueRole, app.AppCode = "", ""
	} else if app.VueRole != "sub" {
		app.AppCode = ""
	} else if req.AppCode != "" {
		app.AppCode = req.AppCode
	}
	if req.IsGateway != nil {
		app.IsGateway = *req.IsGateway
	}
	// IsGateway only meaningful for java apps
	if app.AppType != "java" {
		app.IsGateway = false
	}
	if req.AppDesc != "" {
		app.AppDesc = req.AppDesc
	}
	if req.NodePort != nil {
		app.NodePort = *req.NodePort
	}
	app.IngressHost = req.IngressHost
	app.Ingresses = req.Ingresses
	app.ConfigMapContent = req.ConfigMapContent
	app.EnvTags = req.EnvTags

	// Re-derive artifact name
	app.ArtifactName = app.DeriveArtifactName()
	if err := validateApplicationIdentity(app); err != nil {
		return nil, err
	}
	if oldType != app.AppType || oldRole != app.VueRole || oldCode != app.AppCode {
		var count int64
		if err := repository.DB.Model(&model.Pipeline{}).Where("application_id = ?", id).Count(&count).Error; err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, errors.New("应用已有流水线历史，不能直接修改类型、角色或编号；请通过配置迁移处理")
		}
	}

	if err := s.repo.Update(app); err != nil {
		return nil, err
	}
	return app, nil
}

func (s *ApplicationService) Delete(id uint) error {
	_, err := s.repo.FindByID(id)
	if err != nil {
		return errors.New("应用不存在")
	}
	return s.repo.Delete(id)
}
