package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
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

	if err := service.RecoverInterruptedRollbacks(); err != nil {
		logger.Log.Errorf("恢复中断回滚任务状态失败: %v", err)
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
		p.Status = "FAILED"
		ended := time.Now()
		p.EndTime = &ended
		p.ErrorMessage = fmt.Sprintf("服务重启中断更新，已停止任务；最后记录部署进度=%s；已提交的 Kubernetes 操作可能仍在执行，请核对线上镜像和滚动状态后处理", p.DeploymentProgress)
		stages, stageErr := repository.NewStageRepo().FindByPipelineID(p.ID)
		if stageErr != nil {
			logger.Log.Errorf("读取中断任务阶段失败: %v", stageErr)
		}
		for _, stage := range stages {
			if stage.Status == "RUNNING" {
				p.ErrorStage = stage.StageCode
				stage.Status, stage.ErrorMessage = "FAILED", p.ErrorMessage
				if err := repository.NewStageRepo().Update(&stage); err != nil {
					logger.Log.Errorf("保存中断阶段失败: %v", err)
				}
				_, _ = repository.NewStageLogRepo().AppendLog(p.ID, stage.ID, p.ErrorMessage, "stderr")
			} else if stage.Status == "PENDING" {
				stage.Status = "SKIPPED"
				if err := repository.NewStageRepo().Update(&stage); err != nil {
					logger.Log.Errorf("跳过中断任务后续阶段失败: %v", err)
				}
			}
		}
		if err := s.pipelineRepo.Update(&p); err != nil {
			logger.Log.Errorf("保存中断任务失败: %v", err)
		}
		if err := service.MarkPipelineFailed(p.ID, p.ErrorMessage, p.DeploymentProgress); err != nil {
			s.reportStateError(&p, fmt.Errorf("保存部署失败记录失败: %w", err))
		}
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

func (s *BuildScheduler) Enqueue(pipelineID uint) { s.enqueue(pipelineID, false) }

// Rollout can be submitted while the build worker is finishing IMAGE_READY.
// Keep the resume queued until that worker has released its running slot.
func (s *BuildScheduler) EnqueueDeployment(pipelineID uint) { s.enqueue(pipelineID, true) }

func (s *BuildScheduler) enqueue(pipelineID uint, resume bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.running[pipelineID]; ok && !resume {
		return
	}
	for _, id := range s.queue {
		if id == pipelineID {
			return
		}
	}
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
		index := -1
		for i, id := range s.queue {
			if _, active := s.running[id]; !active {
				index = i
				break
			}
		}
		if index < 0 {
			return
		}
		pipelineID := s.queue[index]
		s.queue = append(s.queue[:index], s.queue[index+1:]...)

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
	if p.Status != "PENDING" && p.Status != "DEPLOYING" {
		return
	}
	resumeDeployment := p.Status == "DEPLOYING"
	p.Status = "RUNNING"
	p.EndTime = nil
	if p.StartTime == nil {
		p.StartTime = &now
	}
	if err := s.pipelineRepo.Update(p); err != nil {
		logger.Log.Errorf("Pipeline %d status update failed: %v", p.ID, err)
		return
	}

	// Build pipeline context
	pCtx := s.buildContext(p)
	if pCtx == nil {
		p.Status = "FAILED"
		p.ErrorMessage = "构建上下文初始化失败"
		endTime := time.Now()
		p.EndTime = &endTime
		s.savePipelineResult(p)
		return
	}

	pCtx.ResumeDeployment = resumeDeployment

	// Execute
	execErr := s.engine.Execute(ctx, p, pCtx)

	// Update final status
	endTime := time.Now()
	p.EndTime = &endTime
	dur := int(endTime.Sub(now).Seconds())
	p.DurationSeconds = &dur

	if execErr != nil {
		if errors.Is(execErr, pipeline.ErrImageReady) {
			// Step one completed. Other prepared images remain available.

			p.Status = "IMAGE_READY"

			if !s.savePipelineResult(p) {
				return
			}
			if err := service.MarkPipelineImageReady(p.ID, p.ImageName); err != nil {
				s.reportStateError(p, fmt.Errorf("保存镜像待部署记录失败: %w", err))
				return
			}
			sse.DefaultHub.Publish(pipelineID, sse.Event{Type: "image_ready", Data: "第一步完成：镜像已制作并推送，可执行滚动更新"})
			// Create notification
			notifyRepo := repository.NewNotificationMsgRepo()
			notifyRepo.Create(&model.NotificationMsg{
				Type: "build_complete", Title: fmt.Sprintf("%s 镜像构建完成", p.AppName),
				Content: fmt.Sprintf("应用 %s 镜像已推送到仓库，可执行第二步滚动更新", p.AppName),
				Level:   "success", PipelineID: p.ID,
			})
			logger.Log.Infof("Pipeline %d image preparation completed", pipelineID)
			return
		}
		latest, _ := s.pipelineRepo.FindByID(pipelineID)
		if latest != nil && latest.Status == "CANCELED" {
			p.Status = "CANCELED"
			p.ErrorMessage = "更新已取消；" + execErr.Error()
		} else {
			p.Status = "FAILED"
			p.ErrorMessage = execErr.Error()
		}
	} else {
		p.Status = "SUCCESS"
	}
	if !s.savePipelineResult(p) {
		return
	}
	if p.Status == "FAILED" || p.Status == "CANCELED" {
		if err := service.MarkPipelineFailed(p.ID, p.ErrorMessage, p.DeploymentProgress); err != nil {
			s.reportStateError(p, fmt.Errorf("保存部署失败记录失败: %w", err))
			return
		}
	}

	// Create notification for final status
	notifyRepo := repository.NewNotificationMsgRepo()
	if p.Status == "SUCCESS" {
		notifyRepo.Create(&model.NotificationMsg{
			Type: "deploy_complete", Title: fmt.Sprintf("%s 更新成功", p.AppName),
			Content: fmt.Sprintf("应用 %s 已成功部署", p.AppName),
			Level:   "success", PipelineID: p.ID,
		})
	} else if p.Status == "FAILED" {
		title, content, level := fmt.Sprintf("%s 更新失败", p.AppName), fmt.Sprintf("应用 %s 部署失败: %s", p.AppName, p.ErrorMessage), "error"
		if p.DeploymentProgress == "completed" {
			title, content, level = fmt.Sprintf("%s 部署成功，结果记录异常", p.AppName), fmt.Sprintf("应用 %s 滚动更新已完成，后续结果记录异常: %s", p.AppName, p.ErrorMessage), "warning"
		}
		notifyRepo.Create(&model.NotificationMsg{Type: "deploy_failed", Title: title, Content: content, Level: level, PipelineID: p.ID})
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
			DeploymentProgress: p.DeploymentProgress,
			AppName:            p.AppName,
			Branch:             p.GitBranch,
			Status:             p.Status,
			TriggerUser:        p.TriggerUser,
			Duration:           dur,
			ErrorStage:         p.ErrorStage,
			ErrorMsg:           p.ErrorMessage,
		}
		notification.SendBuildNotification(event)
	}()

	// Close SSE channel
	sse.DefaultHub.Close(pipelineID)

	logger.Log.Infof("Pipeline %d completed: %s (%ds)", pipelineID, p.Status, dur)
}

// A persistence failure must not emit an ordinary completion notification.
func (s *BuildScheduler) savePipelineResult(p *model.Pipeline) bool {
	if err := s.pipelineRepo.Update(p); err != nil {
		s.reportStateError(p, fmt.Errorf("保存流水线最终状态失败: %w", err))
		return false
	}
	return true
}

func (s *BuildScheduler) reportStateError(p *model.Pipeline, err error) {
	msg := fmt.Sprintf("流水线=%s；执行结果=%s；%v；原始执行信息=%s。集群操作不会因此撤销，请核对执行日志；页面持久化状态可能尚未更新。", p.PipelineNo, p.Status, err, p.ErrorMessage)
	if p.DeploymentProgress == "completed" {
		msg = "部署已成功，结果记录保存失败；" + msg
	}
	logger.Log.Error(msg)
	sse.DefaultHub.Publish(p.ID, sse.Event{Type: "state_error", Data: msg})
	sse.DefaultHub.Close(p.ID)
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

	// Get build/install command from application config (falling back to BuildConfig, then sensible defaults)
	buildCmd := ""
	installCmd := ""
	artifactDir := ""
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
		if p.TaskID > 0 {
			artifactDir = app.ArtifactDir
		}
		vueRole = app.VueRole
		appCode = app.AppCode
		nodePort = app.NodePort
		ingressHost = app.IngressHost
		configMapContent = app.ConfigMapContent
		isGateway = app.IsGateway
		ingresses = app.GetIngresses()
	}
	// Uploaded frontend roles and directories are fixed when the batch is submitted.
	if p.FrontendArtifactsJSON != "" {
		var artifacts []model.FrontendArtifact
		if err := json.Unmarshal([]byte(p.FrontendArtifactsJSON), &artifacts); err != nil || len(artifacts) == 0 {
			logger.Log.Errorf("Pipeline %d frontend artifact manifest is invalid", p.ID)
			return nil
		}
		vueRole, appCode = artifacts[0].Role, artifacts[0].AppCode
		for _, artifact := range artifacts {
			if artifact.Role == "main" {
				vueRole, appCode = "main", ""
				break
			}
		}
	}
	// Fall back to BuildConfig commands
	if buildCmd == "" && buildConfig != nil {
		buildCmd = buildConfig.BuildCommand
	}
	if installCmd == "" && buildConfig != nil {
		installCmd = buildConfig.InstallCommand
	}
	if artifactDir == "" && buildConfig != nil {
		artifactDir = buildConfig.ArtifactDir
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
		ArtifactDir:      artifactDir,
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
func CreateAndEnqueue(taskID uint, gitBranch, triggerUser string) (*model.Pipeline, error) {
	if err := repository.RequireSourceBuild(); err != nil {
		return nil, err
	}
	taskRepo := repository.NewTaskRepo()
	pipelineRepo := repository.NewPipelineRepo()
	appRepo := repository.NewApplicationRepo()
	settingsRepo := repository.NewSettingsRepo()

	task, err := taskRepo.FindByID(taskID)
	if err != nil {
		return nil, fmt.Errorf("任务不存在")
	}

	if !task.Enabled {
		return nil, fmt.Errorf("任务已停用，请先核对任务配置")
	}

	app, err := appRepo.FindByID(task.ApplicationID)
	if err != nil {
		return nil, fmt.Errorf("关联应用不存在")
	}

	if !app.Enabled {
		return nil, fmt.Errorf("应用已停用，请先核对应用配置")
	}

	if strings.TrimSpace(app.GitRepo) == "" {
		return nil, fmt.Errorf("源码任务需要配置应用的 Git 仓库")
	}
	bc, err := repository.NewBuildConfigRepo().FindByID(task.BuildConfigID)
	if err != nil || bc.Status != "online" {
		return nil, fmt.Errorf("编译配置不可用，请配置本机编译工具和命令后启用")
	}

	// Existing source-build task settings retain upload-only/upload behavior.
	deployMode := pipeline.NormalizeDeployMode(task.DeployMode)
	if task.DeployMode == "" {
		configured, err := settingsRepo.Read("deploy_mode", false)
		if err != nil {
			return nil, err
		}
		deployMode = pipeline.NormalizeDeployMode(configured)
	}
	// New pipelines always use the environment namespace; historical pipelines
	// retain the namespace captured when they were submitted.
	namespace, err := k8s.GetDefaultNamespace()
	if err != nil {
		return nil, err
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
		ArtifactName:  app.DeriveArtifactName(),
		DeployMode:    deployMode,
		K8sNamespace:  namespace,
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
