package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/stages"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"df-build-server/internal/service"
	sshclient "df-build-server/internal/ssh"
	"df-build-server/pkg/crypto"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"gorm.io/gorm"
)

const (
	batchUploadRelativeRoot = "workspaces/batch-upload"
	artifactRelativeRoot    = "artifacts"
	artifactVersionKeep     = 3
)

var errDownloadCanceled = errors.New("下载任务已取消")

type BatchDeployHandler struct {
	appRepo       *repository.ApplicationRepo
	pipelineRepo  *repository.PipelineRepo
	runtimeReader service.ArtifactDeployRuntimeReader
	uploads       *artifactUploadStore
}

type deployRecordRef struct {
	FileName string
	AppID    uint
}

type downloadJobItem struct {
	Name       string `json:"name"`
	RemotePath string `json:"remotePath"`
	LocalPath  string `json:"localPath"`
	Size       int64  `json:"size"`
	ModTime    string `json:"modTime,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Artifact   bool   `json:"artifact"`
}

type downloadProgress struct {
	TotalFiles     int               `json:"totalFiles"`
	CompletedFiles int               `json:"completedFiles"`
	CurrentPath    string            `json:"currentPath"`
	Items          []downloadJobItem `json:"items"`
}

type downloadJob struct {
	ID             string            `json:"id"`
	Status         string            `json:"status"`
	RemotePath     string            `json:"remotePath"`
	SelectedPaths  []string          `json:"selectedPaths,omitempty"`
	LocalDir       string            `json:"localDir"`
	TargetPath     string            `json:"targetPath"`
	BatchID        string            `json:"batchId"`
	Files          []string          `json:"files"`
	Items          []downloadJobItem `json:"items"`
	Count          int               `json:"count"`
	TotalFiles     int               `json:"totalFiles"`
	CompletedFiles int               `json:"completedFiles"`
	CurrentPath    string            `json:"currentPath"`
	Error          string            `json:"error"`
	HasPartial     bool              `json:"hasPartial"`
	StartedAt      time.Time         `json:"startedAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

type artifactSourceBatch struct {
	BatchID     string    `json:"batchId"`
	SourceType  string    `json:"sourceType"`
	SourceLabel string    `json:"sourceLabel"`
	Status      string    `json:"status"`
	LocalDir    string    `json:"localDir"`
	TargetPath  string    `json:"targetPath"`
	Files       []string  `json:"files"`
	Count       int       `json:"count"`
	Error       string    `json:"error"`
	HasPartial  bool      `json:"hasPartial"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

var downloadJobStore = struct {
	sync.Mutex
	items map[string]*downloadJob
}{items: map[string]*downloadJob{}}

var downloadJobCancelStore = struct {
	sync.Mutex
	items map[string]context.CancelFunc
}{items: map[string]context.CancelFunc{}}

func NewBatchDeployHandler() *BatchDeployHandler {
	return &BatchDeployHandler{
		appRepo:       repository.NewApplicationRepo(),
		pipelineRepo:  repository.NewPipelineRepo(),
		runtimeReader: service.NewK8sRuntimeVersionReader(),
		uploads:       defaultArtifactUploads,
	}
}

func (h *BatchDeployHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/batch-deploy")
	g.Use(middleware.AuthRequired())
	{
		g.POST("/upload", h.Upload)
		g.POST("/uploads", h.CreateArtifactUpload)
		g.GET("/uploads/:uploadID", h.GetArtifactUpload)
		g.PUT("/uploads/:uploadID/files/:fileIndex/chunks/:chunkIndex", h.PutArtifactUploadChunk)
		g.POST("/uploads/:uploadID/complete", h.CompleteArtifactUpload)
		g.DELETE("/uploads/:uploadID", h.CancelArtifactUpload)
		g.POST("/remote-dir", h.ImportRemoteDir)
		g.GET("/package-server/:id/list", h.ListPackageServerDir)
		g.GET("/package-download/list", h.ListPackageDownloadDir)
		g.GET("/package-download/local-list", h.ListPackageDownloadLocalDir)
		g.POST("/download-remote-dir/start", h.StartDownloadRemoteDir)
		g.GET("/download-remote-dir/active", h.GetActiveDownloadJob)
		g.GET("/artifact-versions", h.ListArtifactVersions)
		g.POST("/artifact-versions/sync-workspace", h.SyncArtifactVersionsFromWorkspace)
		g.GET("/artifact-versions/:versionNo", h.GetArtifactVersion)
		g.GET("/artifact-versions/:versionNo/pipelines", h.GetArtifactVersionPipelines)
		g.DELETE("/artifact-versions/:versionNo", h.DeleteArtifactVersion)
		g.DELETE("/artifact-versions/:versionNo/items/:itemID", h.DeleteArtifactVersionItem)
		g.POST("/artifact-versions/:versionNo/items/:itemID/replace", h.ReplaceArtifactVersionItem)
		g.POST("/artifact-versions/:versionNo/items/:itemID/redownload", h.RedownloadArtifactVersionItem)
		g.GET("/deploy-batches", h.ListArtifactDeployBatches)
		g.GET("/deploy-batches/:batchNo", h.GetArtifactDeployBatch)
		g.POST("/deploy-batches/:batchNo/rollback", h.RollbackArtifactDeployBatch)
		g.POST("/deploy-batches/:batchNo/rollout", h.RolloutArtifactDeployBatch)
		g.GET("/source-batches", h.ListSourceBatches)
		g.POST("/download-remote-dir/cancel/*id", h.CancelDownloadJob)
		g.GET("/download-remote-dir/progress/*id", h.GetDownloadProgress)
		g.POST("/download-remote-dir", h.DownloadRemoteDir)
		g.POST("/match", h.Match)
		g.POST("/execute", h.Execute)
		g.GET("/local-dir", h.ListLocalDir)
		g.GET("/local-browser", h.ListLocalBrowser)
	}
}

func (h *BatchDeployHandler) ListSourceBatches(c *gin.Context) {
	if repository.DB != nil {
		_ = backfillArtifactVersionsFromWorkspace()
		versions := []model.ArtifactVersion{}
		if err := repository.DB.Order("updated_at DESC").Find(&versions).Error; err == nil && len(versions) > 0 {
			response.OK(c, gin.H{"batches": artifactSourceBatchesFromVersions(versions)})
			return
		}
	}

	root, err := batchUploadRootDir()
	if err != nil {
		response.Fail(c, 13018, err.Error())
		return
	}

	batches, err := listArtifactSourceBatches(root)
	if err != nil {
		response.Fail(c, 13018, err.Error())
		return
	}
	response.OK(c, gin.H{"batches": batches})
}

func (h *BatchDeployHandler) ListArtifactVersions(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13019, "数据库未初始化")
		return
	}
	versions := []model.ArtifactVersion{}
	query := repository.DB.Omit("release_manifest_json").Order("updated_at DESC")
	if c.Query("deployable") == "true" {
		query = query.Where("status = ? AND deployable_count > 0", "available")
	}
	if err := query.Find(&versions).Error; err != nil {
		response.Fail(c, 13019, "读取更新版本失败: "+err.Error())
		return
	}
	if err := markVersionUsage(versions); err != nil {
		response.Fail(c, 13019, "读取制品使用状态失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"versions": versions})
}

func (h *BatchDeployHandler) SyncArtifactVersionsFromWorkspace(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13019, "数据库未初始化")
		return
	}
	if err := backfillArtifactVersionsFromWorkspace(); err != nil {
		response.Fail(c, 13019, "同步历史更新版本失败: "+err.Error())
		return
	}
	versions := []model.ArtifactVersion{}
	if err := repository.DB.Omit("release_manifest_json").Order("updated_at DESC").Find(&versions).Error; err != nil {
		response.Fail(c, 13019, "读取更新版本失败: "+err.Error())
		return
	}
	if err := markVersionUsage(versions); err != nil {
		response.Fail(c, 13019, "读取制品使用状态失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"versions": versions})
}

func (h *BatchDeployHandler) GetArtifactVersion(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13020, "数据库未初始化")
		return
	}
	versionNo := safePathSegment(c.Param("versionNo"))
	if versionNo == "" {
		response.Fail(c, 13020, "版本号无效")
		return
	}
	var version model.ArtifactVersion
	if err := repository.DB.Where("version_no = ?", versionNo).First(&version).Error; err != nil {
		response.Fail(c, 13020, "更新版本不存在")
		return
	}
	items := []model.ArtifactVersionItem{}
	if err := repository.DB.Where("version_no = ?", versionNo).Order("file_name ASC").Find(&items).Error; err != nil {
		response.Fail(c, 13020, "读取版本明细失败: "+err.Error())
		return
	}
	// Resolve current frontend roles for the confirmation dialog, including older uploads.
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		if item.AppID != 0 {
			ids = append(ids, item.AppID)
		}
	}
	if len(ids) > 0 {
		var apps []model.Application
		if err := repository.DB.Select("id", "vue_role", "app_code").Where("id IN ?", ids).Find(&apps).Error; err != nil {
			response.Fail(c, 13020, "读取应用更新范围失败: "+err.Error())
			return
		}
		byID := make(map[uint]model.Application, len(apps))
		for _, app := range apps {
			byID[app.ID] = app
		}
		for i := range items {
			app := byID[items[i].AppID]
			items[i].VueRole, items[i].AppCode = app.VueRole, app.AppCode
		}
	}
	if err := markArtifactUsage(&version, items); err != nil {
		response.Fail(c, 13020, "读取制品使用状态失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"version": version, "items": items})
}

func (h *BatchDeployHandler) GetArtifactVersionPipelines(c *gin.Context) {
	if repository.DB == nil {
		response.FailWithStatus(c, 503, 13020, "数据库未初始化")
		return
	}
	versionNo := strings.TrimSpace(c.Param("versionNo"))
	if versionNo == "" || safePathSegment(versionNo) != versionNo {
		response.Fail(c, 13020, "版本号无效")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	var pipelines []model.Pipeline
	if err := repository.DB.WithContext(ctx).Preload("Stages", func(db *gorm.DB) *gorm.DB { return db.Order("stage_order ASC") }).Where("batch_id = ?", versionNo).Order("id ASC").Find(&pipelines).Error; err != nil {
		response.FailWithStatus(c, 503, 13020, "读取镜像任务进度失败: "+err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, gin.H{"pipelines": pipelines})
}

func (h *BatchDeployHandler) DeleteArtifactVersion(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13026, "数据库未初始化")
		return
	}
	versionNo := safePathSegment(c.Param("versionNo"))
	if versionNo == "" {
		response.Fail(c, 13026, "版本号无效")
		return
	}
	if err := deleteArtifactVersion(versionNo); err != nil {
		response.Fail(c, 13026, err.Error())
		return
	}
	response.OK(c, gin.H{"versionNo": versionNo})
}

func deleteArtifactVersion(versionNo string) error {
	var version model.ArtifactVersion
	if err := repository.DB.Where("version_no = ?", versionNo).First(&version).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("更新版本不存在")
		}
		return err
	}
	used, err := usedArtifactFiles(repository.DB, versionNo)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		return fmt.Errorf("该版本已有制品提交更新，不能删除；如需再次更新请重新上传")
	}
	if hasRunningDownloadJob(versionNo) {
		return fmt.Errorf("下载任务正在执行，不能删除")
	}
	var deployBatchCount int64
	if err := repository.DB.Model(&model.ArtifactDeployBatch{}).Where("version_no = ?", versionNo).Count(&deployBatchCount).Error; err != nil {
		return err
	}
	if deployBatchCount > 0 {
		return fmt.Errorf("该版本已有部署批次，不能删除")
	}

	workspaceDir, err := artifactVersionWorkspaceDir(version)
	if err != nil {
		return err
	}
	if workspaceDir != "" {
		if err := os.RemoveAll(workspaceDir); err != nil {
			return fmt.Errorf("删除版本目录失败: %w", err)
		}
	}

	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("version_no = ?", versionNo).Delete(&model.ArtifactVersionItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("version_no = ?", versionNo).Delete(&model.ArtifactVersion{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	removeDownloadJobsFromMemoryByBatchID(versionNo)
	return nil
}

func artifactVersionWorkspaceDir(version model.ArtifactVersion) (string, error) {
	root, err := batchUploadRootDir()
	if err != nil {
		return "", err
	}
	rawDir := firstNonEmptyString(version.LocalDir, version.TargetPath)
	if strings.TrimSpace(rawDir) == "" {
		return "", nil
	}
	absDir, err := filepath.Abs(rawDir)
	if err != nil {
		return "", err
	}
	if !isPathInside(root, absDir) {
		return "", fmt.Errorf("版本目录超出批量上传工作区")
	}
	if isDownloadWorkspaceRoot(absDir) {
		return "", nil
	}
	rel, err := filepath.Rel(root, absDir)
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) >= 2 {
		switch parts[0] {
		case "upload", "local", "download", "artifact":
			return filepath.Join(root, parts[0], parts[1]), nil
		}
	}
	return absDir, nil
}

func (h *BatchDeployHandler) DeleteArtifactVersionItem(c *gin.Context) {
	version, item, ok := h.loadArtifactVersionItem(c)
	if !ok {
		return
	}
	filePath, err := resolveArtifactPath(version.TargetPath, item.FileName)
	if err == nil {
		if removeErr := os.Remove(filePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			response.Fail(c, 13023, "删除制品文件失败: "+removeErr.Error())
			return
		}
	}
	updatedVersion, items, err := refreshArtifactVersion(version)
	if err != nil {
		response.Fail(c, 13023, "刷新版本信息失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"version": updatedVersion, "items": items})
}

func (h *BatchDeployHandler) ReplaceArtifactVersionItem(c *gin.Context) {
	version, item, ok := h.loadArtifactVersionItem(c)
	if !ok {
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		response.Fail(c, 13024, "请选择替换文件")
		return
	}
	fileName, err := safeArtifactFileName(file.Filename)
	if err != nil {
		response.Fail(c, 13024, err.Error())
		return
	}
	used, err := usedArtifactFiles(repository.DB, version.VersionNo)
	if err != nil {
		response.Fail(c, 13024, "读取制品使用状态失败: "+err.Error())
		return
	}
	if used[fileName] {
		response.Fail(c, 13024, "目标制品已提交过更新，不能覆盖；请重新上传生成新版本")
		return
	}
	if oldPath, err := resolveArtifactPath(version.TargetPath, item.FileName); err == nil && item.FileName != fileName {
		if removeErr := os.Remove(oldPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			response.Fail(c, 13024, "删除旧制品文件失败: "+removeErr.Error())
			return
		}
	}
	dst := filepath.Join(version.TargetPath, fileName)
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		response.Fail(c, 13024, "创建目录失败: "+err.Error())
		return
	}
	if err := c.SaveUploadedFile(file, dst); err != nil {
		response.Fail(c, 13024, "保存替换文件失败: "+err.Error())
		return
	}
	updatedVersion, items, err := refreshArtifactVersion(version)
	if err != nil {
		response.Fail(c, 13024, "刷新版本信息失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"version": updatedVersion, "items": items})
}

func (h *BatchDeployHandler) RedownloadArtifactVersionItem(c *gin.Context) {
	version, item, ok := h.loadArtifactVersionItem(c)
	if !ok {
		return
	}
	if version.SourceType != "download" || strings.TrimSpace(version.RemotePath) == "" {
		response.Fail(c, 13025, "该版本不是服务器下载来源，不能重新下载")
		return
	}
	sftpClient, sshClient, err := connectPackageDownloadSFTP()
	if err != nil {
		response.Fail(c, 13025, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	if err := redownloadArtifactItemFromRemote(sftpRemoteFS{client: sftpClient}, version.RemotePath, version.TargetPath, item.FileName); err != nil {
		response.Fail(c, 13025, "重新下载失败: "+err.Error())
		return
	}
	updatedVersion, items, err := refreshArtifactVersion(version)
	if err != nil {
		response.Fail(c, 13025, "刷新版本信息失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"version": updatedVersion, "items": items})
}

func (h *BatchDeployHandler) loadArtifactVersionItem(c *gin.Context) (model.ArtifactVersion, model.ArtifactVersionItem, bool) {
	if repository.DB == nil {
		response.Fail(c, 13023, "数据库未初始化")
		return model.ArtifactVersion{}, model.ArtifactVersionItem{}, false
	}
	versionNo := safePathSegment(c.Param("versionNo"))
	itemID, err := strconv.ParseUint(c.Param("itemID"), 10, 64)
	if versionNo == "" || err != nil || itemID == 0 {
		response.Fail(c, 13023, "参数错误")
		return model.ArtifactVersion{}, model.ArtifactVersionItem{}, false
	}
	var version model.ArtifactVersion
	if err := repository.DB.Where("version_no = ?", versionNo).First(&version).Error; err != nil {
		response.Fail(c, 13023, "更新版本不存在")
		return model.ArtifactVersion{}, model.ArtifactVersionItem{}, false
	}
	var item model.ArtifactVersionItem
	if err := repository.DB.Where("id = ? AND version_no = ?", itemID, versionNo).First(&item).Error; err != nil {
		response.Fail(c, 13023, "制品不存在")
		return model.ArtifactVersion{}, model.ArtifactVersionItem{}, false
	}
	used, err := usedArtifactFiles(repository.DB, versionNo)
	if err != nil {
		response.Fail(c, 13023, "读取制品使用记录失败: "+err.Error())
		return model.ArtifactVersion{}, model.ArtifactVersionItem{}, false
	}
	if used[item.FileName] {
		response.Fail(c, 13023, "该制品已提交过更新，不能修改；请重新上传生成新版本")
		return model.ArtifactVersion{}, model.ArtifactVersionItem{}, false
	}
	return version, item, true
}

func (h *BatchDeployHandler) ListArtifactDeployBatches(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13021, "数据库未初始化")
		return
	}
	batches := []model.ArtifactDeployBatch{}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	query := repository.DB.WithContext(ctx).Order("updated_at DESC")
	if c.Query("readyForRollout") == "true" {
		query = query.Where("deploy_batch_no IN (?)", repository.DB.Model(&model.Pipeline{}).Select("batch_id").Where("status = ?", "IMAGE_READY"))
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Find(&batches).Error; err != nil {
		response.Fail(c, 13021, "读取部署批次失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"batches": batches})
}

func (h *BatchDeployHandler) GetArtifactDeployBatch(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13022, "数据库未初始化")
		return
	}
	batchNo := safePathSegment(c.Param("batchNo"))
	if batchNo == "" {
		response.Fail(c, 13022, "部署批次号无效")
		return
	}
	var batch model.ArtifactDeployBatch
	if err := repository.DB.Where("deploy_batch_no = ?", batchNo).First(&batch).Error; err != nil {
		namespace := strings.TrimSpace(c.Query("namespace"))
		if namespace == "" {
			var nsErr error
			namespace, nsErr = k8s.GetDefaultNamespace()
			if nsErr != nil {
				response.Fail(c, 13004, nsErr.Error())
				return
			}
		}
		records, previewErr := service.BuildArtifactDeployPreviewRecordsFromCache(c.Request.Context(), batchNo, namespace)
		if previewErr != nil {
			response.Fail(c, 13022, "读取当前运行状态失败: "+previewErr.Error())
			return
		}
		response.OK(c, gin.H{"batch": nil, "records": records})
		return
	}
	records := []model.ArtifactDeployRecord{}
	if err := repository.DB.Where("deploy_batch_no = ?", batchNo).Order("deployment_name ASC, app_name ASC").Find(&records).Error; err != nil {
		response.Fail(c, 13022, "读取部署记录失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"batch": batch, "records": records})
}

// RolloutArtifactDeployBatch submits only prepared targets. A preparation or
// submission failure never prevents another target from being rolled out.
func (h *BatchDeployHandler) RolloutArtifactDeployBatch(c *gin.Context) {
	batchNo := safePathSegment(c.Param("batchNo"))
	if batchNo == "" {
		response.Fail(c, 13023, "部署批次号无效")
		return
	}
	var ready []model.Pipeline
	if err := repository.DB.Where("batch_id = ? AND status = ?", batchNo, "IMAGE_READY").Order("id ASC").Find(&ready).Error; err != nil {
		response.Fail(c, 13023, "读取可滚动更新任务失败: "+err.Error())
		return
	}
	submitted := []model.Pipeline{}
	failures := []string{}
	for _, target := range ready {
		p, err := scheduler.DefaultScheduler.StartRollout(target.ID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", target.AppName, err))
			continue
		}
		submitted = append(submitted, *p)
	}
	response.OKWithMessage(c, fmt.Sprintf("已提交 %d 个滚动更新任务", len(submitted)), gin.H{"pipelines": submitted, "errors": failures})
}

func (h *BatchDeployHandler) RollbackArtifactDeployBatch(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13023, "数据库未初始化")
		return
	}
	batchNo := safePathSegment(c.Param("batchNo"))
	if batchNo == "" {
		response.Fail(c, 13023, "部署批次号无效")
		return
	}
	batch, err := service.StartRollbackDeployBatch(batchNo, service.NewK8sDeploymentRollbacker(), service.NewK8sRuntimeVersionReader())
	if err != nil {
		response.Fail(c, 13023, "提交回滚失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"taskId": batchNo, "batch": batch})
}

func artifactSourceBatchesFromVersions(versions []model.ArtifactVersion) []artifactSourceBatch {
	batches := make([]artifactSourceBatch, 0, len(versions))
	for _, version := range versions {
		batches = append(batches, artifactSourceBatch{
			BatchID:     version.VersionNo,
			SourceType:  version.SourceType,
			SourceLabel: version.SourceLabel,
			Status:      artifactVersionStatusForLegacy(version.Status),
			LocalDir:    version.LocalDir,
			TargetPath:  version.TargetPath,
			Count:       version.Count,
			Error:       version.Error,
			UpdatedAt:   version.UpdatedAt,
		})
	}
	return batches
}

func artifactVersionStatusForLegacy(status string) string {
	switch status {
	case "available":
		return "ready"
	case "collecting":
		return "running"
	default:
		return status
	}
}

func backfillArtifactVersionsFromWorkspace() error {
	if repository.DB == nil {
		return nil
	}
	root, err := batchUploadRootDir()
	if err != nil {
		return err
	}
	batches, err := listArtifactSourceBatches(root)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if batch.BatchID == "" || batch.TargetPath == "" {
			continue
		}
		input := artifactVersionInput{
			VersionNo:   batch.BatchID,
			SourceType:  batch.SourceType,
			SourceLabel: batch.SourceLabel,
			Status:      artifactVersionStatusFromBatch(batch),
			LocalDir:    batch.LocalDir,
			TargetPath:  batch.TargetPath,
			Error:       batch.Error,
		}
		var existing model.ArtifactVersion
		err := repository.DB.Where("version_no = ?", batch.BatchID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if _, _, err := persistArtifactVersionFromDir(input); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !shouldRefreshArtifactVersionFromBatch(existing, batch, input.Status) {
			continue
		}
		if _, _, err := persistArtifactVersionFromDir(input); err != nil {
			return err
		}
	}
	return nil
}

func shouldRefreshArtifactVersionFromBatch(version model.ArtifactVersion, batch artifactSourceBatch, nextStatus string) bool {
	if strings.TrimSpace(version.TargetPath) == "" || strings.TrimSpace(version.LocalDir) == "" {
		return true
	}
	if batch.Count > 0 && (version.Count != batch.Count || version.DeployableCount == 0) {
		return true
	}
	if nextStatus == "available" {
		switch version.Status {
		case "", "collecting", "running", "pending":
			return true
		}
	}
	if version.Status == "" && nextStatus != "" {
		return true
	}
	return false
}

func artifactVersionStatusFromBatch(batch artifactSourceBatch) string {
	if batch.Status == "ready" || batch.Status == "success" || batch.Status == "" {
		return "available"
	}
	if batch.Status == "running" {
		return "collecting"
	}
	return batch.Status
}

// Upload handles multipart file upload, saves to temp dir, returns file list with validation
func (h *BatchDeployHandler) Upload(c *gin.Context) {
	form, err := c.MultipartForm()
	if err != nil {
		response.Fail(c, 13001, "文件上传失败")
		return
	}

	files := form.File["files"]
	if len(files) == 0 {
		response.Fail(c, 13001, "请选择文件")
		return
	}

	// Create an isolated temp upload dir per batch.
	batchID := newBatchID()
	rootDir, err := batchUploadSourceRoot("upload")
	if err != nil {
		response.Fail(c, 13001, "初始化上传目录失败: "+err.Error())
		return
	}
	uploadDir := filepath.Join(rootDir, batchID)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		response.Fail(c, 13001, "创建上传目录失败: "+err.Error())
		return
	}

	type uploadFailure struct {
		FileName string `json:"fileName"`
		Error    string `json:"error"`
	}

	var successFiles []string
	var failedFiles []uploadFailure
	seenNames := map[string]bool{}
	manifestSaved := false

	for _, file := range files {
		fileName, err := safeUploadFileName(file.Filename)
		if err != nil {
			failedFiles = append(failedFiles, uploadFailure{FileName: file.Filename, Error: err.Error()})
			continue
		}
		if seenNames[fileName] {
			failedFiles = append(failedFiles, uploadFailure{FileName: file.Filename, Error: "目录内存在同名制品文件"})
			continue
		}
		seenNames[fileName] = true
		if fileName == service.ReleaseManifestFileName && file.Size > service.MaxReleaseManifestBytes {
			failedFiles = append(failedFiles, uploadFailure{FileName: fileName, Error: "version.json 不能超过 1 MiB"})
			continue
		}

		dst := filepath.Join(uploadDir, fileName)
		if err := c.SaveUploadedFile(file, dst); err != nil {
			failedFiles = append(failedFiles, uploadFailure{FileName: fileName, Error: "保存文件失败"})
			continue
		}
		if fileName == service.ReleaseManifestFileName {
			manifestSaved = true
			continue
		}

		// Validate artifact integrity
		if err := validateArtifact(dst); err != nil {
			os.Remove(dst)
			failedFiles = append(failedFiles, uploadFailure{FileName: fileName, Error: err.Error()})
			continue
		}

		successFiles = append(successFiles, fileName)
	}

	if len(successFiles) > 0 || manifestSaved {
		if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
			VersionNo:   batchID,
			SourceType:  "upload",
			SourceLabel: "本地上传",
			Status:      "available",
			LocalDir:    uploadDir,
			TargetPath:  uploadDir,
		}); err != nil {
			response.Fail(c, 13001, "制品版本入库失败: "+err.Error())
			return
		}
	}

	response.OK(c, gin.H{
		"batchId":   batchID,
		"uploadDir": uploadDir,
		"success":   successFiles,
		"failed":    failedFiles,
		"count":     len(successFiles),
	})
}

func (h *BatchDeployHandler) ImportRemoteDir(c *gin.Context) {
	var req struct {
		ServerID uint   `json:"serverId" binding:"required"`
		Path     string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 13005, "参数错误")
		return
	}

	batchID := newBatchID()
	rootDir, err := batchUploadSourceRoot("download")
	if err != nil {
		response.Fail(c, 13005, "初始化上传目录失败: "+err.Error())
		return
	}
	uploadDir := filepath.Join(rootDir, batchID)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		response.Fail(c, 13005, "创建上传目录失败: "+err.Error())
		return
	}

	sftpClient, sshClient, err := NewWebSFTPHandler().connectSFTP(req.ServerID)
	if err != nil {
		response.Fail(c, 13005, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	files, err := importArtifactsFromRemoteDir(sftpRemoteFS{client: sftpClient}, req.Path, uploadDir)
	if err != nil {
		response.Fail(c, 13005, "导入远程目录失败: "+err.Error())
		return
	}

	if len(files) > 0 {
		if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
			VersionNo:   batchID,
			SourceType:  "download",
			SourceLabel: "服务器下载",
			Status:      "available",
			LocalDir:    uploadDir,
			TargetPath:  uploadDir,
			RemotePath:  req.Path,
		}); err != nil {
			response.Fail(c, 13005, "制品版本入库失败: "+err.Error())
			return
		}
	}

	response.OK(c, gin.H{
		"batchId":   batchID,
		"uploadDir": uploadDir,
		"files":     files,
		"success":   files,
		"count":     len(files),
	})
}

func (h *BatchDeployHandler) DownloadRemoteDir(c *gin.Context) {
	var req struct {
		ServerID   uint   `json:"serverId"`
		RemotePath string `json:"remotePath" binding:"required"`
		LocalPath  string `json:"localPath" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 13006, "参数错误")
		return
	}

	localDir, err := downloadWorkspaceRootDir()
	if err != nil {
		response.Fail(c, 13006, err.Error())
		return
	}
	batchID := newBatchID()

	var sftpClient *sftp.Client
	var sshClient io.Closer
	if req.ServerID > 0 {
		sftpClient, sshClient, err = connectPackageServerSFTP(req.ServerID)
	} else {
		sftpClient, sshClient, err = connectPackageDownloadSFTP()
	}
	if err != nil {
		response.Fail(c, 13006, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	targetDir, files, err := downloadRemoteDirToLocal(sftpRemoteFS{client: sftpClient}, req.RemotePath, localDir)
	if err != nil {
		response.Fail(c, 13006, "下载远程目录失败: "+err.Error())
		return
	}

	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   batchID,
		SourceType:  "download",
		SourceLabel: "服务器下载",
		Status:      "available",
		LocalDir:    targetDir,
		TargetPath:  targetDir,
		RemotePath:  req.RemotePath,
	}); err != nil {
		response.Fail(c, 13006, "制品版本入库失败: "+err.Error())
		return
	}

	response.OK(c, gin.H{
		"targetPath": targetDir,
		"batchId":    batchID,
		"files":      files,
		"count":      len(files),
	})
}

func (h *BatchDeployHandler) StartDownloadRemoteDir(c *gin.Context) {
	var req struct {
		ServerID      uint     `json:"serverId"`
		RemotePath    string   `json:"remotePath" binding:"required"`
		LocalPath     string   `json:"localPath"`
		SelectedPaths []string `json:"selectedPaths"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 13010, "参数错误")
		return
	}

	localDir, err := downloadWorkspaceRootDir()
	if err != nil {
		response.Fail(c, 13010, err.Error())
		return
	}
	selectedPaths := normalizeRemoteSelectionPaths(req.SelectedPaths)
	jobID, err := downloadJobIDForRemotePath(req.RemotePath, selectedPaths)
	if err != nil {
		response.Fail(c, 13010, err.Error())
		return
	}
	versionNo, err := downloadVersionNoForRemoteSelection(req.RemotePath, selectedPaths)
	if err != nil {
		response.Fail(c, 13010, err.Error())
		return
	}
	if repository.DB != nil {
		used, err := usedArtifactFiles(repository.DB, versionNo)
		if err != nil {
			response.Fail(c, 13010, "读取制品使用状态失败: "+err.Error())
			return
		}
		if len(used) > 0 {
			response.Fail(c, 13010, "该下载版本已提交过更新，不能覆盖；如需再次更新请重新上传生成新版本")
			return
		}
	}
	if existing, ok := getDownloadJobSnapshot(jobID); ok && (existing.Status == "pending" || existing.Status == "running") {
		response.OK(c, existing)
		return
	}

	job := &downloadJob{
		ID:            jobID,
		Status:        "pending",
		RemotePath:    req.RemotePath,
		SelectedPaths: selectedPaths,
		LocalDir:      localDir,
		BatchID:       versionNo,
		StartedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	downloadJobStore.Lock()
	downloadJobStore.items[job.ID] = job
	downloadJobStore.Unlock()

	go runDownloadJob(job.ID, req.ServerID, req.RemotePath, selectedPaths, localDir)

	response.OK(c, job)
}

func (h *BatchDeployHandler) GetActiveDownloadJob(c *gin.Context) {
	var latest *downloadJob
	downloadJobStore.Lock()
	for _, job := range downloadJobStore.items {
		if job.Status != "pending" && job.Status != "running" {
			continue
		}
		if latest == nil || job.UpdatedAt.After(latest.UpdatedAt) {
			copy := copyDownloadJob(*job)
			latest = &copy
		}
	}
	downloadJobStore.Unlock()
	if latest == nil {
		response.OK(c, nil)
		return
	}
	response.OK(c, latest)
}

func downloadJobIDForRemotePath(remotePath string, selectedPaths []string) (string, error) {
	parts := []string{strings.TrimSpace(remotePath)}
	normalizedSelections := append([]string(nil), selectedPaths...)
	sort.Strings(normalizedSelections)
	parts = append(parts, normalizedSelections...)
	seed := strings.Join(parts, "\n")
	if strings.TrimSpace(seed) == "" {
		return "", fmt.Errorf("远程路径为空")
	}
	sum := sha256.Sum256([]byte(seed))
	return "dl-" + hex.EncodeToString(sum[:])[:24], nil
}

func downloadVersionNoForRemoteSelection(remotePath string, selectedPaths []string) (string, error) {
	var sourcePath string
	if len(selectedPaths) == 1 {
		sourcePath = selectedPaths[0]
	} else {
		sourcePath = remotePath
	}
	relativePath, err := packageRelativeLocalPath(sourcePath)
	if err != nil {
		return "", err
	}
	versionNo := safePathSegment(relativePath)
	if versionNo == "" {
		return "", fmt.Errorf("下载版本名称无效")
	}
	return versionNo, nil
}

func downloadWorkspaceDirForStart(remotePath string) (string, error) {
	return downloadWorkspaceRootDir()
}

func downloadWorkspaceRootDir() (string, error) {
	return batchUploadSourceRoot("download")
}

func isDownloadWorkspaceRoot(dir string) bool {
	root, err := downloadWorkspaceRootDir()
	if err != nil {
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	return filepath.Clean(rootAbs) == filepath.Clean(dirAbs)
}

func hasRunningDownloadJob(batchID string) bool {
	downloadJobStore.Lock()
	defer downloadJobStore.Unlock()
	for _, job := range downloadJobStore.items {
		if job.BatchID == batchID && (job.Status == "pending" || job.Status == "running") {
			return true
		}
	}
	return false
}

func removeDownloadJobsFromMemoryByBatchID(batchID string) {
	downloadJobStore.Lock()
	for id, job := range downloadJobStore.items {
		if job.BatchID == batchID {
			delete(downloadJobStore.items, id)
		}
	}
	downloadJobStore.Unlock()
}

func (h *BatchDeployHandler) CancelDownloadJob(c *gin.Context) {
	jobID := downloadJobIDFromRouteParam(c.Param("id"))
	if jobID == "" {
		response.Fail(c, 13016, "下载任务不存在")
		return
	}
	job, ok := getDownloadJobSnapshot(jobID)
	if !ok {
		response.Fail(c, 13016, "下载任务不存在")
		return
	}
	if job.Status != "pending" && job.Status != "running" {
		response.OK(c, job)
		return
	}

	downloadJobCancelStore.Lock()
	cancel := downloadJobCancelStore.items[jobID]
	downloadJobCancelStore.Unlock()
	if cancel != nil {
		cancel()
	}
	cancelDownloadJob(jobID, "用户取消下载任务")

	if job, ok := getDownloadJobSnapshot(jobID); ok {
		response.OK(c, job)
		return
	}
	response.OK(c, gin.H{"id": jobID, "status": "canceled"})
}

func getDownloadJobSnapshot(jobID string) (downloadJob, bool) {
	downloadJobStore.Lock()
	if job, ok := downloadJobStore.items[jobID]; ok {
		snapshot := copyDownloadJob(*job)
		downloadJobStore.Unlock()
		return snapshot, true
	}
	downloadJobStore.Unlock()
	return downloadJob{}, false
}

func copyDownloadJob(job downloadJob) downloadJob {
	if job.Files != nil {
		job.Files = append([]string(nil), job.Files...)
	}
	if job.SelectedPaths != nil {
		job.SelectedPaths = append([]string(nil), job.SelectedPaths...)
	}
	if job.Items != nil {
		job.Items = append([]downloadJobItem(nil), job.Items...)
	}
	return job
}

func (h *BatchDeployHandler) GetDownloadProgress(c *gin.Context) {
	jobID := downloadJobIDFromRouteParam(c.Param("id"))
	if snapshot, ok := getDownloadJobSnapshot(jobID); ok {
		response.OK(c, snapshot)
		return
	}
	response.Fail(c, 13011, "下载任务不存在")
}

func downloadJobIDFromRouteParam(value string) string {
	return strings.TrimPrefix(value, "/")
}

func runDownloadJob(jobID string, serverID uint, remotePath string, selectedPaths []string, localDir string) {
	ctx, cancel := context.WithCancel(context.Background())
	downloadJobCancelStore.Lock()
	downloadJobCancelStore.items[jobID] = cancel
	downloadJobCancelStore.Unlock()
	defer func() {
		downloadJobCancelStore.Lock()
		delete(downloadJobCancelStore.items, jobID)
		downloadJobCancelStore.Unlock()
	}()

	if job, ok := getDownloadJobSnapshot(jobID); ok && job.Status == "canceled" {
		return
	}
	updateDownloadJob(jobID, func(job *downloadJob) {
		job.Status = "running"
	})

	var sftpClient *sftp.Client
	var sshClient io.Closer
	var err error
	if serverID > 0 {
		sftpClient, sshClient, err = connectPackageServerSFTP(serverID)
	} else {
		sftpClient, sshClient, err = connectPackageDownloadSFTP()
	}
	if err != nil {
		failDownloadJob(jobID, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	remoteFS := sftpRemoteFS{client: sftpClient}
	targetDir, selectedDirStrip, err := resolveDownloadTarget(localDir, remotePath, selectedPaths, remoteFS)
	if err != nil {
		failDownloadJob(jobID, "解析下载目标失败: "+err.Error())
		return
	}
	updateDownloadJob(jobID, func(job *downloadJob) {
		job.LocalDir = targetDir
		job.TargetPath = targetDir
	})

	targetDir, files, err := downloadRemoteSelectionToLocalWithProgressContext(ctx, remoteFS, remotePath, selectedPaths, targetDir, selectedDirStrip, func(progress downloadProgress) {
		updateDownloadJob(jobID, func(job *downloadJob) {
			job.TotalFiles = progress.TotalFiles
			job.CompletedFiles = progress.CompletedFiles
			job.CurrentPath = progress.CurrentPath
			job.Items = append([]downloadJobItem(nil), progress.Items...)
		})
	})
	if err != nil {
		if errors.Is(err, errDownloadCanceled) {
			cancelDownloadJob(jobID, "用户取消下载任务")
			return
		}
		failDownloadJob(jobID, "下载远程目录失败: "+err.Error())
		return
	}
	versionNo := jobID
	if snapshot, ok := getDownloadJobSnapshot(jobID); ok && strings.TrimSpace(snapshot.BatchID) != "" {
		versionNo = snapshot.BatchID
	}
	var releaseMetadata *model.ReleaseMetadata
	if snapshot, ok := getDownloadJobSnapshot(jobID); ok {
		metadata := downloadedReleaseMetadata(snapshot.Items)
		releaseMetadata = &metadata
	}
	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:       versionNo,
		SourceType:      "download",
		SourceLabel:     "服务器下载",
		Status:          "available",
		LocalDir:        targetDir,
		TargetPath:      targetDir,
		RemotePath:      remotePath,
		ReleaseMetadata: releaseMetadata,
	}); err != nil {
		failDownloadJob(jobID, "制品版本入库失败: "+err.Error())
		return
	}

	updateDownloadJob(jobID, func(job *downloadJob) {
		job.Status = "success"
		job.TargetPath = targetDir
		job.Files = files
		job.Count = len(files)
		job.CurrentPath = ""
	})
}

func updateDownloadJob(jobID string, update func(*downloadJob)) {
	downloadJobStore.Lock()
	defer downloadJobStore.Unlock()
	if job, ok := downloadJobStore.items[jobID]; ok {
		update(job)
		job.UpdatedAt = time.Now()
	}
}

func failDownloadJob(jobID, message string) {
	updateDownloadJob(jobID, func(job *downloadJob) {
		job.Status = "failed"
		job.Error = message
	})
}

func cancelDownloadJob(jobID, message string) {
	updateDownloadJob(jobID, func(job *downloadJob) {
		job.Status = "canceled"
		job.Error = message
		job.CurrentPath = ""
		for i := range job.Items {
			if job.Items[i].Status == "running" || job.Items[i].Status == "pending" {
				job.Items[i].Status = "canceled"
				job.Items[i].Error = message
			}
		}
	})
}

func listArtifactSourceBatches(batchRoot string) ([]artifactSourceBatch, error) {
	var batches []artifactSourceBatch

	for _, source := range []string{"upload", "download", "artifact"} {
		sourceRoot := filepath.Join(batchRoot, source)
		items, err := scanSourceBatchRoot(sourceRoot, source)
		if err != nil {
			return nil, err
		}
		batches = append(batches, items...)
	}

	sort.Slice(batches, func(i, j int) bool {
		return batches[i].UpdatedAt.After(batches[j].UpdatedAt)
	})
	return batches, nil
}

func scanSourceBatchRoot(sourceRoot, sourceType string) ([]artifactSourceBatch, error) {
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []artifactSourceBatch{}, nil
		}
		return nil, err
	}

	label := map[string]string{
		"upload":   "本地上传",
		"download": "服务器下载",
		"artifact": "制品库选择",
	}[sourceType]
	if label == "" {
		label = sourceType
	}

	batches := make([]artifactSourceBatch, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		dir := filepath.Join(sourceRoot, entry.Name())
		// Resumable uploads publish only after validation and metadata persistence.
		if _, err := os.Stat(filepath.Join(dir, artifactUploadPendingMarker)); err == nil {
			continue
		}
		files, err := collectArtifactFilesFromDir(dir)
		errMessage := ""
		if err != nil {
			errMessage = err.Error()
			files = nil
		}
		status := "ready"
		if errMessage != "" {
			status = "failed"
		}
		batches = append(batches, artifactSourceBatch{
			BatchID:     entry.Name(),
			SourceType:  sourceType,
			SourceLabel: label,
			Status:      status,
			LocalDir:    dir,
			TargetPath:  dir,
			Files:       files,
			Count:       len(files),
			Error:       errMessage,
			UpdatedAt:   info.ModTime(),
		})
	}
	return batches, nil
}

type artifactVersionInput struct {
	// An explicit download re-import uses only manifests selected in that job;
	// cached files from earlier downloads must not supply stale business notes.
	ReleaseMetadata *model.ReleaseMetadata
	VersionNo       string
	SourceType      string
	SourceLabel     string
	Status          string
	LocalDir        string
	TargetPath      string
	RemotePath      string
	Error           string
}

type artifactVersionCounters struct {
	total      int
	deployable int
	matched    int
	valid      int
	invalid    int
	skipped    int
	unmatched  int
}

func persistArtifactVersionFromDir(input artifactVersionInput) (model.ArtifactVersion, []model.ArtifactVersionItem, error) {
	if repository.DB == nil {
		return model.ArtifactVersion{}, nil, fmt.Errorf("数据库未初始化")
	}
	input.VersionNo = strings.TrimSpace(input.VersionNo)
	if input.VersionNo == "" {
		input.VersionNo = batchIDFromTargetPath(input.TargetPath)
	}
	input.VersionNo = safePathSegment(input.VersionNo)
	if input.VersionNo == "" {
		return model.ArtifactVersion{}, nil, fmt.Errorf("版本号无效")
	}
	if strings.TrimSpace(input.TargetPath) == "" {
		input.TargetPath = input.LocalDir
	}
	if strings.TrimSpace(input.LocalDir) == "" {
		input.LocalDir = input.TargetPath
	}
	if strings.TrimSpace(input.SourceLabel) == "" {
		input.SourceLabel = artifactSourceLabel(input.SourceType)
	}
	if strings.TrimSpace(input.Status) == "" {
		input.Status = "available"
	}

	files, err := collectArtifactFilesFromDir(input.TargetPath)
	if err != nil {
		input.Status = "failed"
		input.Error = err.Error()
		files = nil
	}
	sort.Strings(files)

	apps, _, _ := repository.NewApplicationRepo().List(repository.AppListParams{PageSize: 1000})
	items, counters := buildArtifactVersionItems(input.VersionNo, input.TargetPath, files, apps)
	return persistPreparedArtifactVersion(input, items, counters)
}

// Keep persistence separate so resumable uploads can validate once before publishing.
func persistPreparedArtifactVersion(input artifactVersionInput, items []model.ArtifactVersionItem, counters artifactVersionCounters) (model.ArtifactVersion, []model.ArtifactVersionItem, error) {
	version := model.ArtifactVersion{
		ReleaseMetadata: service.ReadReleaseManifest(firstNonEmptyString(input.TargetPath, input.LocalDir)),
		VersionNo:       input.VersionNo,
		SourceType:      input.SourceType,
		SourceLabel:     input.SourceLabel,
		Status:          input.Status,
		LocalDir:        input.LocalDir,
		TargetPath:      input.TargetPath,
		RemotePath:      input.RemotePath,
		Count:           counters.total,
		DeployableCount: counters.deployable,
		MatchedCount:    counters.matched,
		ValidCount:      counters.valid,
		InvalidCount:    counters.invalid,
		SkippedCount:    counters.skipped,
		UnmatchedCount:  counters.unmatched,
		Error:           input.Error,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if input.ReleaseMetadata != nil {
		version.ReleaseMetadata = *input.ReleaseMetadata
	}

	tx := repository.DB.Begin()
	if tx.Error != nil {
		return model.ArtifactVersion{}, nil, tx.Error
	}
	var existing model.ArtifactVersion
	err := tx.Where("version_no = ?", version.VersionNo).First(&existing).Error
	if err == nil {
		version.ID = existing.ID
		version.CreatedAt = existing.CreatedAt
		// Keep the imported business information as an audit snapshot. Replacing
		// an artifact or refreshing matching must not rewrite a release's notes.
		if input.ReleaseMetadata == nil && (existing.ReleaseManifestJSON != "" || existing.ReleaseManifestError != "") {
			version.ReleaseMetadata = existing.ReleaseMetadata
		}
		version.UpdatedAt = time.Now()
		if err := tx.Save(&version).Error; err != nil {
			tx.Rollback()
			return model.ArtifactVersion{}, nil, err
		}
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.Create(&version).Error; err != nil {
			tx.Rollback()
			return model.ArtifactVersion{}, nil, err
		}
	} else {
		tx.Rollback()
		return model.ArtifactVersion{}, nil, err
	}

	if err := tx.Where("version_no = ?", version.VersionNo).Delete(&model.ArtifactVersionItem{}).Error; err != nil {
		tx.Rollback()
		return model.ArtifactVersion{}, nil, err
	}
	for i := range items {
		if err := tx.Create(&items[i]).Error; err != nil {
			tx.Rollback()
			return model.ArtifactVersion{}, nil, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return model.ArtifactVersion{}, nil, err
	}
	return version, items, nil
}

func downloadedReleaseMetadata(items []downloadJobItem) model.ReleaseMetadata {
	var manifest *downloadJobItem
	for i := range items {
		if items[i].Name != service.ReleaseManifestFileName {
			continue
		}
		if manifest != nil {
			return model.ReleaseMetadata{ReleaseManifestError: "本次下载包含多个 version.json，请按业务版本分别导入"}
		}
		manifest = &items[i]
	}
	if manifest == nil {
		return model.ReleaseMetadata{}
	}
	if manifest.Status != "success" && manifest.Status != "skipped" {
		return model.ReleaseMetadata{ReleaseManifestError: "version.json 下载失败，未导入业务版本信息: " + manifest.Error}
	}
	return service.ReadReleaseManifestFile(manifest.LocalPath)
}

func refreshArtifactVersion(version model.ArtifactVersion) (model.ArtifactVersion, []model.ArtifactVersionItem, error) {
	updated, items, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   version.VersionNo,
		SourceType:  version.SourceType,
		SourceLabel: version.SourceLabel,
		Status:      refreshArtifactVersionStatus(version.Status),
		LocalDir:    version.LocalDir,
		TargetPath:  firstNonEmptyString(version.TargetPath, version.LocalDir),
		RemotePath:  version.RemotePath,
		Error:       version.Error,
	})
	if err == nil {
		err = markArtifactUsage(&updated, items)
	}
	return updated, items, err
}

func refreshArtifactVersionStatus(status string) string {
	switch status {
	case "", "collecting", "running", "pending":
		return "available"
	default:
		return status
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildArtifactVersionItems(versionNo, sourceDir string, files []string, apps []model.Application) ([]model.ArtifactVersionItem, artifactVersionCounters) {
	items := make([]model.ArtifactVersionItem, 0, len(files))
	for _, fileName := range files {
		items = append(items, buildArtifactVersionItem(versionNo, sourceDir, fileName, apps))
	}
	return items, countArtifactVersionItems(items)
}

func countArtifactVersionItems(items []model.ArtifactVersionItem) artifactVersionCounters {
	counters := artifactVersionCounters{total: len(items)}
	for _, item := range items {
		if item.Deployable {
			counters.deployable++
		}
		if item.MatchStatus == "matched" && item.ValidateStatus == "valid" {
			counters.matched++
		}
		if item.ValidateStatus == "valid" {
			counters.valid++
		}
		if item.ValidateStatus == "invalid" {
			counters.invalid++
		}
		if item.MatchStatus == "skipped" {
			counters.skipped++
		}
		if item.MatchStatus == "unmatched" && item.ValidateStatus == "valid" {
			counters.unmatched++
		}
	}
	return counters
}

func buildArtifactVersionItem(versionNo, sourceDir, fileName string, apps []model.Application) model.ArtifactVersionItem {
	item := model.ArtifactVersionItem{
		VersionNo:      versionNo,
		FileName:       fileName,
		FileType:       artifactFileType(fileName),
		MatchStatus:    "unmatched",
		ValidateStatus: "invalid",
		StatusReason:   "未找到匹配的应用",
	}
	filePath, err := resolveArtifactPath(sourceDir, fileName)
	if err != nil {
		item.StatusReason = err.Error()
		return item
	}
	size, sha, shaErr := fileSHA256(filePath)
	if shaErr == nil {
		item.FileSizeBytes = size
		item.SHA256 = sha
	}

	validateErr := validateArtifact(filePath)
	if validateErr == nil {
		item.ValidateStatus = "valid"
	} else {
		item.ValidateStatus = "invalid"
		item.StatusReason = fmt.Sprintf("文件异常: %v", validateErr)
	}

	if isSQLArtifactFile(fileName) {
		item.FileType = "sql"
		item.MatchStatus = "skipped"
		if validateErr == nil {
			item.StatusReason = "SQL 制品，跳过应用匹配"
		}
		return item
	}

	result := (&BatchDeployHandler{}).matchFile(fileName, apps)
	if result.Matched {
		item.MatchStatus = "matched"
		item.AppID = result.AppID
		item.AppName = result.AppName
		item.AppType = result.AppType
		item.VueRole = result.VueRole
		item.AppCode = result.AppCode
		if result.AppType == "vue" {
			item.FileType = "vue"
		}
		if versionJSON, err := service.ExtractPackageBusinessVersion(filePath, result.AppType); err == nil {
			item.PackageVersionJSON = versionJSON
		}
		if validateErr == nil {
			item.Deployable = true
			item.StatusReason = result.MatchReason
		}
		return item
	}
	if validateErr == nil {
		item.StatusReason = result.MatchReason
	}
	return item
}

func artifactFileType(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".jar":
		return "jar"
	case ".zip":
		return "zip"
	default:
		return strings.TrimPrefix(ext, ".")
	}
}

func artifactSourceLabel(sourceType string) string {
	switch sourceType {
	case "upload":
		return "本地上传"
	case "download":
		return "服务器下载"
	case "artifact":
		return "制品库选择"
	default:
		return sourceType
	}
}

func batchIDFromTargetPath(targetPath string) string {
	root, err := batchUploadRootDir()
	if err == nil && strings.TrimSpace(targetPath) != "" {
		return batchIDFromSourceDir(root, targetPath)
	}
	return filepath.Base(targetPath)
}

func (h *BatchDeployHandler) ListPackageDownloadDir(c *gin.Context) {
	remotePath := c.Query("path")
	if strings.TrimSpace(remotePath) == "" {
		remotePath = packageDownloadPath()
	}

	sftpClient, sshClient, err := connectPackageDownloadSFTP()
	if err != nil {
		response.Fail(c, 13009, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	files, err := listRemoteDirFiles(sftpClient, remotePath)
	if err != nil {
		response.Fail(c, 13009, "读取目录失败: "+err.Error())
		return
	}

	response.OK(c, gin.H{"path": remotePath, "basePath": packageDownloadPath(), "files": files})
}

func (h *BatchDeployHandler) ListPackageDownloadLocalDir(c *gin.Context) {
	root, err := downloadWorkspaceRootDir()
	if err != nil {
		response.Fail(c, 13023, err.Error())
		return
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		response.Fail(c, 13023, "创建下载工作区失败: "+err.Error())
		return
	}
	if err := normalizeLegacyPackageDownloadDirs(root); err != nil {
		response.Fail(c, 13023, "整理历史下载目录失败: "+err.Error())
		return
	}

	queryPath := strings.TrimSpace(c.Query("path"))
	dir := root
	if queryPath != "" {
		queryPath = normalizeLegacyPackageDownloadQueryPath(root, queryPath)
		dir, err = resolveBatchWorkspaceDir(queryPath)
		if err != nil {
			response.Fail(c, 13023, err.Error())
			return
		}
		rel, relErr := filepath.Rel(root, dir)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			response.Fail(c, 13023, "目录超出下载工作区")
			return
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		response.Fail(c, 13023, "读取目录失败: "+err.Error())
		return
	}

	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		if dir == root && entry.IsDir() && isGeneratedBatchID(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(dir, entry.Name()),
			Size:    info.Size(),
			IsDir:   entry.IsDir(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return files[i].Name < files[j].Name
	})

	response.OK(c, gin.H{"root": root, "path": dir, "files": files})
}

func normalizeLegacyPackageDownloadDirs(root string) error {
	legacyRoot := filepath.Join(root, "root")
	entries, err := os.ReadDir(legacyRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		serverRoot := filepath.Join(legacyRoot, entry.Name())
		children, err := os.ReadDir(serverRoot)
		if err != nil {
			return err
		}
		for _, child := range children {
			src := filepath.Join(serverRoot, child.Name())
			dst := filepath.Join(root, child.Name())
			if err := mergeLegacyDownloadPath(src, dst); err != nil {
				return err
			}
		}
		_ = os.Remove(serverRoot)
	}
	_ = os.Remove(legacyRoot)
	return nil
}

func normalizeLegacyPackageDownloadQueryPath(root, queryPath string) string {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return queryPath
	}
	queryAbs, err := filepath.Abs(queryPath)
	if err != nil {
		return queryPath
	}
	legacyRoot := filepath.Join(rootAbs, "root")
	rel, err := filepath.Rel(legacyRoot, queryAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return queryPath
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) <= 1 {
		return rootAbs
	}
	return filepath.Join(append([]string{rootAbs}, parts[1:]...)...)
}

func mergeLegacyDownloadPath(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dstInfo, err := os.Stat(dst)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return os.Rename(src, dst)
	}
	if err != nil {
		return err
	}
	if srcInfo.IsDir() && dstInfo.IsDir() {
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := mergeLegacyDownloadPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return os.Remove(src)
	}
	return os.RemoveAll(src)
}

func (h *BatchDeployHandler) ListPackageServerDir(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	remotePath := c.DefaultQuery("path", "/")

	sftpClient, sshClient, err := connectPackageServerSFTP(uint(id))
	if err != nil {
		response.Fail(c, 13008, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	files, err := listRemoteDirFiles(sftpClient, remotePath)
	if err != nil {
		response.Fail(c, 13008, "读取目录失败: "+err.Error())
		return
	}

	response.OK(c, gin.H{"path": remotePath, "files": files})
}

func listRemoteDirFiles(sftpClient *sftp.Client, remotePath string) ([]fileInfo, error) {
	entries, err := sftpClient.ReadDir(remotePath)
	if err != nil {
		return nil, err
	}

	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		files = append(files, fileInfo{
			Name:    entry.Name(),
			Path:    path.Join(remotePath, entry.Name()),
			Size:    entry.Size(),
			IsDir:   entry.IsDir(),
			Mode:    entry.Mode().String(),
			ModTime: entry.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return files[i].Name < files[j].Name
	})
	return files, nil
}

// ListLocalDir lists files in a local directory on the server
func (h *BatchDeployHandler) ListLocalDir(c *gin.Context) {
	if c.Query("path") == "" && c.Query("batchId") == "" {
		response.Fail(c, 13002, "请指定目录路径")
		return
	}

	dir, err := resolveBatchSourceDir(c.Query("path"), c.Query("batchId"))
	if err != nil {
		response.Fail(c, 13002, err.Error())
		return
	}

	files, err := collectArtifactFilesFromDir(dir)
	if err != nil {
		response.Fail(c, 13002, "读取目录失败: "+err.Error())
		return
	}

	response.OK(c, gin.H{"path": dir, "files": files, "count": len(files)})
}

func (h *BatchDeployHandler) ListLocalBrowser(c *gin.Context) {
	root, err := batchUploadRootDir()
	if err != nil {
		response.Fail(c, 13007, err.Error())
		return
	}
	for _, source := range []string{"local", "download", "artifact"} {
		_ = os.MkdirAll(filepath.Join(root, source), 0755)
	}
	_ = os.MkdirAll(filepath.Join(root, "upload"), 0755)
	if strings.TrimSpace(c.Query("path")) == "" {
		response.OK(c, gin.H{"root": root, "path": root, "files": []fileInfo{}})
		return
	}
	dir, err := resolveBatchWorkspaceDir(c.Query("path"))
	if err != nil {
		response.Fail(c, 13007, err.Error())
		return
	}
	if dir == root {
		response.OK(c, gin.H{"root": root, "path": root, "files": []fileInfo{}})
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		response.Fail(c, 13007, "创建目录失败: "+err.Error())
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		response.Fail(c, 13007, "读取目录失败: "+err.Error())
		return
	}

	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(dir, entry.Name()),
			Size:    info.Size(),
			IsDir:   entry.IsDir(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return files[i].Name < files[j].Name
	})

	response.OK(c, gin.H{"root": root, "path": dir, "files": files})
}

type matchResult struct {
	Regions     []frontend.Region `json:"regions,omitempty"`
	VueRole     string            `json:"vueRole"`
	AppCode     string            `json:"appCode"`
	UpdateUsed  bool              `json:"updateUsed"`
	FileName    string            `json:"fileName"`
	AppName     string            `json:"appName"`
	AppType     string            `json:"appType"`
	AppID       uint              `json:"appId"`
	Matched     bool              `json:"matched"`
	Valid       bool              `json:"valid"`
	Skipped     bool              `json:"skipped"`
	MatchReason string            `json:"matchReason"`
}

// Match matches uploaded file names to applications and validates file integrity
func (h *BatchDeployHandler) Match(c *gin.Context) {
	var req struct {
		Files     []string `json:"files" binding:"required"`
		SourceDir string   `json:"sourceDir"`
		BatchID   string   `json:"batchId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 13003, "参数错误")
		return
	}

	sourceDir, err := resolveBatchSourceDir(req.SourceDir, req.BatchID)
	if err != nil {
		response.Fail(c, 13003, err.Error())
		return
	}
	batchID := strings.TrimSpace(req.BatchID)
	if batchID == "" {
		root, _ := batchUploadRootDir()
		batchID = batchIDFromSourceDir(root, sourceDir)
	}

	// Load all applications
	apps, _, _ := h.appRepo.List(repository.AppListParams{PageSize: 1000})

	var results []matchResult
	for _, fileName := range req.Files {
		safeName, err := safeArtifactFileName(fileName)
		if err != nil {
			results = append(results, matchResult{FileName: fileName, Valid: false, Matched: false, MatchReason: err.Error()})
			continue
		}
		fileName = safeName
		if isSQLArtifactFile(fileName) {
			results = append(results, matchResult{
				FileName:    fileName,
				Valid:       true,
				Skipped:     true,
				MatchReason: "SQL 制品，跳过微服务制品匹配",
			})
			continue
		}
		result := h.matchFile(fileName, apps)
		// Validate file integrity
		filePath, err := resolveArtifactPath(sourceDir, fileName)
		if err != nil {
			result.Valid = false
			result.Matched = false
			result.MatchReason = err.Error()
		} else if err := validateArtifact(filePath); err != nil {
			result.Valid = false
			result.Matched = false
			result.MatchReason = fmt.Sprintf("文件异常: %v", err)
		} else {
			result.Valid = true
			if result.Matched && result.AppType == "vue" {
				for _, app := range apps {
					if app.ID == result.AppID {
						regions, err := stages.InspectFrontendArtifact(filePath, app)
						if err != nil {
							result.Valid = false
							result.MatchReason = err.Error()
						} else {
							result.Regions = regions
						}
						break
					}
				}
			}
		}
		results = append(results, result)
	}

	if repository.DB != nil && batchID != "" {
		used, err := usedArtifactFiles(repository.DB, batchID)
		if err != nil {
			response.Fail(c, 13003, "读取制品使用状态失败: "+err.Error())
			return
		}
		for i := range results {
			results[i].UpdateUsed = used[results[i].FileName]
		}
	}
	matched := 0
	invalid := 0
	skipped := 0
	for _, r := range results {
		if r.Matched && r.Valid {
			matched++
		}
		if r.Skipped {
			skipped++
		}
		if !r.Valid {
			invalid++
		}
	}

	response.OK(c, gin.H{"results": results, "total": len(results), "matched": matched, "invalid": invalid, "skipped": skipped, "sourceDir": sourceDir, "batchId": batchID})
}

func (h *BatchDeployHandler) matchFile(fileName string, apps []model.Application) matchResult {
	result := matchResult{FileName: fileName}

	// Remove extension to get base name
	baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	ext := filepath.Ext(fileName)

	for _, app := range apps {
		if !app.Enabled || app.ValidateIdentity() != nil {
			continue
		}
		switch {
		case ext == ".jar" && app.AppType == "java":
			if app.AppName == baseName {
				result.Matched = true
				result.AppName = app.AppName
				result.AppType = app.AppType
				result.AppID = app.ID
				result.VueRole = app.VueRole
				result.AppCode = app.AppCode
				result.MatchReason = "Java 应用名匹配"
				return result
			}
		case ext == ".zip" && app.AppType == "vue":
			if app.AppName == baseName && (app.VueRole == "main" || app.VueRole == "standalone" || app.VueRole == "cdn") {
				result.Matched = true
				result.AppName = app.AppName
				result.AppType = app.AppType
				result.AppID = app.ID
				result.VueRole = app.VueRole
				result.AppCode = app.AppCode
				result.MatchReason = fmt.Sprintf("Vue %s 应用名匹配", app.VueRole)
				return result
			}
			if app.VueRole == "sub" && app.AppCode == baseName {
				result.Matched = true
				result.AppName = app.AppName
				result.AppType = app.AppType
				result.AppID = app.ID
				result.VueRole = app.VueRole
				result.AppCode = app.AppCode
				result.MatchReason = fmt.Sprintf("Vue 子应用 appCode=%s 匹配", app.AppCode)
				return result
			}
		}
	}

	result.MatchReason = "未找到匹配的应用"
	return result
}

// Execute creates pipelines for matched applications
func (h *BatchDeployHandler) Execute(c *gin.Context) {
	var req struct {
		SourceDir string `json:"sourceDir"` // Directory containing artifacts
		BatchID   string `json:"batchId"`
		Items     []struct {
			FileName string `json:"fileName"`
			AppID    uint   `json:"appId"`
		} `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 13004, "参数错误")
		return
	}

	if len(req.Items) == 0 {
		response.Fail(c, 13004, "请选择要部署的应用")
		return
	}

	sourceDir, err := resolveBatchSourceDir(req.SourceDir, req.BatchID)
	if err != nil {
		response.Fail(c, 13004, err.Error())
		return
	}

	username := middleware.GetCurrentUsername(c)
	namespace, nsErr := k8s.GetDefaultNamespace()
	if nsErr != nil {
		response.Fail(c, 13004, nsErr.Error())
		return
	}
	batchID := strings.TrimSpace(req.BatchID)
	if batchID == "" {
		if root, rootErr := batchUploadRootDir(); rootErr == nil {
			batchID = batchIDFromSourceDir(root, sourceDir)
		}
	}
	if batchID == "" {
		batchID = newBatchID()
	}
	pipelineDeployMode := "artifact_image"
	var pipelines []gin.H
	var errors []string

	preparationFailed := func(p *model.Pipeline, cause error) {
		p.Status = "FAILED"
		p.ErrorStage = "PREPARE_UPDATE"
		p.ErrorMessage = fmt.Sprintf("准备更新失败：应用=%s；制品=%s；原因=%v；其他部署任务继续，修复后请重新上传", p.AppName, p.ArtifactName, cause)
		now := time.Now()
		p.EndTime = &now
		if err := h.pipelineRepo.Update(p); err != nil {
			errors = append(errors, fmt.Sprintf("%s: 保存失败详情失败: %v；原始错误: %v", p.AppName, err, cause))
		}

		if err := service.MarkPipelineFailed(p.ID, p.ErrorMessage); err != nil {
			errors = append(errors, fmt.Sprintf("%s: 保存准备失败明细失败: %v；原始错误: %v", p.AppName, err, cause))
		}
	}

	appByID := map[uint]model.Application{}
	var frontendSummary []string
	var webArtifacts []model.FrontendArtifact
	seenApps := map[uint]bool{}
	seenFiles := map[string]bool{}
	seenCodes := map[string]bool{}
	invalidApps := map[uint]bool{}
	invalidSharedWeb := false
	invalidate := func(app *model.Application, message string) {
		errors = append(errors, message)
		invalidApps[app.ID] = true
		if app.SharedFrontend() {
			invalidSharedWeb = true
		}
	}

	// Separate web-main related items from others
	var webMainItem *struct {
		FileName string
		AppID    uint
	}
	var subAppItems []struct {
		FileName string
		AppID    uint
		AppCode  string
	}
	var otherItems []struct {
		FileName string
		AppID    uint
	}

	for _, item := range req.Items {
		app, err := h.appRepo.FindByID(item.AppID)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: 读取应用失败: %v", item.FileName, err))
			continue
		}
		appByID[app.ID] = *app
		fileName, err := safeArtifactFileName(item.FileName)
		if err != nil {
			invalidate(app, fmt.Sprintf("%s: %s", item.FileName, err.Error()))
			continue
		}
		item.FileName = fileName

		if err := validateExecuteArtifact(sourceDir, item.FileName, *app); err != nil {
			invalidate(app, fmt.Sprintf("%s: %s", item.FileName, err.Error()))
			continue
		}

		if seenApps[app.ID] || seenFiles[item.FileName] {
			invalidate(app, fmt.Sprintf("%s: 重复选择应用或制品", item.FileName))
			continue
		}
		seenApps[app.ID], seenFiles[item.FileName] = true, true
		if app.AppType == "vue" {
			role := "独立应用"
			if app.VueRole == "main" {
				role = "主应用"
			} else if app.VueRole == "sub" {
				role = "子应用"
			}
			frontendSummary = append(frontendSummary, fmt.Sprintf("%s：%s；appCode=%s；制品=%s", role, app.AppName, app.AppCode, item.FileName))
		}
		if app.SharedFrontend() {
			key := app.VueRole
			if app.VueRole == "sub" {
				key += ":" + app.AppCode
			}
			if seenCodes[key] {
				invalidate(app, "web-main 更新组：同一批次不能包含多个主应用或相同 appCode 的子应用")
				continue
			}
			seenCodes[key] = true
			artifactPath, err := resolveArtifactPath(sourceDir, item.FileName)
			if err != nil {
				invalidate(app, fmt.Sprintf("%s: 定位制品失败: %v", item.FileName, err))
				continue
			}
			_, hash, err := fileSHA256(artifactPath)
			if err != nil {
				invalidate(app, fmt.Sprintf("%s: 读取制品失败: %v", item.FileName, err))
				continue
			}
			webArtifacts = append(webArtifacts, model.FrontendArtifact{AppID: app.ID, AppName: app.AppName, FileName: item.FileName, Role: app.VueRole, AppCode: app.AppCode, SHA256: hash})
		}
		if app.AppType == "vue" && app.VueRole == "main" {
			webMainItem = &struct {
				FileName string
				AppID    uint
			}{item.FileName, item.AppID}
		} else if app.AppType == "vue" && (app.VueRole == "sub" || app.VueRole == "cdn") {
			subAppItems = append(subAppItems, struct {
				FileName string
				AppID    uint
				AppCode  string
			}{item.FileName, item.AppID, app.AppCode})
		} else {
			otherItems = append(otherItems, struct {
				FileName string
				AppID    uint
			}{item.FileName, item.AppID})
		}
	}

	if !invalidSharedWeb && len(webArtifacts) > 0 {
		if err := validateFrontendSelectionFromSource(sourceDir, webArtifacts); err != nil {
			invalidSharedWeb = true
			errors = append(errors, err.Error())
		}
	}
	// Each failed application is skipped without blocking other targets.
	if invalidSharedWeb {
		webMainItem, subAppItems, webArtifacts = nil, nil, nil
		errors = append(errors, "web-main 更新组校验失败，主应用及本次所选子应用均跳过；其他独立服务继续")
	}
	validOthers := otherItems[:0]
	for _, item := range otherItems {
		if !invalidApps[item.AppID] {
			validOthers = append(validOthers, item)
		}
	}
	otherItems = validOthers
	if webMainItem == nil && len(subAppItems) == 0 && len(otherItems) == 0 {
		response.OKWithMessage(c, "制品校验失败，未创建更新任务", gin.H{"pipelines": pipelines, "errors": errors, "consumedFiles": []string{}})
		return
	}

	// Consumption is atomic within each deployment target, independent across targets.
	consumedFiles := []string{}
	claimGroup := func(files []string) bool {
		if err := claimArtifactUpdates(batchID, sourceDir, files); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", strings.Join(files, "、"), err))
			return false
		}
		consumedFiles = append(consumedFiles, files...)
		return true
	}
	var sharedFiles []string
	for _, artifact := range webArtifacts {
		sharedFiles = append(sharedFiles, artifact.FileName)
	}
	if len(sharedFiles) > 0 && !claimGroup(sharedFiles) {
		webMainItem, subAppItems, webArtifacts = nil, nil, nil
	}
	claimedOthers := otherItems[:0]
	for _, item := range otherItems {
		if claimGroup([]string{item.FileName}) {
			claimedOthers = append(claimedOthers, item)
		}
	}
	otherItems = claimedOthers
	if len(consumedFiles) == 0 {
		response.Fail(c, 13004, strings.Join(errors, "；"))
		return
	}

	webArtifactsJSON, err := json.Marshal(webArtifacts)
	if err != nil {
		response.Fail(c, 13004, "保存前端制品清单失败")
		return
	}

	type deployTarget struct {
		app    model.Application
		refs   []deployRecordRef
		shared bool
		note   string
	}
	var targets []deployTarget
	if webMainItem != nil || len(subAppItems) > 0 {
		target := deployTarget{shared: true}
		if webMainItem != nil {
			target.app = appByID[webMainItem.AppID]
			target.refs = append(target.refs, deployRecordRef{FileName: webMainItem.FileName, AppID: webMainItem.AppID})
		} else {
			target.app = appByID[subAppItems[0].AppID]
		}
		for _, sub := range subAppItems {
			target.refs = append(target.refs, deployRecordRef{FileName: sub.FileName, AppID: sub.AppID})
		}
		target.note = fmt.Sprintf("合并 %d 个制品更新 web-main", len(target.refs))
		targets = append(targets, target)
	}
	for _, item := range otherItems {
		targets = append(targets, deployTarget{app: appByID[item.AppID], refs: []deployRecordRef{{FileName: item.FileName, AppID: item.AppID}}})
	}
	// Use the applications already validated above. Create target records before
	// touching its workspace, so preparation failures remain in batch results.
	for _, target := range targets {
		var fileNames []string
		for _, ref := range target.refs {
			fileNames = append(fileNames, ref.FileName)
		}
		targetLabel := strings.Join(fileNames, "、")
		app := target.app
		p := &model.Pipeline{
			PipelineNo: h.pipelineRepo.GenerateNo(app.AppName), ApplicationID: app.ID,
			AppName: app.AppName, AppType: app.AppType, GitBranch: "manual-upload",
			Status: "PENDING", TriggerUser: username, ArtifactName: target.refs[0].FileName,
			BatchID: batchID, DeployMode: pipelineDeployMode, K8sNamespace: namespace,
			FrontendUpdateSummary: strings.Join(frontendSummary, "\n"),
		}
		if target.shared {
			p.FrontendArtifactsJSON = string(webArtifactsJSON)
		} else if app.AppType == "vue" {
			artifactPath, pathErr := resolveArtifactPath(sourceDir, target.refs[0].FileName)
			if pathErr != nil {
				errors = append(errors, pathErr.Error())
				continue
			}
			_, hash, hashErr := fileSHA256(artifactPath)
			if hashErr != nil {
				errors = append(errors, hashErr.Error())
				continue
			}
			data, _ := json.Marshal([]model.FrontendArtifact{{AppID: app.ID, AppName: app.AppName, FileName: target.refs[0].FileName, Role: app.VueRole, SHA256: hash}})
			p.FrontendArtifactsJSON = string(data)
		}
		if err := h.pipelineRepo.Create(p); err != nil {
			errors = append(errors, fmt.Sprintf("%s: 创建 Pipeline 失败: %v", targetLabel, err))
			continue
		}
		if err := createDeployRecordsForPipeline(batchID, namespace, "two_step", username, *p, target.refs, appByID); err != nil {
			preparationFailed(p, err)
			errors = append(errors, fmt.Sprintf("%s: 创建部署记录失败: %v", targetLabel, err))
			continue
		}
		pipelineDir := fmt.Sprintf("./workspaces/%s/pipeline-%d/source", app.AppName, p.ID)
		if app.AppType == "java" {
			pipelineDir = filepath.Join(pipelineDir, "build", "libs")
		}
		copyErr := os.MkdirAll(pipelineDir, 0755)
		if copyErr != nil {
			copyErr = fmt.Errorf("创建制品目录 %s 失败: %w", pipelineDir, copyErr)
		}
		for _, ref := range target.refs {
			if copyErr != nil {
				break
			}
			if err := copyArtifactFromSource(sourceDir, ref.FileName, filepath.Join(pipelineDir, ref.FileName)); err != nil {
				copyErr = fmt.Errorf("复制制品 %s 失败: %w", ref.FileName, err)
			}
		}
		if copyErr != nil {
			preparationFailed(p, copyErr)
			errors = append(errors, fmt.Sprintf("%s: %v", targetLabel, copyErr))
			continue
		}
		scheduler.DefaultScheduler.Enqueue(p.ID)
		pipelines = append(pipelines, gin.H{"id": p.ID, "no": p.PipelineNo, "app": app.AppName, "note": target.note})
	}

	response.OKWithMessage(c, fmt.Sprintf("已创建 %d 个制品更新任务", len(pipelines)), gin.H{
		"pipelines":     pipelines,
		"errors":        errors,
		"consumedFiles": consumedFiles,
	})
}

func createDeployRecordsForPipeline(versionNo, namespace, deployMode, username string, pipeline model.Pipeline, refs []deployRecordRef, appByID map[uint]model.Application) error {
	if len(refs) == 0 {
		return nil
	}
	items := make([]model.ArtifactVersionItem, 0, len(refs))
	apps := make([]model.Application, 0, len(refs))
	seenApps := map[uint]bool{}
	for _, ref := range refs {
		var item model.ArtifactVersionItem
		if err := repository.DB.
			Where("version_no = ? AND app_id = ? AND file_name = ?", versionNo, ref.AppID, ref.FileName).
			First(&item).Error; err != nil {
			return fmt.Errorf("%s 未找到版本明细", ref.FileName)
		}
		items = append(items, item)
		if !seenApps[ref.AppID] {
			apps = append(apps, appByID[ref.AppID])
			seenApps[ref.AppID] = true
		}
	}

	return service.CreateArtifactDeployRecords(service.ArtifactDeployRecordInput{
		VersionNo:    versionNo,
		Namespace:    namespace,
		DeployMode:   deployMode,
		TriggerUser:  username,
		Pipeline:     pipeline,
		VersionItems: items,
		Applications: apps,
	})
}

func copyArtifactFromSource(sourceDir, fileName, dst string) error {
	src, err := resolveArtifactPath(sourceDir, fileName)
	if err != nil {
		return err
	}
	return copyFileSimple(src, dst)
}

func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

type remoteArtifactFS interface {
	ReadDir(path string) ([]os.FileInfo, error)
	Stat(path string) (os.FileInfo, error)
	Open(path string) (io.ReadCloser, error)
}

type remoteSeekableFS interface {
	OpenAt(path string, offset int64) (io.ReadCloser, error)
}

type sftpRemoteFS struct {
	client interface {
		ReadDir(string) ([]os.FileInfo, error)
		Stat(string) (os.FileInfo, error)
		Open(string) (*sftp.File, error)
	}
}

func connectPackageServerSFTP(serverID uint) (*sftp.Client, *sshclient.Client, error) {
	server, err := repository.NewServerRepo().FindByID(serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("软件包下载服务器不存在")
	}
	sshClient, err := sshclient.Connect(server)
	if err != nil {
		return nil, nil, err
	}
	sftpClient, err := sshClient.NewRawSFTPClient()
	if err != nil {
		sshClient.Close()
		return nil, nil, err
	}
	return sftpClient, sshClient, nil
}

func connectPackageDownloadSFTP() (*sftp.Client, *sshclient.Client, error) {
	repo := repository.NewSettingsRepo()
	hostValue, _ := repo.GetByKey("package_download_host")
	username, _ := repo.GetByKey("package_download_user")
	password, _ := repo.GetByKey("package_download_password")
	key, _ := repo.GetByKey("package_download_key")
	return connectPackageDownloadSFTPWithConfig(hostValue, username, password, key)
}

func connectPackageDownloadSFTPWithConfig(hostValue, username, password, key string) (*sftp.Client, *sshclient.Client, error) {
	server, err := packageDownloadRemoteServer(hostValue, username, password, key)
	if err != nil {
		return nil, nil, err
	}

	sshClient, err := sshclient.Connect(server)
	if err != nil {
		return nil, nil, err
	}
	sftpClient, err := sshClient.NewRawSFTPClient()
	if err != nil {
		sshClient.Close()
		return nil, nil, err
	}
	return sftpClient, sshClient, nil
}

func packageDownloadRemoteServer(hostValue, username, password, key string) (*model.RemoteServer, error) {
	host, port, err := parsePackageDownloadHost(hostValue)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(username) == "" {
		return nil, fmt.Errorf("未配置软件包下载服务器用户名")
	}

	authType := "password"
	credential := strings.TrimSpace(password)
	if strings.TrimSpace(key) != "" {
		authType = "ssh_key"
		credential = strings.TrimSpace(key)
	}
	if credential == "" {
		return nil, fmt.Errorf("未配置软件包下载服务器密码或 Key")
	}

	return &model.RemoteServer{
		Name:                "package-download",
		Host:                host,
		Port:                port,
		Username:            strings.TrimSpace(username),
		AuthType:            authType,
		CredentialEncrypted: mustEncryptPlainCredential(credential),
	}, nil
}

func parsePackageDownloadHost(value string) (string, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0, fmt.Errorf("未配置软件包下载服务器地址")
	}
	if strings.Contains(value, ":") {
		host, portValue, err := net.SplitHostPort(value)
		if err != nil && !strings.HasPrefix(value, "[") {
			parts := strings.Split(value, ":")
			if len(parts) == 2 {
				host = parts[0]
				portValue = parts[1]
				err = nil
			}
		}
		if err != nil {
			return "", 0, fmt.Errorf("软件包下载服务器地址格式错误")
		}
		port, err := strconv.Atoi(portValue)
		if err != nil || port <= 0 || port > 65535 {
			return "", 0, fmt.Errorf("软件包下载服务器端口无效")
		}
		if strings.TrimSpace(host) == "" {
			return "", 0, fmt.Errorf("软件包下载服务器地址格式错误")
		}
		return host, port, nil
	}
	return value, 22, nil
}

func packageDownloadPath() string {
	if repository.DB == nil {
		return "/"
	}
	value, _ := repository.NewSettingsRepo().GetByKey("package_download_path")
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	return value
}

func mustEncryptPlainCredential(value string) string {
	encrypted, err := crypto.Encrypt(value)
	if err != nil {
		return ""
	}
	return encrypted
}

func (fs sftpRemoteFS) ReadDir(remotePath string) ([]os.FileInfo, error) {
	return fs.client.ReadDir(remotePath)
}

func (fs sftpRemoteFS) Stat(remotePath string) (os.FileInfo, error) {
	return fs.client.Stat(remotePath)
}

func (fs sftpRemoteFS) Open(remotePath string) (io.ReadCloser, error) {
	return fs.client.Open(remotePath)
}

func (fs sftpRemoteFS) OpenAt(remotePath string, offset int64) (io.ReadCloser, error) {
	file, err := fs.client.Open(remotePath)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func collectArtifactFilesFromDir(root string) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".jar" && ext != ".zip" {
			return nil
		}
		safeName, err := safeArtifactFileName(name)
		if err != nil {
			return err
		}
		if seen[safeName] {
			return fmt.Errorf("目录内存在同名制品文件: %s", safeName)
		}
		seen[safeName] = true
		files = append(files, safeName)
		return nil
	})
	return files, err
}

func importArtifactsFromRemoteDir(fs remoteArtifactFS, remoteDir, destDir string) ([]string, error) {
	seen := map[string]bool{}
	var files []string

	var walk func(string) error
	walk = func(dir string) error {
		entries, err := fs.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			remotePath := path.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := walk(remotePath); err != nil {
					return err
				}
				continue
			}

			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext != ".jar" && ext != ".zip" && entry.Name() != service.ReleaseManifestFileName {
				continue
			}
			fileName, err := safeUploadFileName(entry.Name())
			if err != nil {
				return err
			}
			if seen[fileName] {
				return fmt.Errorf("远程目录内存在同名制品文件: %s", fileName)
			}
			seen[fileName] = true
			if fileName == service.ReleaseManifestFileName && entry.Size() > service.MaxReleaseManifestBytes {
				return fmt.Errorf("version.json 不能超过 1 MiB")
			}

			src, err := fs.Open(remotePath)
			if err != nil {
				return err
			}
			dstPath := filepath.Join(destDir, fileName)
			if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
				src.Close()
				return err
			}
			dst, err := os.Create(dstPath)
			if err != nil {
				src.Close()
				return err
			}
			_, copyErr := io.Copy(dst, src)
			closeErr := dst.Close()
			src.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if fileName == service.ReleaseManifestFileName {
				continue
			}
			if err := validateArtifact(dstPath); err != nil {
				os.Remove(dstPath)
				return fmt.Errorf("%s 文件异常: %v", fileName, err)
			}
			files = append(files, fileName)
		}
		return nil
	}

	if err := walk(remoteDir); err != nil {
		return nil, err
	}
	return files, nil
}

func redownloadArtifactItemFromRemote(fs remoteArtifactFS, remoteRoot, targetDir, fileName string) error {
	safeName, err := safeArtifactFileName(fileName)
	if err != nil {
		return err
	}
	remotePath, err := findRemoteArtifactByName(fs, remoteRoot, safeName)
	if err != nil {
		return err
	}
	localPath := filepath.Join(targetDir, safeName)
	if err := copyRemoteFile(fs, remotePath, localPath, -1); err != nil {
		return err
	}
	if err := validateArtifact(localPath); err != nil {
		_ = os.Remove(localPath)
		return fmt.Errorf("%s 文件异常: %v", safeName, err)
	}
	return nil
}

func findRemoteArtifactByName(fs remoteArtifactFS, remoteRoot, fileName string) (string, error) {
	var matches []string
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := fs.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			remotePath := path.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := walk(remotePath); err != nil {
					return err
				}
				continue
			}
			if entry.Name() == fileName {
				matches = append(matches, remotePath)
			}
		}
		return nil
	}
	if err := walk(remoteRoot); err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("远程目录中未找到 %s", fileName)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("远程目录中存在多个 %s", fileName)
	}
	return matches[0], nil
}

func downloadRemoteDirToLocal(fs remoteArtifactFS, remoteDir, localParent string) (string, []string, error) {
	return downloadRemoteDirToLocalWithProgress(fs, remoteDir, localParent, nil)
}

func downloadRemoteDirToLocalWithProgress(fs remoteArtifactFS, remoteDir, localParent string, onProgress func(downloadProgress)) (string, []string, error) {
	return downloadRemoteDirToLocalWithProgressContext(context.Background(), fs, remoteDir, localParent, onProgress)
}

func downloadRemoteDirToLocalWithProgressContext(ctx context.Context, fs remoteArtifactFS, remoteDir, localParent string, onProgress func(downloadProgress)) (string, []string, error) {
	targetDir, selectedStrip, err := resolveDownloadTarget(localParent, remoteDir, nil, fs)
	if err != nil {
		return "", nil, err
	}
	return downloadRemoteSelectionToLocalWithProgressContext(ctx, fs, remoteDir, nil, targetDir, selectedStrip, onProgress)
}

func downloadRemoteSelectionToLocalWithProgressContext(ctx context.Context, fs remoteArtifactFS, remoteDir string, selectedPaths []string, localParent string, selectedStrip string, onProgress func(downloadProgress)) (string, []string, error) {
	cleanRemoteDir := cleanRemoteDownloadPath(remoteDir)
	targetDir := localParent
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", nil, err
	}

	items, err := collectRemoteDownloadSelectionItems(fs, cleanRemoteDir, selectedPaths, targetDir, selectedStrip)
	if err != nil {
		return "", nil, err
	}
	files, err := downloadRemoteItemsToLocal(ctx, fs, items, onProgress)
	if err != nil {
		return "", nil, err
	}
	return targetDir, files, nil
}

func resolveDownloadTarget(localParent, remoteDir string, selectedPaths []string, fs remoteArtifactFS) (string, string, error) {
	cleanRemote := cleanRemoteDownloadPath(remoteDir)
	base, err := packageRelativeLocalPath(cleanRemote)
	if err != nil {
		return "", "", err
	}

	if len(selectedPaths) == 1 {
		selectedPaths = normalizeRemoteSelectionPaths(selectedPaths)
		if len(selectedPaths) == 1 {
			rel, err := relativeRemoteDownloadPath(cleanRemote, selectedPaths[0])
			if err != nil {
				return "", "", err
			}
			if rel != "" && !strings.Contains(rel, "/") {
				if info, err := fs.Stat(selectedPaths[0]); err == nil && info.IsDir() {
					selectedBase, err := packageRelativeLocalPath(selectedPaths[0])
					if err != nil {
						return "", "", err
					}
					return filepath.Join(localParent, filepath.FromSlash(selectedBase)), filepath.Base(selectedPaths[0]), nil
				}
			}
		}
	}

	if base == "" {
		return localParent, "", nil
	}
	return filepath.Join(localParent, filepath.FromSlash(base)), "", nil
}

func packageRelativeLocalPath(remotePath string) (string, error) {
	return packageRelativeLocalPathWithRoot(remotePath, packageDownloadPath())
}

func packageRelativeLocalPathWithRoot(remotePath, packageRoot string) (string, error) {
	cleanRemote := cleanRemoteDownloadPath(remotePath)
	if cleanRemote == "" {
		return "", fmt.Errorf("远程路径不能为空")
	}
	cleanRoot := cleanRemoteDownloadPath(packageRoot)
	if cleanRoot == "" {
		cleanRoot = "/"
	}

	var rel string
	if cleanRoot != "/" {
		if cleanRemote == cleanRoot {
			rel = ""
		} else if candidate, err := relativeRemoteDownloadPath(cleanRoot, cleanRemote); err == nil {
			rel = candidate
		} else if candidate := relativePathAfterRootName(cleanRoot, cleanRemote); candidate != "" {
			rel = candidate
		} else {
			rel = path.Base(cleanRemote)
		}
	} else {
		rel = strings.TrimPrefix(cleanRemote, "/")
		rel = trimServerAbsoluteAncestors(rel)
	}

	if rel == "" {
		return "", nil
	}
	cleanRel := path.Clean("/" + rel)
	cleanRel = strings.TrimPrefix(cleanRel, "/")
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
		return "", fmt.Errorf("远程路径非法: %s", remotePath)
	}
	return cleanRel, nil
}

func relativePathAfterRootName(packageRoot, remotePath string) string {
	rootName := path.Base(cleanRemoteDownloadPath(packageRoot))
	if rootName == "." || rootName == "/" || rootName == "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(cleanRemoteDownloadPath(remotePath), "/"), "/")
	for i, part := range parts {
		if part == rootName && i+1 < len(parts) {
			return strings.Join(parts[i+1:], "/")
		}
	}
	return ""
}

func trimServerAbsoluteAncestors(relativePath string) string {
	parts := strings.Split(strings.Trim(relativePath, "/"), "/")
	if len(parts) >= 3 && parts[0] == "root" {
		return strings.Join(parts[2:], "/")
	}
	return relativePath
}

func downloadRemoteItemsToLocal(ctx context.Context, fs remoteArtifactFS, items []downloadJobItem, onProgress func(downloadProgress)) ([]string, error) {
	totalFiles := len(items)
	completedFiles := precheckUnchangedDownloadItems(items)
	reportProgress := func(currentPath string) {
		if onProgress != nil {
			onProgress(downloadProgress{
				TotalFiles:     totalFiles,
				CompletedFiles: completedFiles,
				CurrentPath:    currentPath,
				Items:          copyDownloadJobItems(items),
			})
		}
	}
	reportProgress("")

	seenArtifacts := map[string]bool{}
	var artifacts []string
	var artifactFailures []string
	markDone := func(index int, status string, err error) {
		wasCompleted := isCompletedDownloadStatus(items[index].Status)
		items[index].Status = status
		if err != nil {
			items[index].Error = err.Error()
		} else {
			items[index].Error = ""
		}
		if !wasCompleted {
			completedFiles++
		}
		reportProgress(items[index].RemotePath)
	}
	recordArtifactFailure := func(index int, err error) {
		artifactFailures = append(artifactFailures, fmt.Sprintf("%s: %v", items[index].Name, err))
		markDone(index, "failed", err)
	}

	for i := range items {
		if err := ctx.Err(); err != nil {
			items[i].Status = "canceled"
			items[i].Error = errDownloadCanceled.Error()
			reportProgress(items[i].RemotePath)
			return nil, errDownloadCanceled
		}
		matched := items[i].Status == "skipped"
		if !matched {
			items[i].Status = "running"
			reportProgress(items[i].RemotePath)

			var err error
			matched, err = remoteFileMatchesLocal(ctx, fs, items[i].RemotePath, items[i].LocalPath, items[i].Size, parseDownloadJobModTime(items[i].ModTime))
			if err != nil {
				if errors.Is(err, errDownloadCanceled) {
					items[i].Status = "canceled"
					items[i].Error = errDownloadCanceled.Error()
					reportProgress(items[i].RemotePath)
					return nil, err
				}
				if items[i].Artifact {
					recordArtifactFailure(i, err)
				} else {
					markDone(i, "failed", err)
				}
				continue
			}

			if !matched {
				if err := copyRemoteFileContext(ctx, fs, items[i].RemotePath, items[i].LocalPath, items[i].Size, parseDownloadJobModTime(items[i].ModTime)); err != nil {
					if errors.Is(err, errDownloadCanceled) {
						items[i].Status = "canceled"
						items[i].Error = errDownloadCanceled.Error()
						reportProgress(items[i].RemotePath)
						return nil, err
					}
					if items[i].Artifact {
						recordArtifactFailure(i, err)
					} else {
						markDone(i, "failed", err)
					}
					continue
				}
			}
		}

		if !items[i].Artifact {
			status := "success"
			if matched {
				status = "skipped"
			}
			markDone(i, status, nil)
			continue
		}
		fileName, err := safeArtifactFileName(items[i].Name)
		if err != nil {
			recordArtifactFailure(i, err)
			continue
		}
		if seenArtifacts[fileName] {
			err := fmt.Errorf("远程目录内存在同名制品文件: %s", fileName)
			recordArtifactFailure(i, err)
			continue
		}
		if err := validateArtifact(items[i].LocalPath); err != nil {
			os.Remove(items[i].LocalPath)
			wrappedErr := fmt.Errorf("%s 文件异常: %v", fileName, err)
			recordArtifactFailure(i, wrappedErr)
			continue
		}
		seenArtifacts[fileName] = true
		artifacts = append(artifacts, fileName)
		status := "success"
		if matched {
			status = "skipped"
		}
		markDone(i, status, nil)
	}
	reportProgress("")

	if len(artifactFailures) > 0 {
		return artifacts, errors.New(strings.Join(artifactFailures, "；"))
	}
	return artifacts, nil
}

func collectRemoteDownloadItems(fs remoteArtifactFS, remoteDir, targetDir string) ([]downloadJobItem, error) {
	var items []downloadJobItem
	var walk func(string, string) error
	walk = func(currentRemote, currentLocal string) error {
		entries, err := fs.ReadDir(currentRemote)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			remotePath := path.Join(currentRemote, entry.Name())
			localPath := filepath.Join(currentLocal, entry.Name())
			if entry.IsDir() {
				if err := walk(remotePath, localPath); err != nil {
					return err
				}
				continue
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			items = append(items, downloadJobItem{
				Name:       entry.Name(),
				RemotePath: remotePath,
				LocalPath:  localPath,
				Size:       entry.Size(),
				ModTime:    formatDownloadJobModTime(entry.ModTime()),
				Status:     "pending",
				Artifact:   ext == ".jar" || ext == ".zip",
			})
		}
		return nil
	}
	if err := walk(remoteDir, targetDir); err != nil {
		return nil, err
	}
	return items, nil
}

func collectRemoteDownloadSelectionItems(fs remoteArtifactFS, remoteDir string, selectedPaths []string, targetDir string, selectedStrip string) ([]downloadJobItem, error) {
	selectedPaths = normalizeRemoteSelectionPaths(selectedPaths)
	if len(selectedPaths) == 0 {
		return collectRemoteDownloadItems(fs, remoteDir, targetDir)
	}

	itemsByRemotePath := map[string]downloadJobItem{}
	for _, selectedPath := range selectedPaths {
		cleanSelected := cleanRemoteDownloadPath(selectedPath)
		rel, err := relativeRemoteDownloadPath(remoteDir, cleanSelected)
		if err != nil {
			return nil, err
		}
		info, err := fs.Stat(cleanSelected)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			localDir := targetDir
			if rel != "" && rel != selectedStrip {
				localDir = filepath.Join(targetDir, filepath.FromSlash(rel))
			}
			childItems, err := collectRemoteDownloadItems(fs, cleanSelected, localDir)
			if err != nil {
				return nil, err
			}
			for _, item := range childItems {
				itemsByRemotePath[item.RemotePath] = item
			}
			continue
		}
		if rel == "" {
			rel = path.Base(cleanSelected)
		}
		item := remoteDownloadFileItem(cleanSelected, filepath.Join(targetDir, filepath.FromSlash(rel)), info)
		itemsByRemotePath[item.RemotePath] = item
	}

	items := make([]downloadJobItem, 0, len(itemsByRemotePath))
	for _, item := range itemsByRemotePath {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RemotePath < items[j].RemotePath })
	return items, nil
}

func remoteDownloadFileItem(remotePath, localPath string, info os.FileInfo) downloadJobItem {
	name := info.Name()
	if name == "" {
		name = path.Base(remotePath)
	}
	ext := strings.ToLower(filepath.Ext(name))
	return downloadJobItem{
		Name:       name,
		RemotePath: remotePath,
		LocalPath:  localPath,
		Size:       info.Size(),
		ModTime:    formatDownloadJobModTime(info.ModTime()),
		Status:     "pending",
		Artifact:   ext == ".jar" || ext == ".zip",
	}
}

func normalizeRemoteSelectionPaths(paths []string) []string {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(paths))
	for _, value := range paths {
		clean := cleanRemoteDownloadPath(value)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		normalized = append(normalized, clean)
	}
	sort.Strings(normalized)
	return normalized
}

func cleanRemoteDownloadPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value)
}

func relativeRemoteDownloadPath(remoteDir, selectedPath string) (string, error) {
	remoteDir = cleanRemoteDownloadPath(remoteDir)
	selectedPath = cleanRemoteDownloadPath(selectedPath)
	if remoteDir == "" || selectedPath == "" {
		return "", fmt.Errorf("远程路径不能为空")
	}
	if selectedPath == remoteDir {
		return "", nil
	}
	prefix := remoteDir
	if prefix != "/" {
		prefix += "/"
	}
	if !strings.HasPrefix(selectedPath, prefix) {
		return "", fmt.Errorf("选择路径不在远程目录内: %s", selectedPath)
	}
	return strings.TrimPrefix(selectedPath, prefix), nil
}

func copyDownloadJobItems(items []downloadJobItem) []downloadJobItem {
	if items == nil {
		return nil
	}
	return append([]downloadJobItem(nil), items...)
}

func countRemoteFiles(fs remoteArtifactFS, remoteDir string) (int, error) {
	entries, err := fs.ReadDir(remoteDir)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, entry := range entries {
		remotePath := path.Join(remoteDir, entry.Name())
		if entry.IsDir() {
			count, err := countRemoteFiles(fs, remotePath)
			if err != nil {
				return 0, err
			}
			total += count
			continue
		}
		total++
	}
	return total, nil
}

func copyRemoteFile(fs remoteArtifactFS, remotePath, localPath string, remoteSize int64) error {
	return copyRemoteFileContext(context.Background(), fs, remotePath, localPath, remoteSize, time.Time{})
}

func precheckUnchangedDownloadItems(items []downloadJobItem) int {
	completed := 0
	for i := range items {
		remoteModTime := parseDownloadJobModTime(items[i].ModTime)
		if localFileMatchesRemoteSizeAndModTime(items[i].LocalPath, items[i].Size, remoteModTime) {
			items[i].Status = "skipped"
			items[i].Error = ""
			completed++
		}
	}
	return completed
}

func isCompletedDownloadStatus(status string) bool {
	return status == "success" || status == "skipped" || status == "failed" || status == "canceled"
}

func remoteFileMatchesLocal(ctx context.Context, fs remoteArtifactFS, remotePath, localPath string, remoteSize int64, remoteModTime time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, errDownloadCanceled
	}
	info, err := os.Stat(localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if remoteSize >= 0 && info.Size() != remoteSize {
		return false, nil
	}
	if sameDownloadFileModTime(info.ModTime(), remoteModTime) {
		return true, nil
	}
	_, localSHA, err := fileSHA256(localPath)
	if err != nil {
		return false, err
	}
	remoteSHA, err := remoteFileSHA256(ctx, fs, remotePath)
	if err != nil {
		return false, err
	}
	return localSHA == remoteSHA, nil
}

func localFileMatchesRemoteSizeAndModTime(localPath string, remoteSize int64, remoteModTime time.Time) bool {
	if remoteModTime.IsZero() {
		return false
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return false
	}
	if remoteSize >= 0 && info.Size() != remoteSize {
		return false
	}
	return sameDownloadFileModTime(info.ModTime(), remoteModTime)
}

func sameDownloadFileModTime(localModTime, remoteModTime time.Time) bool {
	if remoteModTime.IsZero() {
		return false
	}
	return localModTime.Unix() == remoteModTime.Unix()
}

func formatDownloadJobModTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseDownloadJobModTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func remoteFileSHA256(ctx context.Context, fs remoteArtifactFS, remotePath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errDownloadCanceled
	}
	src, err := fs.Open(remotePath)
	if err != nil {
		return "", err
	}
	defer src.Close()

	h := sha256.New()
	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", errDownloadCanceled
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := h.Write(buf[:n]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyRemoteFileContext(ctx context.Context, fs remoteArtifactFS, remotePath, localPath string, remoteSize int64, remoteModTime time.Time) error {
	if err := ctx.Err(); err != nil {
		return errDownloadCanceled
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}

	partPath := localPath + ".part"
	if info, err := os.Stat(partPath); err == nil && remoteSize >= 0 && info.Size() > remoteSize {
		if err := os.Remove(partPath); err != nil {
			return err
		}
	}

	offset := int64(0)
	if info, err := os.Stat(partPath); err == nil {
		offset = info.Size()
	}

	if remoteSize >= 0 && offset == remoteSize {
		return finishDownloadedRemoteFile(partPath, localPath, remoteModTime)
	}

	src, err := openRemoteFileAt(fs, remotePath, offset)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if _, err := dst.Seek(offset, io.SeekStart); err != nil {
		dst.Close()
		return err
	}

	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			dst.Close()
			_ = os.Remove(partPath)
			return errDownloadCanceled
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				dst.Close()
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			dst.Close()
			return readErr
		}
	}
	if err := dst.Close(); err != nil {
		return err
	}

	if remoteSize >= 0 {
		info, err := os.Stat(partPath)
		if err != nil {
			return err
		}
		if info.Size() != remoteSize {
			return fmt.Errorf("下载文件大小不一致: %s", remotePath)
		}
	}
	return finishDownloadedRemoteFile(partPath, localPath, remoteModTime)
}

func finishDownloadedRemoteFile(partPath, localPath string, remoteModTime time.Time) error {
	if err := os.Rename(partPath, localPath); err != nil {
		return err
	}
	if !remoteModTime.IsZero() {
		if err := os.Chtimes(localPath, remoteModTime, remoteModTime); err != nil {
			return err
		}
	}
	return nil
}

func openRemoteFileAt(fs remoteArtifactFS, remotePath string, offset int64) (io.ReadCloser, error) {
	if offset <= 0 {
		return fs.Open(remotePath)
	}
	if seekable, ok := fs.(remoteSeekableFS); ok {
		return seekable.OpenAt(remotePath, offset)
	}
	src, err := fs.Open(remotePath)
	if err != nil {
		return nil, err
	}
	if _, err := io.CopyN(io.Discard, src, offset); err != nil {
		src.Close()
		return nil, err
	}
	return src, nil
}

func safeLocalFolderName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
		return ""
	}
	if name == ".." {
		return ""
	}
	return name
}

func resolveArtifactPath(sourceDir, fileName string) (string, error) {
	safeName, err := safeArtifactFileName(fileName)
	if err != nil {
		return "", err
	}

	flatPath := filepath.Join(sourceDir, safeName)
	if info, err := os.Stat(flatPath); err == nil && !info.IsDir() {
		return flatPath, nil
	}

	var matches []string
	err = filepath.WalkDir(sourceDir, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == safeName {
			matches = append(matches, filePath)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("文件不存在或无法访问: %s", safeName)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("目录内存在同名制品文件: %s", safeName)
	}
	return matches[0], nil
}

func newBatchID() string {
	var b [4]byte
	timestamp := batchIDTimestamp(time.Now())
	if _, err := rand.Read(b[:]); err != nil {
		return timestamp
	}
	return timestamp + "-" + hex.EncodeToString(b[:])
}

func isGeneratedBatchID(value string) bool {
	if len(value) != 23 || value[14] != '-' {
		return false
	}
	for _, ch := range value[:14] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	for _, ch := range value[15:] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

func batchIDTimestamp(t time.Time) string {
	return t.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("20060102150405")
}

// Only the upload/import boundary accepts this reserved metadata file.
// Deployment inputs continue to require a JAR or ZIP.
func safeUploadFileName(name string) (string, error) {
	if filepath.Base(strings.ReplaceAll(name, "\\", "/")) == service.ReleaseManifestFileName {
		return service.ReleaseManifestFileName, nil
	}
	return safeArtifactFileName(name)
}

func safeArtifactFileName(name string) (string, error) {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("文件名无效")
	}
	ext := strings.ToLower(filepath.Ext(base))
	if ext != ".jar" && ext != ".zip" {
		return "", fmt.Errorf("不支持的文件格式: %s", ext)
	}
	return base, nil
}

func batchUploadRootDir() (string, error) {
	return filepath.Abs(filepath.Join(platformBaseDir(), batchUploadRelativeRoot))
}

func batchUploadSourceRoot(source string) (string, error) {
	if strings.TrimSpace(source) == "" || strings.ContainsAny(source, `/\`) {
		return "", fmt.Errorf("工作区来源无效")
	}
	root, err := batchUploadRootDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, source)
	return dir, os.MkdirAll(dir, 0755)
}

func newBatchWorkspaceDir(source string) (string, error) {
	root, err := batchUploadSourceRoot(source)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, newBatchID())
	return dir, os.MkdirAll(dir, 0755)
}

func batchArtifactWorkspaceDir(batchID string) (string, error) {
	if safePathSegment(batchID) == "" {
		return "", fmt.Errorf("批次编号无效")
	}
	root, err := batchUploadSourceRoot("artifact")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, safePathSegment(batchID))
	return dir, os.MkdirAll(dir, 0755)
}

func artifactLibraryRootDir() string {
	return filepath.Join(platformBaseDir(), artifactRelativeRoot)
}

func platformBaseDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func resolveBatchWorkspaceDir(path string) (string, error) {
	root, err := batchUploadRootDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		if err := os.MkdirAll(root, 0755); err != nil {
			return "", err
		}
		return root, nil
	}
	dir, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !isPathInside(root, dir) {
		return "", fmt.Errorf("目录超出批量上传工作区")
	}
	return dir, nil
}

func resolveBatchSourceDir(sourceDir, batchID string) (string, error) {
	root, err := batchUploadRootDir()
	if err != nil {
		return "", err
	}

	var dir string
	if batchID != "" {
		if strings.ContainsAny(batchID, `/\`) || strings.TrimSpace(batchID) == "" {
			return "", fmt.Errorf("批次编号无效")
		}
		dir = filepath.Join(root, batchID)
		if _, err := os.Stat(dir); err != nil {
			for _, source := range []string{"upload", "local", "download", "artifact"} {
				candidate := filepath.Join(root, source, batchID)
				if _, statErr := os.Stat(candidate); statErr == nil {
					dir = candidate
					break
				}
			}
		}
	} else if sourceDir != "" {
		dir, err = filepath.Abs(sourceDir)
		if err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("请指定批次编号或目录路径")
	}

	if !isPathInside(root, dir) {
		return "", fmt.Errorf("目录超出批量上传工作区")
	}
	return dir, nil
}

func batchIDFromSourceDir(root, sourceDir string) string {
	rel, err := filepath.Rel(root, sourceDir)
	if err != nil {
		return filepath.Base(sourceDir)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) >= 2 {
		switch parts[0] {
		case "upload", "local", "download", "artifact":
			if safePathSegment(parts[1]) != "" {
				return parts[1]
			}
		}
	}
	return filepath.Base(sourceDir)
}

func isPathInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func detectBatchSourceType(sourceDir string) string {
	root, err := batchUploadRootDir()
	if err != nil {
		return "unknown"
	}
	rel, err := filepath.Rel(root, sourceDir)
	if err != nil {
		return "unknown"
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 {
		return "unknown"
	}
	switch parts[0] {
	case "upload", "local":
		return "local_upload"
	case "download":
		return "server_download"
	case "artifact":
		return "artifact_reuse"
	default:
		return "unknown"
	}
}

func isSQLArtifactFile(name string) bool {
	lower := strings.ToLower(filepath.Base(name))
	return strings.HasSuffix(lower, "-sql.zip")
}

type artifactLibraryMeta struct {
	VersionPath   string
	LatestPath    string
	FileSizeBytes int64
	SHA256        string
}

func artifactLibraryPaths(root, appName, batchID, fileName string) (string, string, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", fmt.Errorf("制品库目录为空")
	}
	if strings.TrimSpace(appName) == "" {
		return "", "", fmt.Errorf("应用名为空")
	}
	if strings.TrimSpace(batchID) == "" {
		return "", "", fmt.Errorf("批次编号为空")
	}
	safeName, err := safeArtifactFileName(fileName)
	if err != nil {
		return "", "", err
	}
	safeApp := safePathSegment(appName)
	if safeApp == "" {
		return "", "", fmt.Errorf("应用名无效")
	}
	safeBatch := safePathSegment(batchID)
	if safeBatch == "" {
		return "", "", fmt.Errorf("批次编号无效")
	}
	versionPath := filepath.Join(root, "apps", safeApp, "versions", safeBatch, safeName)
	latestPath := filepath.Join(root, "apps", safeApp, "latest", safeName)
	return versionPath, latestPath, nil
}

func copyArtifactToLibrary(sourcePath, root, batchID string, app model.Application) (artifactLibraryMeta, error) {
	fileName := app.ArtifactName
	if strings.TrimSpace(fileName) == "" {
		fileName = filepath.Base(sourcePath)
	}
	versionPath, latestPath, err := artifactLibraryPaths(root, app.AppName, batchID, fileName)
	if err != nil {
		return artifactLibraryMeta{}, err
	}
	size, sha, err := fileSHA256(sourcePath)
	if err != nil {
		return artifactLibraryMeta{}, err
	}
	if err := copyFileSimple(sourcePath, versionPath); err != nil {
		return artifactLibraryMeta{}, err
	}
	if err := copyFileSimple(sourcePath, latestPath); err != nil {
		return artifactLibraryMeta{}, err
	}
	return artifactLibraryMeta{VersionPath: versionPath, LatestPath: latestPath, FileSizeBytes: size, SHA256: sha}, nil
}

func copyArtifactToWorkspace(sourcePath, workspaceDir, fileName string) (string, error) {
	safeName, err := safeArtifactFileName(fileName)
	if err != nil {
		return "", err
	}
	target := filepath.Join(workspaceDir, safeName)
	if err := copyFileSimple(sourcePath, target); err != nil {
		return "", err
	}
	return target, nil
}

func recordLatestArtifact(app model.Application, artifactName, sourceType, sourcePath, batchID string, meta artifactLibraryMeta) error {
	repo := repository.NewArtifactRepo()
	if err := repo.ClearLatest(app.AppName); err != nil {
		return err
	}
	return repo.Create(&model.Artifact{
		AppName:       app.AppName,
		ArtifactName:  artifactName,
		GitBranch:     "manual-upload",
		SourceType:    sourceType,
		SourcePath:    sourcePath,
		StoragePath:   meta.LatestPath,
		SHA256:        meta.SHA256,
		BatchID:       batchID,
		IsLatest:      true,
		FileSizeBytes: meta.FileSizeBytes,
	})
}

func pruneArtifactVersions(root, appName string, keep int) error {
	if keep <= 0 {
		return nil
	}
	safeApp := safePathSegment(appName)
	if safeApp == "" {
		return fmt.Errorf("应用名无效")
	}
	versionsDir := filepath.Join(root, "apps", safeApp, "versions")
	entries, err := os.ReadDir(versionsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type versionEntry struct {
		name    string
		path    string
		modTime time.Time
	}
	var versions []versionEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		versions = append(versions, versionEntry{
			name:    entry.Name(),
			path:    filepath.Join(versionsDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].modTime.Equal(versions[j].modTime) {
			return versions[i].name > versions[j].name
		}
		return versions[i].modTime.After(versions[j].modTime)
	})
	for i := keep; i < len(versions); i++ {
		if err := os.RemoveAll(versions[i].path); err != nil {
			return err
		}
	}
	return nil
}

func fileSHA256(filePath string) (int64, string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

func safePathSegment(value string) string {
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	if value == "." || value == string(filepath.Separator) || strings.TrimSpace(value) == "" || value == ".." {
		return ""
	}
	return value
}

func validateFrontendSelectionFromSource(sourceDir string, artifacts []model.FrontendArtifact) error {
	flat := true
	for _, artifact := range artifacts {
		name, err := safeArtifactFileName(artifact.FileName)
		if err != nil {
			return err
		}
		if name != artifact.FileName {
			return fmt.Errorf("前端包文件名不能包含目录")
		}
		info, err := os.Stat(filepath.Join(sourceDir, artifact.FileName))
		if err != nil || !info.Mode().IsRegular() {
			flat = false
		}
	}
	if flat {
		return stages.ValidateFrontendSelection(sourceDir, artifacts)
	}
	// A downloaded directory may retain nested paths. Resolve the same inputs
	// used by single-file validation, then validate the complete update group
	// with the display filenames that will be copied into its workspace.
	tmp, err := os.MkdirTemp("", "df-frontend-selection-inputs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, artifact := range artifacts {
		if err := copyArtifactFromSource(sourceDir, artifact.FileName, filepath.Join(tmp, artifact.FileName)); err != nil {
			return err
		}
	}
	return stages.ValidateFrontendSelection(tmp, artifacts)
}

func validateExecuteArtifact(sourceDir, fileName string, app model.Application) error {
	if !app.Enabled {
		return fmt.Errorf("应用已停用")
	}
	if err := app.ValidateIdentity(); err != nil {
		return err
	}
	fileName, err := safeArtifactFileName(fileName)
	if err != nil {
		return err
	}
	filePath, err := resolveArtifactPath(sourceDir, fileName)
	if err != nil {
		return err
	}
	if err := validateArtifact(filePath); err != nil {
		return fmt.Errorf("文件异常: %v", err)
	}

	result := (&BatchDeployHandler{}).matchFile(fileName, []model.Application{app})
	if !result.Matched {
		return fmt.Errorf("制品与应用不匹配")
	}
	if app.AppType == "vue" {
		return stages.ValidateFrontendArtifact(filePath, app)
	}
	return nil
}

// validateArtifact checks if a jar/zip file is valid and accessible
func validateArtifact(filePath string) error {
	// Check file exists and is readable
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("文件不存在或无法访问")
	}
	if info.Size() == 0 {
		return fmt.Errorf("文件大小为 0")
	}

	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".jar", ".zip":
		return frontend.ValidateArchive(filePath)
	default:
		return fmt.Errorf("不支持的文件格式: %s", ext)
	}
}
