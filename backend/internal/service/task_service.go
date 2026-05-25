package service

import (
	"errors"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

type TaskService struct {
	taskRepo        *repository.TaskRepo
	appRepo         *repository.ApplicationRepo
	buildConfigRepo *repository.BuildConfigRepo
}

func NewTaskService() *TaskService {
	return &TaskService{
		taskRepo:        repository.NewTaskRepo(),
		appRepo:         repository.NewApplicationRepo(),
		buildConfigRepo: repository.NewBuildConfigRepo(),
	}
}

type CreateTaskRequest struct {
	TaskName      string `json:"taskName" binding:"required"`
	ApplicationID uint   `json:"applicationId" binding:"required"`
	GitBranch     string `json:"gitBranch" binding:"required"`
	BuildConfigID uint   `json:"buildConfigId" binding:"required"`
	DeployMode    string `json:"deployMode"`
	K8sNamespace  string `json:"k8sNamespace"`
}

type UpdateTaskRequest struct {
	TaskName      string  `json:"taskName"`
	ApplicationID uint    `json:"applicationId"`
	GitBranch     string  `json:"gitBranch"`
	BuildConfigID uint    `json:"buildConfigId"`
	DeployMode    string  `json:"deployMode"`
	K8sNamespace  *string `json:"k8sNamespace"`
}

func (s *TaskService) List(params repository.TaskListParams) ([]model.Task, int64, error) {
	return s.taskRepo.List(params)
}

func (s *TaskService) GetByID(id uint) (*model.Task, error) {
	return s.taskRepo.FindByID(id)
}

func (s *TaskService) Create(req *CreateTaskRequest) (*model.Task, error) {
	if s.taskRepo.ExistsByName(req.TaskName) {
		return nil, errors.New("任务名称已存在")
	}

	// Validate application exists
	if _, err := s.appRepo.FindByID(req.ApplicationID); err != nil {
		return nil, errors.New("关联应用不存在")
	}

	// Validate build config exists
	if _, err := s.buildConfigRepo.FindByID(req.BuildConfigID); err != nil {
		return nil, errors.New("编译配置不存在")
	}

	task := &model.Task{
		TaskName:      req.TaskName,
		ApplicationID: req.ApplicationID,
		GitBranch:     req.GitBranch,
		BuildConfigID: req.BuildConfigID,
		DeployMode:    req.DeployMode,
		K8sNamespace:  req.K8sNamespace,
		Enabled:       true,
	}

	if task.DeployMode == "" {
		task.DeployMode = "deploy"
	}

	if err := s.taskRepo.Create(task); err != nil {
		return nil, err
	}

	// Reload with associations
	return s.taskRepo.FindByID(task.ID)
}

func (s *TaskService) Update(id uint, req *UpdateTaskRequest) (*model.Task, error) {
	task, err := s.taskRepo.FindByID(id)
	if err != nil {
		return nil, errors.New("任务不存在")
	}

	if req.TaskName != "" && req.TaskName != task.TaskName {
		// Check uniqueness against other tasks
		if s.taskRepo.ExistsByName(req.TaskName) {
			return nil, errors.New("任务名称已存在")
		}
		task.TaskName = req.TaskName
	}
	if req.ApplicationID > 0 {
		task.ApplicationID = req.ApplicationID
	}
	if req.GitBranch != "" {
		task.GitBranch = req.GitBranch
	}
	if req.BuildConfigID > 0 {
		task.BuildConfigID = req.BuildConfigID
	}

	// Deploy mode fields
	if req.DeployMode != "" {
		task.DeployMode = req.DeployMode
	}
	if req.K8sNamespace != nil {
		task.K8sNamespace = *req.K8sNamespace
	}

	if err := s.taskRepo.Update(task); err != nil {
		return nil, err
	}

	return s.taskRepo.FindByID(task.ID)
}

func (s *TaskService) Delete(id uint) error {
	if _, err := s.taskRepo.FindByID(id); err != nil {
		return errors.New("任务不存在")
	}
	return s.taskRepo.Delete(id)
}
