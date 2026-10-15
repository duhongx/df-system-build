package service

import (
	"errors"
	"strings"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

type TaskService struct {
	taskRepo *repository.TaskRepo
	appRepo  *repository.ApplicationRepo
}

func NewTaskService() *TaskService {
	return &TaskService{
		taskRepo: repository.NewTaskRepo(),
		appRepo:  repository.NewApplicationRepo(),
	}
}

type CreateTaskRequest struct {
	TaskName      string `json:"taskName" binding:"required"`
	ApplicationID uint   `json:"applicationId" binding:"required"`
	GitBranch     string `json:"gitBranch" binding:"required"`
	BuildConfigID uint   `json:"buildConfigId"`
	DeployMode    string `json:"deployMode"`
}

type UpdateTaskRequest struct {
	TaskName      string `json:"taskName"`
	ApplicationID uint   `json:"applicationId"`
	GitBranch     string `json:"gitBranch"`
	BuildConfigID uint   `json:"buildConfigId"`
	DeployMode    string `json:"deployMode"`
}

func (s *TaskService) List(params repository.TaskListParams) ([]model.Task, int64, error) {
	return s.taskRepo.List(params)
}

func (s *TaskService) GetByID(id uint) (*model.Task, error) {
	return s.taskRepo.FindByID(id)
}

func (s *TaskService) Create(req *CreateTaskRequest) (*model.Task, error) {
	if err := repository.RequireSourceBuild(); err != nil {
		return nil, err
	}
	if s.taskRepo.ExistsByName(req.TaskName) {
		return nil, errors.New("任务名称已存在")
	}

	// Validate application exists
	app, err := s.appRepo.FindByID(req.ApplicationID)
	if err != nil {
		return nil, errors.New("关联应用不存在")
	}

	if strings.TrimSpace(app.GitRepo) == "" {
		return nil, errors.New("源码任务需要配置应用的 Git 仓库")
	}

	bc, err := repository.DefaultBuildConfig(app.AppType)
	if err != nil {
		return nil, err
	}
	task := &model.Task{
		TaskName:      req.TaskName,
		ApplicationID: req.ApplicationID,
		GitBranch:     req.GitBranch,
		BuildConfigID: bc.ID,
		DeployMode:    req.DeployMode,
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
	if err := repository.RequireSourceBuild(); err != nil {
		return nil, err
	}
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
	app, err := s.appRepo.FindByID(task.ApplicationID)
	if err != nil {
		return nil, errors.New("关联应用不存在")
	}
	bc, err := repository.DefaultBuildConfig(app.AppType)
	if err != nil {
		return nil, err
	}
	task.BuildConfigID = bc.ID

	// Deploy mode fields
	if req.DeployMode != "" {
		task.DeployMode = req.DeployMode
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
