package scheduler

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/notification"
	"df-build-server/internal/pipeline"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/logger"
	"df-build-server/pkg/sse"
)

type BuildScheduler struct {
	mu              sync.Mutex
	queue           []uint // pending pipeline IDs (FIFO)
	running         map[uint]context.CancelFunc
	maxSlots        int
	engine          *pipeline.Engine
	pipelineRepo    *repository.PipelineRepo
	taskRepo        *repository.TaskRepo
	appRepo         *repository.ApplicationRepo
	serverRepo      *repository.ServerRepo
	buildConfigRepo *repository.BuildConfigRepo
	settingsRepo    *repository.SettingsRepo
}

var DefaultScheduler *BuildScheduler

func Init() {
	DefaultScheduler = &BuildScheduler{
		queue:           make([]uint, 0),
		running:         make(map[uint]context.CancelFunc),
		maxSlots:        5,
		engine:          pipeline.NewEngine(),
		pipelineRepo:    repository.NewPipelineRepo(),
		taskRepo:        repository.NewTaskRepo(),
		appRepo:         repository.NewApplicationRepo(),
		serverRepo:      repository.NewServerRepo(),
		buildConfigRepo: repository.NewBuildConfigRepo(),
		settingsRepo:    repository.NewSettingsRepo(),
	}

	// Load concurrency limit from settings
	if val, err := DefaultScheduler.settingsRepo.GetByKey("concurrency_limit"); err == nil {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			DefaultScheduler.maxSlots = limit
		}
	}

	// Recover pending pipelines on startup
	DefaultScheduler.recoverPending()

	logger.Log.Infof("Build scheduler initialized (concurrency: %d)", DefaultScheduler.maxSlots)
}

func (s *BuildScheduler) recoverPending() {
	// Mark stranded RUNNING pipelines as FAILED (process crashed mid-build)
	var stranded []model.Pipeline
	repository.DB.Where("status = ?", "RUNNING").Find(&stranded)
	for _, p := range stranded {
		s.pipelineRepo.UpdateStatus(p.ID, "FAILED")
		logger.Log.Warnf("Marked stranded RUNNING pipeline %d (%s) as FAILED", p.ID, p.PipelineNo)
	}

	// Requeue PENDING pipelines
	pending, _ := s.pipelineRepo.GetPending()
	for _, p := range pending {
		s.queue = append(s.queue, p.ID)
	}
	if len(pending) > 0 {
		logger.Log.Infof("Recovered %d pending pipelines", len(pending))
		s.tryDispatch()
	}
}

func (s *BuildScheduler) Enqueue(pipelineID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.queue = append(s.queue, pipelineID)
	logger.Log.Infof("Pipeline %d enqueued (queue size: %d, running: %d)", pipelineID, len(s.queue), len(s.running))
	s.tryDispatch()
}

func (s *BuildScheduler) Cancel(pipelineID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove from queue if pending
	for i, id := range s.queue {
		if id == pipelineID {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.pipelineRepo.UpdateStatus(pipelineID, "CANCELED")
			logger.Log.Infof("Pipeline %d canceled from queue", pipelineID)
			return
		}
	}

	// Cancel if running
	if cancel, ok := s.running[pipelineID]; ok {
		cancel()
		logger.Log.Infof("Pipeline %d cancel signal sent", pipelineID)
	}
}

func (s *BuildScheduler) SetConcurrency(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSlots = limit
	logger.Log.Infof("Concurrency limit updated to %d", limit)
	s.tryDispatch()
}

func (s *BuildScheduler) GetRunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

func (s *BuildScheduler) GetQueueSize() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

func (s *BuildScheduler) tryDispatch() {
	for len(s.running) < s.maxSlots && len(s.queue) > 0 {
		pipelineID := s.queue[0]
		s.queue = s.queue[1:]

		// Read build timeout from settings
		timeout := 30 * time.Minute
		if val, err := s.settingsRepo.GetByKey("build_timeout_seconds"); err == nil {
			if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
				timeout = time.Duration(secs) * time.Second
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		s.running[pipelineID] = cancel

		go s.executePipeline(ctx, pipelineID)
	}
}

func (s *BuildScheduler) executePipeline(ctx context.Context, pipelineID uint) {
	defer func() {
		s.mu.Lock()
		delete(s.running, pipelineID)
		s.tryDispatch()
		s.mu.Unlock()
	}()

	// Load pipeline
	p, err := s.pipelineRepo.FindByID(pipelineID)
	if err != nil {
		logger.Log.Errorf("Pipeline %d not found: %v", pipelineID, err)
		return
	}

	// Mark as running
	now := time.Now()
	p.Status = "RUNNING"
	p.StartTime = &now
	s.pipelineRepo.Update(p)

	// Build pipeline context
	pCtx := s.buildContext(p)
	if pCtx == nil {
		p.Status = "FAILED"
		p.ErrorMessage = "构建上下文初始化失败"
		endTime := time.Now()
		p.EndTime = &endTime
		s.pipelineRepo.Update(p)
		return
	}

	// Execute
	execErr := s.engine.Execute(ctx, p, pCtx)

	// Update final status
	endTime := time.Now()
	p.EndTime = &endTime
	dur := int(endTime.Sub(now).Seconds())
	p.DurationSeconds = &dur

	if execErr != nil {
		if errors.Is(execErr, pipeline.ErrImageReady) {
			// Manual mode: pause at IMAGE_READY, don't mark as failed
			// Cancel any existing IMAGE_READY pipelines for the same app (keep only latest)
			s.pipelineRepo.CancelOldImageReady(p.ApplicationID, p.ID)

				p.Status = "IMAGE_READY"
				p.EndTime = nil
				p.DurationSeconds = nil
				s.pipelineRepo.Update(p)
				service.MarkPipelineImageReady(p.ID, p.ImageName)
				sse.DefaultHub.Publish(pipelineID, sse.Event{Type: "image_ready", Data: "镜像已就绪，等待确认部署"})
			// Create notification
			notifyRepo := repository.NewNotificationMsgRepo()
			notifyRepo.Create(&model.NotificationMsg{
				Type: "build_complete", Title: fmt.Sprintf("%s 镜像构建完成", p.AppName),
				Content: fmt.Sprintf("应用 %s 镜像已推送到仓库，等待确认部署", p.AppName),
				Level:   "success", PipelineID: p.ID,
			})
			logger.Log.Infof("Pipeline %d paused at IMAGE_READY", pipelineID)
			return
		}
		latest, _ := s.pipelineRepo.FindByID(pipelineID)
		if latest != nil && latest.Status == "CANCELED" {
			p.Status = "CANCELED"
			p.ErrorMessage = "构建已取消"
			} else {
				p.Status = "FAILED"
				p.ErrorMessage = execErr.Error()
				service.MarkPipelineFailed(p.ID, p.ErrorMessage)
			}
	} else {
		p.Status = "SUCCESS"
	}
	s.pipelineRepo.Update(p)

	// Create notification for final status
	notifyRepo := repository.NewNotificationMsgRepo()
	if p.Status == "SUCCESS" {
		notifyRepo.Create(&model.NotificationMsg{
			Type: "deploy_complete", Title: fmt.Sprintf("%s 更新成功", p.AppName),
			Content: fmt.Sprintf("应用 %s 已成功部署", p.AppName),
			Level:   "success", PipelineID: p.ID,
		})
	} else if p.Status == "FAILED" {
		notifyRepo.Create(&model.NotificationMsg{
			Type: "deploy_failed", Title: fmt.Sprintf("%s 更新失败", p.AppName),
			Content: fmt.Sprintf("应用 %s 部署失败: %s", p.AppName, p.ErrorMessage),
			Level:   "error", PipelineID: p.ID,
		})
	}

	// Update task last run info
	if p.TaskID > 0 {
		s.taskRepo.UpdateLastRun(p.TaskID, p.Status, dur)
	}
	// Update application last build
	if p.ApplicationID > 0 {
		s.appRepo.UpdateBuildStatus(p.ApplicationID, p.Status)
	}

	// Create artifact record on success
	if p.Status == "SUCCESS" {
		artifact := &model.Artifact{
			PipelineID:      p.ID,
			PipelineNo:      p.PipelineNo,
			AppName:         p.AppName,
			ArtifactName:    p.ArtifactName,
			GitBranch:       p.GitBranch,
			GitCommitHash:   p.GitCommitHash,
			UploadPath:      p.UploadPath,
			UploadTargets:   p.UploadTargets,
			DurationSeconds: dur,
		}
		repository.NewArtifactRepo().Create(artifact)
	}

	// Send notifications (Task 12 integration)
	go func() {
		event := notification.BuildEvent{
			AppName:     p.AppName,
			Branch:      p.GitBranch,
			Status:      p.Status,
			TriggerUser: p.TriggerUser,
			Duration:    dur,
			ErrorStage:  p.ErrorStage,
			ErrorMsg:    p.ErrorMessage,
		}
		notification.SendBuildNotification(event)
	}()

	// Close SSE channel
	sse.DefaultHub.Close(pipelineID)

	logger.Log.Infof("Pipeline %d completed: %s (%ds)", pipelineID, p.Status, dur)
}

func (s *BuildScheduler) buildContext(p *model.Pipeline) *types.PipelineContext {
	var task *model.Task
	var buildConfig *model.BuildConfig
	if p.TaskID > 0 {
		var err error
		task, err = s.taskRepo.FindByID(p.TaskID)
		if err != nil {
			logger.Log.Errorf("Task %d not found for pipeline %d", p.TaskID, p.ID)
			return nil
		}
		if task.BuildConfigID > 0 {
			bc, _ := s.buildConfigRepo.FindByID(task.BuildConfigID)
			buildConfig = bc
		}
	}

	// BuildMode is determined by the BuildConfig itself (no global override)
	// User selects the BuildConfig in the task, which already specifies docker/local mode.

	// Get build/install command from application config (falling back to BuildConfig, then sensible defaults)
	buildCmd := ""
	installCmd := ""
	vueRole := ""
	appCode := ""
	nodePort := 0
	ingressHost := ""
	configMapContent := ""
	isGateway := false
	var ingresses []model.IngressConfig
	if app, err := s.appRepo.FindByID(p.ApplicationID); err == nil && app != nil {
		buildCmd = app.BuildCommand
		installCmd = app.InstallCommand
		vueRole = app.VueRole
		appCode = app.AppCode
		nodePort = app.NodePort
		ingressHost = app.IngressHost
		configMapContent = app.ConfigMapContent
		isGateway = app.IsGateway
		ingresses = app.GetIngresses()
	}
	// Fall back to BuildConfig commands
	if buildCmd == "" && buildConfig != nil {
		buildCmd = buildConfig.BuildCommand
	}
	if installCmd == "" && buildConfig != nil {
		installCmd = buildConfig.InstallCommand
	}
	// Final fallback to defaults
	if buildCmd == "" {
		if p.AppType == "vue" {
			buildCmd = "npm run build:new"
		} else {
			buildCmd = "gradle clean build -x test"
		}
	}
	if installCmd == "" && p.AppType == "vue" {
		installCmd = "yarn install"
	}

	return &types.PipelineContext{
		PipelineID: p.ID,
		Pipeline:   p,
		Workspace: &types.Workspace{
			BaseDir: func() string {
				abs, err := filepath.Abs("./workspaces")
				if err != nil {
					return "./workspaces"
				}
				return abs
			}(),
			PipelineID: p.ID,
			AppName:    p.AppName,
		},
		GitBranch:        p.GitBranch,
		GitRepo:          p.GitRepo,
		BuildConfig:      buildConfig,
		AppType:          p.AppType,
		AppName:          p.AppName,
		ArtifactName:     p.ArtifactName,
		BuildCommand:     buildCmd,
		InstallCommand:   installCmd,
		VueRole:          vueRole,
		AppCode:          appCode,
		IsGateway:        isGateway,
		DeployMode:       p.DeployMode,
		K8sNamespace:     p.K8sNamespace,
		ImageName:        p.ImageName,
		NodePort:         nodePort,
		IngressHost:      ingressHost,
		Ingresses:        ingresses,
		ConfigMapContent: configMapContent,
	}
}

// CreateAndEnqueue creates a pipeline record and enqueues it
// autoDeploy: true=镜像推送后自动部署, false=镜像推送后等待手动确认
func CreateAndEnqueue(taskID uint, gitBranch, triggerUser string, autoDeploy bool) (*model.Pipeline, error) {
	taskRepo := repository.NewTaskRepo()
	pipelineRepo := repository.NewPipelineRepo()
	appRepo := repository.NewApplicationRepo()
	settingsRepo := repository.NewSettingsRepo()

	task, err := taskRepo.FindByID(taskID)
	if err != nil {
		return nil, fmt.Errorf("任务不存在")
	}

	app, err := appRepo.FindByID(task.ApplicationID)
	if err != nil {
		return nil, fmt.Errorf("关联应用不存在")
	}

	// Determine deploy mode: task default, then global default, overridden by autoDeploy flag
	deployMode, _ := settingsRepo.GetByKey("deploy_mode")
	if task.DeployMode != "" {
		deployMode = task.DeployMode
	}
	if deployMode == "" {
		deployMode = "deploy"
	}
	if !autoDeploy {
		deployMode = "deploy_with_approval"
	}
	namespace := task.K8sNamespace
	if namespace == "" {
		namespace = k8s.GetDefaultNamespace()
	}

	p := &model.Pipeline{
		PipelineNo:    pipelineRepo.GenerateNo(app.AppName),
		TaskID:        taskID,
		ApplicationID: app.ID,
		AppName:       app.AppName,
		AppType:       app.AppType,
		GitRepo:       app.GitRepo,
		GitBranch:     gitBranch,
		Status:        "PENDING",
		TriggerUser:   triggerUser,
		BuilderImage: func() string {
			if task.BuildConfigID > 0 && task.BuildConfig.ID > 0 {
				return task.BuildConfig.DockerImage
			}
			return ""
		}(),
		ArtifactName: app.DeriveArtifactName(),
		DeployMode:   deployMode,
		K8sNamespace: namespace,
	}

	if err := pipelineRepo.Create(p); err != nil {
		return nil, err
	}

	DefaultScheduler.Enqueue(p.ID)
	return p, nil
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}
