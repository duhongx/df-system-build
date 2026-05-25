package handler

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type BatchDeployHandler struct {
	appRepo      *repository.ApplicationRepo
	pipelineRepo *repository.PipelineRepo
}

func NewBatchDeployHandler() *BatchDeployHandler {
	return &BatchDeployHandler{
		appRepo:      repository.NewApplicationRepo(),
		pipelineRepo: repository.NewPipelineRepo(),
	}
}

func (h *BatchDeployHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/batch-deploy")
	g.Use(middleware.AuthRequired())
	{
		g.POST("/upload", h.Upload)
		g.POST("/match", h.Match)
		g.POST("/execute", h.Execute)
		g.GET("/local-dir", h.ListLocalDir)
	}
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

	// Create temp upload dir
	uploadDir := "./workspaces/batch-upload"
	os.MkdirAll(uploadDir, 0755)

	type uploadFailure struct {
		FileName string `json:"fileName"`
		Error    string `json:"error"`
	}

	var successFiles []string
	var failedFiles []uploadFailure

	for _, file := range files {
		dst := filepath.Join(uploadDir, file.Filename)
		if err := c.SaveUploadedFile(file, dst); err != nil {
			failedFiles = append(failedFiles, uploadFailure{FileName: file.Filename, Error: "保存文件失败"})
			continue
		}

		// Validate artifact integrity
		if err := validateArtifact(dst); err != nil {
			os.Remove(dst)
			failedFiles = append(failedFiles, uploadFailure{FileName: file.Filename, Error: err.Error()})
			continue
		}

		successFiles = append(successFiles, file.Filename)
	}

	response.OK(c, gin.H{
		"uploadDir": uploadDir,
		"success":   successFiles,
		"failed":    failedFiles,
		"count":     len(successFiles),
	})
}

// ListLocalDir lists files in a local directory on the server
func (h *BatchDeployHandler) ListLocalDir(c *gin.Context) {
	dir := c.Query("path")
	if dir == "" {
		response.Fail(c, 13002, "请指定目录路径")
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		response.Fail(c, 13002, "读取目录失败: "+err.Error())
		return
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Only include jar and zip files
		if strings.HasSuffix(name, ".jar") || strings.HasSuffix(name, ".zip") {
			files = append(files, name)
		}
	}

	response.OK(c, gin.H{"path": dir, "files": files, "count": len(files)})
}

type matchResult struct {
	FileName    string `json:"fileName"`
	AppName     string `json:"appName"`
	AppType     string `json:"appType"`
	AppID       uint   `json:"appId"`
	Matched     bool   `json:"matched"`
	Valid       bool   `json:"valid"`
	MatchReason string `json:"matchReason"`
}

// Match matches uploaded file names to applications and validates file integrity
func (h *BatchDeployHandler) Match(c *gin.Context) {
	var req struct {
		Files     []string `json:"files" binding:"required"`
		SourceDir string   `json:"sourceDir"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 13003, "参数错误")
		return
	}

	sourceDir := req.SourceDir
	if sourceDir == "" {
		sourceDir = "./workspaces/batch-upload"
	}

	// Load all applications
	apps, _, _ := h.appRepo.List(repository.AppListParams{PageSize: 1000})

	var results []matchResult
	for _, fileName := range req.Files {
		result := h.matchFile(fileName, apps)
		// Validate file integrity
		filePath := filepath.Join(sourceDir, fileName)
		if err := validateArtifact(filePath); err != nil {
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
	for _, r := range results {
		if r.Matched && r.Valid {
			matched++
		}
		if !r.Valid {
			invalid++
		}
	}

	response.OK(c, gin.H{"results": results, "total": len(results), "matched": matched, "invalid": invalid})
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
		SourceDir string `json:"sourceDir"` // Directory containing artifacts
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

	sourceDir := req.SourceDir
	if sourceDir == "" {
		sourceDir = "./workspaces/batch-upload"
	}

	username := middleware.GetCurrentUsername(c)
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
		app, err := h.appRepo.FindByID(item.AppID)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: 应用不存在", item.FileName))
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

	// Handle web-main + sub-apps as a single pipeline (first deploy scenario)
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
			DeployMode:    "artifact_deploy",
		}

		if err := h.pipelineRepo.Create(p); err != nil {
			errors = append(errors, fmt.Sprintf("%s: 创建 Pipeline 失败", webMainItem.FileName))
		} else {
			// Prepare workspace: copy web-main.zip + all sub-app zips
			pipelineDir := fmt.Sprintf("./workspaces/%s/pipeline-%d/source", app.AppName, p.ID)
			os.MkdirAll(pipelineDir, 0755)

			// Copy web-main.zip
			copyFileSimple(filepath.Join(sourceDir, webMainItem.FileName), filepath.Join(pipelineDir, webMainItem.FileName))

			// Copy all sub-app zips into the same source dir (for first deploy bundling)
			for _, sub := range subAppItems {
				copyFileSimple(filepath.Join(sourceDir, sub.FileName), filepath.Join(pipelineDir, sub.FileName))
			}

			scheduler.DefaultScheduler.Enqueue(p.ID)
			pipelines = append(pipelines, gin.H{"id": p.ID, "no": p.PipelineNo, "app": app.AppName, "note": "包含子应用"})
		}
	} else {
		// No web-main in batch: sub-apps create individual pipelines
		for _, sub := range subAppItems {
			app, _ := h.appRepo.FindByID(sub.AppID)
			p := &model.Pipeline{
				PipelineNo:    h.pipelineRepo.GenerateNo(app.AppName),
				ApplicationID: app.ID,
				AppName:       app.AppName,
				AppType:       app.AppType,
				GitBranch:     "manual-upload",
				Status:        "PENDING",
				TriggerUser:   username,
				ArtifactName:  sub.FileName,
				DeployMode:    "artifact_deploy",
			}
			if err := h.pipelineRepo.Create(p); err != nil {
				errors = append(errors, fmt.Sprintf("%s: 创建 Pipeline 失败", sub.FileName))
				continue
			}
			pipelineDir := fmt.Sprintf("./workspaces/%s/pipeline-%d/source", app.AppName, p.ID)
			os.MkdirAll(pipelineDir, 0755)
			copyFileSimple(filepath.Join(sourceDir, sub.FileName), filepath.Join(pipelineDir, sub.FileName))
			scheduler.DefaultScheduler.Enqueue(p.ID)
			pipelines = append(pipelines, gin.H{"id": p.ID, "no": p.PipelineNo, "app": app.AppName})
		}
	}

	// Handle other items (Java, standalone Vue) individually
	for _, item := range otherItems {
		app, err := h.appRepo.FindByID(item.AppID)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: 应用不存在", item.FileName))
			continue
		}

		artifactSrc := filepath.Join(sourceDir, item.FileName)
		if _, err := os.Stat(artifactSrc); os.IsNotExist(err) {
			errors = append(errors, fmt.Sprintf("%s: 文件不存在", item.FileName))
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
			DeployMode:    "artifact_deploy",
		}

		if err := h.pipelineRepo.Create(p); err != nil {
			errors = append(errors, fmt.Sprintf("%s: 创建 Pipeline 失败", item.FileName))
			continue
		}

		pipelineDir := fmt.Sprintf("./workspaces/%s/pipeline-%d/source", app.AppName, p.ID)
		os.MkdirAll(pipelineDir, 0755)

		if app.AppType == "java" {
			os.MkdirAll(filepath.Join(pipelineDir, "build", "libs"), 0755)
			copyFileSimple(artifactSrc, filepath.Join(pipelineDir, "build", "libs", item.FileName))
		} else {
			copyFileSimple(artifactSrc, filepath.Join(pipelineDir, item.FileName))
		}

		scheduler.DefaultScheduler.Enqueue(p.ID)
		pipelines = append(pipelines, gin.H{"id": p.ID, "no": p.PipelineNo, "app": app.AppName})
	}

	response.OKWithMessage(c, fmt.Sprintf("已创建 %d 个构建任务", len(pipelines)), gin.H{
		"pipelines": pipelines,
		"errors":    errors,
	})
}

func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
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
