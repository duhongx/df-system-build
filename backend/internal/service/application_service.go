package service

import (
	"errors"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

type ApplicationService struct {
	repo *repository.ApplicationRepo
}

func NewApplicationService() *ApplicationService {
	return &ApplicationService{
		repo: repository.NewApplicationRepo(),
	}
}

type CreateAppRequest struct {
	AppName          string `json:"appName" binding:"required"`
	AppType          string `json:"appType" binding:"required"`
	GitRepo          string `json:"gitRepo" binding:"required"`
	VueRole          string `json:"vueRole"`
	IsGateway        bool   `json:"isGateway"`
	AppCode          string `json:"appCode"`
	AppDesc          string `json:"appDesc"`
	NodePort         int    `json:"nodePort"`
	IngressHost      string `json:"ingressHost"`
	Ingresses        string `json:"ingresses"`
	ConfigMapContent string `json:"configMapContent"`
	EnvTags          string `json:"envTags"`
}

type UpdateAppRequest struct {
	AppType          string `json:"appType"`
	GitRepo          string `json:"gitRepo"`
	VueRole          string `json:"vueRole"`
	IsGateway        *bool  `json:"isGateway"`
	AppCode          string `json:"appCode"`
	AppDesc          string `json:"appDesc"`
	NodePort         *int   `json:"nodePort"`
	IngressHost      string `json:"ingressHost"`
	Ingresses        string `json:"ingresses"`
	ConfigMapContent string `json:"configMapContent"`
	EnvTags          string `json:"envTags"`
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
		if req.VueRole != "main" && req.VueRole != "sub" && req.VueRole != "standalone" {
			return nil, errors.New("应用角色必须是 main/sub/standalone")
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
		AppName:          req.AppName,
		AppType:          req.AppType,
		GitRepo:          req.GitRepo,
		VueRole:          req.VueRole,
		IsGateway:        isGateway,
		AppCode:          req.AppCode,
		AppDesc:          req.AppDesc,
		NodePort:         req.NodePort,
		IngressHost:      req.IngressHost,
		Ingresses:        req.Ingresses,
		ConfigMapContent: req.ConfigMapContent,
		EnvTags:          req.EnvTags,
		Enabled:          true,
	}

	// Derive artifact name
	app.ArtifactName = app.DeriveArtifactName()

	if err := s.repo.Create(app); err != nil {
		return nil, err
	}
	return app, nil
}

func (s *ApplicationService) Update(id uint, req *UpdateAppRequest) (*model.Application, error) {
	app, err := s.repo.FindByID(id)
	if err != nil {
		return nil, errors.New("应用不存在")
	}

	if req.AppType != "" {
		app.AppType = req.AppType
	}
	if req.GitRepo != "" {
		app.GitRepo = req.GitRepo
	}
	app.VueRole = req.VueRole
	app.AppCode = req.AppCode
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
