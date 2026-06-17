package handler

import (
	"archive/zip"
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

	"df-build-server/internal/k8s"
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
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

type BatchDeployHandler struct {
	appRepo      *repository.ApplicationRepo
	pipelineRepo *repository.PipelineRepo
}

type deployRecordRef struct {
	FileName string
	AppID    uint
}

type downloadProgress struct {
	TotalFiles     int    `json:"totalFiles"`
	CompletedFiles int    `json:"completedFiles"`
	CurrentPath    string `json:"currentPath"`
}

type downloadJob struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	RemotePath     string    `json:"remotePath"`
	LocalDir       string    `json:"localDir"`
	TargetPath     string    `json:"targetPath"`
	BatchID        string    `json:"batchId"`
	Files          []string  `json:"files"`
	Count          int       `json:"count"`
	TotalFiles     int       `json:"totalFiles"`
	CompletedFiles int       `json:"completedFiles"`
	CurrentPath    string    `json:"currentPath"`
	Error          string    `json:"error"`
	HasPartial     bool      `json:"hasPartial"`
	StartedAt      time.Time `json:"startedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
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

func NewBatchDeployHandler() *BatchDeployHandler {
	return &BatchDeployHandler{
		appRepo:      repository.NewApplicationRepo(),
		pipelineRepo: repository.NewPipelineRepo(),
	}
}

func FinalizeOrphanedDownloadJobs(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, nil
	}
	result := db.Model(&model.DownloadJob{}).
		Where("status IN ?", []string{"pending", "running"}).
		Updates(map[string]any{
			"status":     "failed",
			"error":      "服务重启，下载任务已中断，可重新点击下载并断点续传",
			"updated_at": time.Now(),
		})
	return result.RowsAffected, result.Error
}

func (h *BatchDeployHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/batch-deploy")
	g.Use(middleware.AuthRequired())
	{
		g.POST("/upload", h.Upload)
		g.POST("/remote-dir", h.ImportRemoteDir)
		g.GET("/package-server/:id/list", h.ListPackageServerDir)
		g.GET("/package-download/list", h.ListPackageDownloadDir)
		g.POST("/download-remote-dir/start", h.StartDownloadRemoteDir)
		g.GET("/download-remote-dir/active", h.GetActiveDownloadJob)
		g.GET("/download-remote-dir/latest", h.GetLatestDownloadJob)
		g.GET("/download-remote-dir/batches", h.ListDownloadBatches)
		g.GET("/artifact-versions", h.ListArtifactVersions)
		g.GET("/artifact-versions/:versionNo", h.GetArtifactVersion)
		g.DELETE("/artifact-versions/:versionNo/items/:itemID", h.DeleteArtifactVersionItem)
		g.POST("/artifact-versions/:versionNo/items/:itemID/replace", h.ReplaceArtifactVersionItem)
		g.POST("/artifact-versions/:versionNo/items/:itemID/redownload", h.RedownloadArtifactVersionItem)
		g.GET("/deploy-batches", h.ListArtifactDeployBatches)
		g.GET("/deploy-batches/:batchNo", h.GetArtifactDeployBatch)
		g.POST("/deploy-batches/:batchNo/rollback", h.RollbackArtifactDeployBatch)
		g.GET("/source-batches", h.ListSourceBatches)
		g.POST("/download-remote-dir/batches/:batchId/retry", h.RetryDownloadBatch)
		g.DELETE("/download-remote-dir/batches/:batchId", h.ClearDownloadBatch)
		g.GET("/download-remote-dir/progress/:id", h.GetDownloadProgress)
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

	jobs := []model.DownloadJob{}
	if repository.DB != nil {
		_ = repository.DB.Order("updated_at DESC").Find(&jobs).Error
	}
	batches, err := listArtifactSourceBatches(root, jobs)
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
	if err := backfillArtifactVersionsFromWorkspace(); err != nil {
		response.Fail(c, 13019, "同步历史更新版本失败: "+err.Error())
		return
	}
	versions := []model.ArtifactVersion{}
	query := repository.DB.Order("updated_at DESC")
	if c.Query("deployable") == "true" {
		query = query.Where("status = ? AND deployable_count > 0", "available")
	}
	if err := query.Find(&versions).Error; err != nil {
		response.Fail(c, 13019, "读取更新版本失败: "+err.Error())
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
	response.OK(c, gin.H{"version": version, "items": items})
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
	return version, item, true
}

func (h *BatchDeployHandler) ListArtifactDeployBatches(c *gin.Context) {
	if repository.DB == nil {
		response.Fail(c, 13021, "数据库未初始化")
		return
	}
	batches := []model.ArtifactDeployBatch{}
	query := repository.DB.Order("updated_at DESC")
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
		response.OK(c, gin.H{"batch": nil, "records": []model.ArtifactDeployRecord{}})
		return
	}
	records := []model.ArtifactDeployRecord{}
	if err := repository.DB.Where("deploy_batch_no = ?", batchNo).Order("deployment_name ASC, app_name ASC").Find(&records).Error; err != nil {
		response.Fail(c, 13022, "读取部署记录失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"batch": batch, "records": records})
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
	if err := service.RollbackDeployBatch(c.Request.Context(), batchNo, service.NewK8sDeploymentRollbacker(), service.NewK8sRuntimeVersionReader()); err != nil {
		response.Fail(c, 13023, "整体回滚失败: "+err.Error())
		return
	}
	var batch model.ArtifactDeployBatch
	_ = repository.DB.Where("deploy_batch_no = ?", batchNo).First(&batch).Error
	records := []model.ArtifactDeployRecord{}
	_ = repository.DB.Where("deploy_batch_no = ?", batchNo).Order("deployment_name ASC, app_name ASC").Find(&records).Error
	response.OK(c, gin.H{"batch": batch, "records": records})
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
	jobs := []model.DownloadJob{}
	_ = repository.DB.Order("updated_at DESC").Find(&jobs).Error
	batches, err := listArtifactSourceBatches(root, jobs)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if batch.BatchID == "" || batch.TargetPath == "" {
			continue
		}
		var count int64
		if err := repository.DB.Model(&model.ArtifactVersion{}).Where("version_no = ?", batch.BatchID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
			VersionNo:   batch.BatchID,
			SourceType:  batch.SourceType,
			SourceLabel: batch.SourceLabel,
			Status:      artifactVersionStatusFromBatch(batch),
			LocalDir:    batch.LocalDir,
			TargetPath:  batch.TargetPath,
			Error:       batch.Error,
		}); err != nil {
			return err
		}
	}
	return nil
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

	for _, file := range files {
		fileName, err := safeArtifactFileName(file.Filename)
		if err != nil {
			failedFiles = append(failedFiles, uploadFailure{FileName: file.Filename, Error: err.Error()})
			continue
		}
		if seenNames[fileName] {
			failedFiles = append(failedFiles, uploadFailure{FileName: file.Filename, Error: "目录内存在同名制品文件"})
			continue
		}
		seenNames[fileName] = true

		dst := filepath.Join(uploadDir, fileName)
		if err := c.SaveUploadedFile(file, dst); err != nil {
			failedFiles = append(failedFiles, uploadFailure{FileName: fileName, Error: "保存文件失败"})
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

	if len(successFiles) > 0 {
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

	localDir, err := newBatchWorkspaceDir("download")
	if err != nil {
		response.Fail(c, 13006, err.Error())
		return
	}

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
		VersionNo:   filepath.Base(localDir),
		SourceType:  "download",
		SourceLabel: "服务器下载",
		Status:      "available",
		LocalDir:    localDir,
		TargetPath:  targetDir,
		RemotePath:  req.RemotePath,
	}); err != nil {
		response.Fail(c, 13006, "制品版本入库失败: "+err.Error())
		return
	}

	response.OK(c, gin.H{
		"targetPath": targetDir,
		"batchId":    filepath.Base(localDir),
		"files":      files,
		"count":      len(files),
	})
}

func (h *BatchDeployHandler) StartDownloadRemoteDir(c *gin.Context) {
	var req struct {
		ServerID   uint   `json:"serverId"`
		RemotePath string `json:"remotePath" binding:"required"`
		LocalPath  string `json:"localPath"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 13010, "参数错误")
		return
	}

	localDir, err := downloadWorkspaceDirForStart(req.RemotePath)
	if err != nil {
		response.Fail(c, 13010, err.Error())
		return
	}

	job := &downloadJob{
		ID:         newBatchID(),
		Status:     "pending",
		RemotePath: req.RemotePath,
		LocalDir:   localDir,
		BatchID:    filepath.Base(localDir),
		StartedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	downloadJobStore.Lock()
	downloadJobStore.items[job.ID] = job
	downloadJobStore.Unlock()
	persistDownloadJob(*job)

	go runDownloadJob(job.ID, req.ServerID, req.RemotePath, localDir)

	response.OK(c, job)
}

func (h *BatchDeployHandler) GetActiveDownloadJob(c *gin.Context) {
	var dbJob model.DownloadJob
	if repository.DB == nil {
		response.OK(c, nil)
		return
	}
	err := repository.DB.
		Where("status IN ?", []string{"pending", "running"}).
		Order("updated_at DESC").
		First(&dbJob).Error
	if err != nil {
		response.OK(c, nil)
		return
	}
	response.OK(c, downloadJobFromModel(dbJob))
}

func (h *BatchDeployHandler) GetLatestDownloadJob(c *gin.Context) {
	if repository.DB != nil {
		var dbJob model.DownloadJob
		err := repository.DB.
			Where("status = ?", "success").
			Order("updated_at DESC").
			First(&dbJob).Error
		if err == nil {
			response.OK(c, downloadJobFromModel(dbJob))
			return
		}
	}

	root, err := batchUploadRootDir()
	if err != nil {
		response.Fail(c, 13012, err.Error())
		return
	}
	if job, ok := latestDownloadJobFromWorkspace(root); ok {
		response.OK(c, job)
		return
	}
	response.OK(c, nil)
}

func (h *BatchDeployHandler) ListDownloadBatches(c *gin.Context) {
	root, err := batchUploadRootDir()
	if err != nil {
		response.Fail(c, 13013, err.Error())
		return
	}

	jobs := []model.DownloadJob{}
	if repository.DB != nil {
		_ = repository.DB.Order("updated_at DESC").Find(&jobs).Error
	}
	batches, err := listDownloadBatches(root, jobs)
	if err != nil {
		response.Fail(c, 13013, err.Error())
		return
	}
	response.OK(c, gin.H{"batches": batches})
}

func (h *BatchDeployHandler) RetryDownloadBatch(c *gin.Context) {
	batchID := safePathSegment(c.Param("batchId"))
	if batchID == "" {
		response.Fail(c, 13014, "批次编号无效")
		return
	}
	jobModel, err := findLatestDownloadJobByBatchID(batchID)
	if err != nil {
		response.Fail(c, 13014, "下载任务不存在")
		return
	}
	if jobModel.RemotePath == "" {
		response.Fail(c, 13014, "该批次没有远程目录信息，无法重试")
		return
	}
	if jobModel.Status == "pending" || jobModel.Status == "running" {
		response.Fail(c, 13014, "下载任务正在执行")
		return
	}
	localDir := jobModel.LocalDir
	if localDir == "" {
		root, err := batchUploadSourceRoot("download")
		if err != nil {
			response.Fail(c, 13014, err.Error())
			return
		}
		localDir = filepath.Join(root, batchID)
	}
	if err := validateDownloadBatchDir(localDir); err != nil {
		response.Fail(c, 13014, err.Error())
		return
	}

	job := &downloadJob{
		ID:         newBatchID(),
		Status:     "pending",
		RemotePath: jobModel.RemotePath,
		LocalDir:   localDir,
		BatchID:    batchID,
		StartedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	downloadJobStore.Lock()
	downloadJobStore.items[job.ID] = job
	downloadJobStore.Unlock()
	persistDownloadJob(*job)

	go runDownloadJob(job.ID, 0, job.RemotePath, localDir)
	response.OK(c, job)
}

func (h *BatchDeployHandler) ClearDownloadBatch(c *gin.Context) {
	batchID := safePathSegment(c.Param("batchId"))
	if batchID == "" {
		response.Fail(c, 13015, "批次编号无效")
		return
	}
	if hasRunningDownloadJob(batchID) {
		response.Fail(c, 13015, "下载任务正在执行，不能清理")
		return
	}
	localDir, err := downloadBatchDirByID(batchID)
	if err != nil {
		response.Fail(c, 13015, err.Error())
		return
	}
	if err := os.RemoveAll(localDir); err != nil {
		response.Fail(c, 13015, "清理下载目录失败: "+err.Error())
		return
	}
	removeDownloadJobsByBatchID(batchID)
	response.OK(c, gin.H{"batchId": batchID})
}

func downloadWorkspaceDirForStart(remotePath string) (string, error) {
	if repository.DB != nil {
		var failedJob model.DownloadJob
		err := repository.DB.
			Where("remote_path = ? AND status = ? AND local_dir <> ''", remotePath, "failed").
			Order("updated_at DESC").
			First(&failedJob).Error
		if err == nil {
			root, rootErr := batchUploadRootDir()
			localDir, absErr := filepath.Abs(failedJob.LocalDir)
			if rootErr == nil && absErr == nil && isPathInside(root, localDir) {
				if _, statErr := os.Stat(localDir); statErr == nil {
					return localDir, nil
				}
			}
		}
	}
	return newBatchWorkspaceDir("download")
}

func findLatestDownloadJobByBatchID(batchID string) (model.DownloadJob, error) {
	if repository.DB == nil {
		return model.DownloadJob{}, fmt.Errorf("数据库未初始化")
	}
	var job model.DownloadJob
	err := repository.DB.Where("batch_id = ?", batchID).Order("updated_at DESC").First(&job).Error
	return job, err
}

func validateDownloadBatchDir(localDir string) error {
	root, err := batchUploadRootDir()
	if err != nil {
		return err
	}
	absDir, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	if !isPathInside(filepath.Join(root, "download"), absDir) {
		return fmt.Errorf("下载目录超出批量下载工作区")
	}
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return err
	}
	return nil
}

func downloadBatchDirByID(batchID string) (string, error) {
	root, err := batchUploadSourceRoot("download")
	if err != nil {
		return "", err
	}
	localDir := filepath.Join(root, batchID)
	if repository.DB != nil {
		var job model.DownloadJob
		if err := repository.DB.Where("batch_id = ? AND local_dir <> ''", batchID).Order("updated_at DESC").First(&job).Error; err == nil {
			localDir = job.LocalDir
		}
	}
	if err := validateDownloadBatchDir(localDir); err != nil {
		return "", err
	}
	return localDir, nil
}

func hasRunningDownloadJob(batchID string) bool {
	downloadJobStore.Lock()
	for _, job := range downloadJobStore.items {
		if job.BatchID == batchID && (job.Status == "pending" || job.Status == "running") {
			downloadJobStore.Unlock()
			return true
		}
	}
	downloadJobStore.Unlock()
	if repository.DB == nil {
		return false
	}
	var count int64
	repository.DB.Model(&model.DownloadJob{}).
		Where("batch_id = ? AND status IN ?", batchID, []string{"pending", "running"}).
		Count(&count)
	return count > 0
}

func removeDownloadJobsByBatchID(batchID string) {
	downloadJobStore.Lock()
	for id, job := range downloadJobStore.items {
		if job.BatchID == batchID {
			delete(downloadJobStore.items, id)
		}
	}
	downloadJobStore.Unlock()
	if repository.DB != nil {
		repository.DB.Where("batch_id = ?", batchID).Delete(&model.DownloadJob{})
	}
}

func (h *BatchDeployHandler) GetDownloadProgress(c *gin.Context) {
	jobID := c.Param("id")
	downloadJobStore.Lock()
	job, ok := downloadJobStore.items[jobID]
	if ok {
		snapshot := *job
		if job.Files != nil {
			snapshot.Files = append([]string(nil), job.Files...)
		}
		downloadJobStore.Unlock()
		response.OK(c, snapshot)
		return
	}
	downloadJobStore.Unlock()

	var dbJob model.DownloadJob
	if repository.DB != nil && repository.DB.First(&dbJob, "id = ?", jobID).Error == nil {
		response.OK(c, downloadJobFromModel(dbJob))
		return
	}
	response.Fail(c, 13011, "下载任务不存在")
}

func runDownloadJob(jobID string, serverID uint, remotePath, localDir string) {
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

	targetDir, files, err := downloadRemoteDirToLocalWithProgress(sftpRemoteFS{client: sftpClient}, remotePath, localDir, func(progress downloadProgress) {
		updateDownloadJob(jobID, func(job *downloadJob) {
			job.TotalFiles = progress.TotalFiles
			job.CompletedFiles = progress.CompletedFiles
			job.CurrentPath = progress.CurrentPath
		})
	})
	if err != nil {
		failDownloadJob(jobID, "下载远程目录失败: "+err.Error())
		return
	}
	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   filepath.Base(localDir),
		SourceType:  "download",
		SourceLabel: "服务器下载",
		Status:      "available",
		LocalDir:    localDir,
		TargetPath:  targetDir,
		RemotePath:  remotePath,
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
		persistDownloadJob(*job)
		return
	}

	if repository.DB == nil {
		return
	}
	var dbJob model.DownloadJob
	if repository.DB.First(&dbJob, "id = ?", jobID).Error == nil {
		job := downloadJobFromModel(dbJob)
		update(&job)
		job.UpdatedAt = time.Now()
		persistDownloadJob(job)
	}
}

func failDownloadJob(jobID, message string) {
	updateDownloadJob(jobID, func(job *downloadJob) {
		job.Status = "failed"
		job.Error = message
	})
}

func persistDownloadJob(job downloadJob) {
	if repository.DB == nil {
		return
	}
	dbJob := downloadJobToModel(job)
	repository.DB.Save(&dbJob)
}

func downloadJobToModel(job downloadJob) model.DownloadJob {
	filesJSON, _ := json.Marshal(job.Files)
	return model.DownloadJob{
		ID:             job.ID,
		Status:         job.Status,
		RemotePath:     job.RemotePath,
		LocalDir:       job.LocalDir,
		TargetPath:     job.TargetPath,
		BatchID:        job.BatchID,
		FilesJSON:      string(filesJSON),
		Count:          job.Count,
		TotalFiles:     job.TotalFiles,
		CompletedFiles: job.CompletedFiles,
		CurrentPath:    job.CurrentPath,
		Error:          job.Error,
		StartedAt:      job.StartedAt,
		UpdatedAt:      job.UpdatedAt,
	}
}

func downloadJobFromModel(dbJob model.DownloadJob) downloadJob {
	files := []string{}
	if strings.TrimSpace(dbJob.FilesJSON) != "" {
		_ = json.Unmarshal([]byte(dbJob.FilesJSON), &files)
	}
	return downloadJob{
		ID:             dbJob.ID,
		Status:         dbJob.Status,
		RemotePath:     dbJob.RemotePath,
		LocalDir:       dbJob.LocalDir,
		TargetPath:     dbJob.TargetPath,
		BatchID:        dbJob.BatchID,
		Files:          files,
		Count:          dbJob.Count,
		TotalFiles:     dbJob.TotalFiles,
		CompletedFiles: dbJob.CompletedFiles,
		CurrentPath:    dbJob.CurrentPath,
		Error:          dbJob.Error,
		StartedAt:      dbJob.StartedAt,
		UpdatedAt:      dbJob.UpdatedAt,
	}
}

func latestDownloadJobFromWorkspace(batchRoot string) (downloadJob, bool) {
	downloadRoot := filepath.Join(batchRoot, "download")
	entries, err := os.ReadDir(downloadRoot)
	if err != nil {
		return downloadJob{}, false
	}

	var latestDir string
	var latestName string
	var latestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		batchDir := filepath.Join(downloadRoot, entry.Name())
		if hasPartialDownloadFile(batchDir) {
			continue
		}
		if latestDir == "" || info.ModTime().After(latestTime) {
			latestName = entry.Name()
			latestDir = batchDir
			latestTime = info.ModTime()
		}
	}
	if latestDir == "" {
		return downloadJob{}, false
	}

	targetPath := latestDownloadTargetDir(latestDir)
	files, err := collectArtifactFilesFromDir(targetPath)
	errorMessage := ""
	if err != nil {
		files = nil
		errorMessage = err.Error()
	}
	return downloadJob{
		ID:         latestName,
		Status:     "success",
		LocalDir:   latestDir,
		TargetPath: targetPath,
		BatchID:    latestName,
		Files:      files,
		Count:      len(files),
		Error:      errorMessage,
		HasPartial: false,
		StartedAt:  latestTime,
		UpdatedAt:  latestTime,
	}, true
}

func listDownloadBatches(batchRoot string, jobs []model.DownloadJob) ([]downloadJob, error) {
	downloadRoot := filepath.Join(batchRoot, "download")
	entries, err := os.ReadDir(downloadRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []downloadJob{}, nil
		}
		return nil, err
	}

	jobsByBatchID := latestJobsByBatchID(jobs)
	seen := map[string]bool{}
	batches := make([]downloadJob, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		batchID := entry.Name()
		batchDir := filepath.Join(downloadRoot, batchID)
		jobModel, hasJob := jobsByBatchID[batchID]
		job := downloadJob{
			ID:         batchID,
			Status:     "success",
			LocalDir:   batchDir,
			TargetPath: latestDownloadTargetDir(batchDir),
			BatchID:    batchID,
			StartedAt:  info.ModTime(),
			UpdatedAt:  info.ModTime(),
		}
		if hasJob {
			job = downloadJobFromModel(jobModel)
			if job.LocalDir == "" {
				job.LocalDir = batchDir
			}
			if job.TargetPath == "" {
				job.TargetPath = latestDownloadTargetDir(job.LocalDir)
			}
		}
		job.HasPartial = hasPartialDownloadFile(job.LocalDir)
		if !hasJob && job.HasPartial {
			job.Status = "failed"
			job.Error = "下载未完成，存在 .part 临时文件"
		}
		if job.TargetPath != "" && job.Status == "success" {
			if files, err := collectArtifactFilesFromDir(job.TargetPath); err == nil {
				job.Files = files
				job.Count = len(files)
			} else if job.Error == "" {
				job.Error = err.Error()
			}
		}
		batches = append(batches, job)
		seen[batchID] = true
	}

	for _, dbJob := range jobs {
		if dbJob.BatchID == "" || seen[dbJob.BatchID] {
			continue
		}
		job := downloadJobFromModel(dbJob)
		if job.LocalDir != "" {
			job.HasPartial = hasPartialDownloadFile(job.LocalDir)
			if job.TargetPath == "" {
				job.TargetPath = latestDownloadTargetDir(job.LocalDir)
			}
		}
		batches = append(batches, job)
	}

	sort.Slice(batches, func(i, j int) bool {
		return batches[i].UpdatedAt.After(batches[j].UpdatedAt)
	})
	return batches, nil
}

func listArtifactSourceBatches(batchRoot string, jobs []model.DownloadJob) ([]artifactSourceBatch, error) {
	var batches []artifactSourceBatch

	for _, source := range []string{"upload", "artifact"} {
		sourceRoot := filepath.Join(batchRoot, source)
		items, err := scanSourceBatchRoot(sourceRoot, source)
		if err != nil {
			return nil, err
		}
		batches = append(batches, items...)
	}

	downloadBatches, err := listDownloadBatches(batchRoot, jobs)
	if err != nil {
		return nil, err
	}
	for _, job := range downloadBatches {
		batches = append(batches, artifactSourceBatch{
			BatchID:     job.BatchID,
			SourceType:  "download",
			SourceLabel: "服务器下载",
			Status:      job.Status,
			LocalDir:    job.LocalDir,
			TargetPath:  job.TargetPath,
			Files:       job.Files,
			Count:       job.Count,
			Error:       job.Error,
			HasPartial:  job.HasPartial,
			UpdatedAt:   job.UpdatedAt,
		})
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
	VersionNo   string
	SourceType  string
	SourceLabel string
	Status      string
	LocalDir    string
	TargetPath  string
	RemotePath  string
	Error       string
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
	version := model.ArtifactVersion{
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

	tx := repository.DB.Begin()
	if tx.Error != nil {
		return model.ArtifactVersion{}, nil, tx.Error
	}
	var existing model.ArtifactVersion
	err = tx.Where("version_no = ?", version.VersionNo).First(&existing).Error
	if err == nil {
		version.ID = existing.ID
		version.CreatedAt = existing.CreatedAt
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

func refreshArtifactVersion(version model.ArtifactVersion) (model.ArtifactVersion, []model.ArtifactVersionItem, error) {
	return persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:    version.VersionNo,
		SourceType:   version.SourceType,
		SourceLabel:  version.SourceLabel,
		Status:       version.Status,
		LocalDir:     version.LocalDir,
		TargetPath:   firstNonEmptyString(version.TargetPath, version.LocalDir),
		RemotePath:   version.RemotePath,
		Error:        version.Error,
	})
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
	counters := artifactVersionCounters{total: len(files)}
	for _, fileName := range files {
		item := buildArtifactVersionItem(versionNo, sourceDir, fileName, apps)
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
		items = append(items, item)
	}
	return items, counters
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

func latestJobsByBatchID(jobs []model.DownloadJob) map[string]model.DownloadJob {
	out := map[string]model.DownloadJob{}
	for _, job := range jobs {
		if job.BatchID == "" {
			continue
		}
		existing, ok := out[job.BatchID]
		if !ok || job.UpdatedAt.After(existing.UpdatedAt) {
			out[job.BatchID] = job
		}
	}
	return out
}

func latestDownloadTargetDir(batchDir string) string {
	entries, err := os.ReadDir(batchDir)
	if err != nil {
		return batchDir
	}
	var latestDir string
	var latestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if latestDir == "" || info.ModTime().After(latestTime) {
			latestDir = filepath.Join(batchDir, entry.Name())
			latestTime = info.ModTime()
		}
	}
	if latestDir == "" {
		return batchDir
	}
	return latestDir
}

func hasPartialDownloadFile(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".part") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
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
	FileName    string `json:"fileName"`
	AppName     string `json:"appName"`
	AppType     string `json:"appType"`
	AppID       uint   `json:"appId"`
	Matched     bool   `json:"matched"`
	Valid       bool   `json:"valid"`
	Skipped     bool   `json:"skipped"`
	MatchReason string `json:"matchReason"`
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
		}
		results = append(results, result)
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
		switch {
		case ext == ".jar" && app.AppType == "java":
			if app.AppName == baseName {
				result.Matched = true
				result.AppName = app.AppName
				result.AppType = app.AppType
				result.AppID = app.ID
				result.MatchReason = "Java 应用名匹配"
				return result
			}
		case ext == ".zip" && app.AppType == "vue":
			if app.AppName == baseName && (app.VueRole == "main" || app.VueRole == "standalone") {
				result.Matched = true
				result.AppName = app.AppName
				result.AppType = app.AppType
				result.AppID = app.ID
				result.MatchReason = fmt.Sprintf("Vue %s 应用名匹配", app.VueRole)
				return result
			}
			if app.VueRole == "sub" && app.AppCode == baseName {
				result.Matched = true
				result.AppName = app.AppName
				result.AppType = app.AppType
				result.AppID = app.ID
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
		SourceDir  string `json:"sourceDir"` // Directory containing artifacts
		BatchID    string `json:"batchId"`
		Namespace  string `json:"namespace"`
		DeployMode string `json:"deployMode"`
		Items      []struct {
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
	namespace := strings.TrimSpace(req.Namespace)
	if namespace == "" {
		namespace = k8s.GetDefaultNamespace()
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
	pipelineDeployMode := "artifact_deploy"
	if strings.TrimSpace(req.DeployMode) == "cutover" {
		pipelineDeployMode = "manual"
	}
	var pipelines []gin.H
	var errors []string

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
		fileName, err := safeArtifactFileName(item.FileName)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %s", item.FileName, err.Error()))
			continue
		}
		item.FileName = fileName

		app, err := h.appRepo.FindByID(item.AppID)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: 应用不存在", item.FileName))
			continue
		}
		if err := validateExecuteArtifact(sourceDir, item.FileName, *app); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %s", item.FileName, err.Error()))
			continue
		}

		if app.AppType == "vue" && app.VueRole == "main" {
			webMainItem = &struct {
				FileName string
				AppID    uint
			}{item.FileName, item.AppID}
		} else if app.AppType == "vue" && app.VueRole == "sub" {
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

	// Handle web-main + sub-apps as a single pipeline.
	if webMainItem != nil {
		app, _ := h.appRepo.FindByID(webMainItem.AppID)
		p := &model.Pipeline{
			PipelineNo:    h.pipelineRepo.GenerateNo(app.AppName),
			ApplicationID: app.ID,
			AppName:       app.AppName,
			AppType:       app.AppType,
			GitBranch:     "manual-upload",
			Status:        "PENDING",
			TriggerUser:   username,
			ArtifactName:  webMainItem.FileName,
			BatchID:       batchID,
			DeployMode:    pipelineDeployMode,
			K8sNamespace:  namespace,
		}

		if err := h.pipelineRepo.Create(p); err != nil {
			errors = append(errors, fmt.Sprintf("%s: 创建 Pipeline 失败", webMainItem.FileName))
		} else {
			pipelineDir := fmt.Sprintf("./workspaces/%s/pipeline-%d/source", app.AppName, p.ID)
			os.MkdirAll(pipelineDir, 0755)
			copyOK := true

			if err := copyArtifactFromSource(sourceDir, webMainItem.FileName, filepath.Join(pipelineDir, webMainItem.FileName)); err != nil {
				h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
				errors = append(errors, fmt.Sprintf("%s: 复制制品失败: %v", webMainItem.FileName, err))
				copyOK = false
			}

			if copyOK {
				for _, sub := range subAppItems {
					if err := copyArtifactFromSource(sourceDir, sub.FileName, filepath.Join(pipelineDir, sub.FileName)); err != nil {
						h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
						errors = append(errors, fmt.Sprintf("%s: 复制制品失败: %v", sub.FileName, err))
						copyOK = false
						break
					}
				}
			}

			if copyOK {
				refs := []deployRecordRef{{FileName: webMainItem.FileName, AppID: webMainItem.AppID}}
				for _, sub := range subAppItems {
					refs = append(refs, deployRecordRef{FileName: sub.FileName, AppID: sub.AppID})
				}
				if err := createDeployRecordsForPipeline(batchID, namespace, req.DeployMode, username, *p, refs); err != nil {
					h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
					errors = append(errors, fmt.Sprintf("%s: 创建部署记录失败: %v", app.AppName, err))
				} else {
					scheduler.DefaultScheduler.Enqueue(p.ID)
					pipelines = append(pipelines, gin.H{"id": p.ID, "no": p.PipelineNo, "app": app.AppName, "note": "包含子应用"})
				}
			}
		}
	} else {
		// No web-main in batch: sub-apps share one web-main image update pipeline.
		if len(subAppItems) > 0 {
			firstSub := subAppItems[0]
			app, _ := h.appRepo.FindByID(firstSub.AppID)
			p := &model.Pipeline{
				PipelineNo:    h.pipelineRepo.GenerateNo(app.AppName),
				ApplicationID: app.ID,
				AppName:       app.AppName,
				AppType:       app.AppType,
				GitBranch:     "manual-upload",
				Status:        "PENDING",
				TriggerUser:   username,
				ArtifactName:  firstSub.FileName,
				BatchID:       batchID,
				DeployMode:    pipelineDeployMode,
				K8sNamespace:  namespace,
			}
			if err := h.pipelineRepo.Create(p); err != nil {
				errors = append(errors, "子应用批次: 创建 Pipeline 失败")
			} else {
				pipelineDir := fmt.Sprintf("./workspaces/%s/pipeline-%d/source", app.AppName, p.ID)
				os.MkdirAll(pipelineDir, 0755)
				copyOK := true
				for _, sub := range subAppItems {
					if err := copyArtifactFromSource(sourceDir, sub.FileName, filepath.Join(pipelineDir, sub.FileName)); err != nil {
						h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
						errors = append(errors, fmt.Sprintf("%s: 复制制品失败: %v", sub.FileName, err))
						copyOK = false
						break
					}
				}

				if copyOK {
					refs := make([]deployRecordRef, 0, len(subAppItems))
					for _, sub := range subAppItems {
						refs = append(refs, deployRecordRef{FileName: sub.FileName, AppID: sub.AppID})
					}
					if err := createDeployRecordsForPipeline(batchID, namespace, req.DeployMode, username, *p, refs); err != nil {
						h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
						errors = append(errors, fmt.Sprintf("子应用批次: 创建部署记录失败: %v", err))
					} else {
						scheduler.DefaultScheduler.Enqueue(p.ID)
						pipelines = append(pipelines, gin.H{"id": p.ID, "no": p.PipelineNo, "app": app.AppName, "note": fmt.Sprintf("包含 %d 个子应用", len(subAppItems))})
					}
				}
			}
		}
	}

	// Handle other items (Java, standalone Vue) individually
	for _, item := range otherItems {
		app, err := h.appRepo.FindByID(item.AppID)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: 应用不存在", item.FileName))
			continue
		}

		p := &model.Pipeline{
			PipelineNo:    h.pipelineRepo.GenerateNo(app.AppName),
			ApplicationID: app.ID,
			AppName:       app.AppName,
			AppType:       app.AppType,
			GitBranch:     "manual-upload",
			Status:        "PENDING",
			TriggerUser:   username,
			ArtifactName:  item.FileName,
			BatchID:       batchID,
			DeployMode:    pipelineDeployMode,
			K8sNamespace:  namespace,
		}

		if err := h.pipelineRepo.Create(p); err != nil {
			errors = append(errors, fmt.Sprintf("%s: 创建 Pipeline 失败", item.FileName))
			continue
		}

		pipelineDir := fmt.Sprintf("./workspaces/%s/pipeline-%d/source", app.AppName, p.ID)
		os.MkdirAll(pipelineDir, 0755)

		if app.AppType == "java" {
			os.MkdirAll(filepath.Join(pipelineDir, "build", "libs"), 0755)
			if err := copyArtifactFromSource(sourceDir, item.FileName, filepath.Join(pipelineDir, "build", "libs", item.FileName)); err != nil {
				h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
				errors = append(errors, fmt.Sprintf("%s: 复制制品失败: %v", item.FileName, err))
				continue
			}
		} else {
			if err := copyArtifactFromSource(sourceDir, item.FileName, filepath.Join(pipelineDir, item.FileName)); err != nil {
				h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
				errors = append(errors, fmt.Sprintf("%s: 复制制品失败: %v", item.FileName, err))
				continue
			}
		}

		if err := createDeployRecordsForPipeline(batchID, namespace, req.DeployMode, username, *p, []deployRecordRef{{FileName: item.FileName, AppID: item.AppID}}); err != nil {
			h.pipelineRepo.UpdateStatus(p.ID, "FAILED")
			errors = append(errors, fmt.Sprintf("%s: 创建部署记录失败: %v", item.FileName, err))
			continue
		}

		scheduler.DefaultScheduler.Enqueue(p.ID)
		pipelines = append(pipelines, gin.H{"id": p.ID, "no": p.PipelineNo, "app": app.AppName})
	}

	response.OKWithMessage(c, fmt.Sprintf("已创建 %d 个构建任务", len(pipelines)), gin.H{
		"pipelines": pipelines,
		"errors":    errors,
	})
}

func createDeployRecordsForPipeline(versionNo, namespace, deployMode, username string, pipeline model.Pipeline, refs []deployRecordRef) error {
	if len(refs) == 0 {
		return nil
	}
	items := make([]model.ArtifactVersionItem, 0, len(refs))
	appIDs := make([]uint, 0, len(refs))
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
			appIDs = append(appIDs, ref.AppID)
			seenApps[ref.AppID] = true
		}
	}

	var apps []model.Application
	if len(appIDs) > 0 {
		if err := repository.DB.Where("id IN ?", appIDs).Find(&apps).Error; err != nil {
			return err
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
	Open(path string) (io.ReadCloser, error)
}

type remoteSeekableFS interface {
	OpenAt(path string, offset int64) (io.ReadCloser, error)
}

type sftpRemoteFS struct {
	client interface {
		ReadDir(string) ([]os.FileInfo, error)
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
			if ext != ".jar" && ext != ".zip" {
				continue
			}
			fileName, err := safeArtifactFileName(entry.Name())
			if err != nil {
				return err
			}
			if seen[fileName] {
				return fmt.Errorf("远程目录内存在同名制品文件: %s", fileName)
			}
			seen[fileName] = true

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
	folderName := safeLocalFolderName(path.Base(path.Clean(remoteDir)))
	if folderName == "" {
		folderName = "remote-download-" + newBatchID()
	}
	targetDir := filepath.Join(localParent, folderName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", nil, err
	}

	totalFiles, err := countRemoteFiles(fs, remoteDir)
	if err != nil {
		return "", nil, err
	}
	completedFiles := 0
	reportProgress := func(currentPath string) {
		if onProgress != nil {
			onProgress(downloadProgress{TotalFiles: totalFiles, CompletedFiles: completedFiles, CurrentPath: currentPath})
		}
	}
	reportProgress("")

	seenArtifacts := map[string]bool{}
	var artifacts []string

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
				if err := os.MkdirAll(localPath, 0755); err != nil {
					return err
				}
				if err := walk(remotePath, localPath); err != nil {
					return err
				}
				continue
			}

			reportProgress(remotePath)
			if err := copyRemoteFile(fs, remotePath, localPath, entry.Size()); err != nil {
				return err
			}
			completedFiles++
			reportProgress(remotePath)

			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext != ".jar" && ext != ".zip" {
				continue
			}
			fileName, err := safeArtifactFileName(entry.Name())
			if err != nil {
				return err
			}
			if seenArtifacts[fileName] {
				return fmt.Errorf("远程目录内存在同名制品文件: %s", fileName)
			}
			if err := validateArtifact(localPath); err != nil {
				os.Remove(localPath)
				return fmt.Errorf("%s 文件异常: %v", fileName, err)
			}
			seenArtifacts[fileName] = true
			artifacts = append(artifacts, fileName)
		}
		return nil
	}

	if err := walk(remoteDir, targetDir); err != nil {
		return "", nil, err
	}
	return targetDir, artifacts, nil
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
	if remoteSize >= 0 {
		if info, err := os.Stat(localPath); err == nil && info.Size() == remoteSize {
			return nil
		}
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
		return os.Rename(partPath, localPath)
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

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
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
	return os.Rename(partPath, localPath)
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
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102150405")
	}
	return time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(b[:])
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
			for _, source := range []string{"local", "download", "artifact"} {
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

func validateExecuteArtifact(sourceDir, fileName string, app model.Application) error {
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
		// Both jar and zip use ZIP format - try to open and read entries
		r, err := zip.OpenReader(filePath)
		if err != nil {
			return fmt.Errorf("无法解压 (文件损坏或格式错误)")
		}
		defer r.Close()

		if len(r.File) == 0 {
			return fmt.Errorf("压缩包内容为空")
		}

		// Try to read the first file entry to verify integrity
		firstFile := r.File[0]
		rc, err := firstFile.Open()
		if err != nil {
			return fmt.Errorf("压缩包内容无法读取")
		}
		rc.Close()

		return nil
	default:
		return fmt.Errorf("不支持的文件格式: %s", ext)
	}
}
