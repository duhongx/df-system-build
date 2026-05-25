package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type BuildConfigHandler struct {
	repo *repository.BuildConfigRepo
}

func NewBuildConfigHandler() *BuildConfigHandler {
	return &BuildConfigHandler{repo: repository.NewBuildConfigRepo()}
}

func (h *BuildConfigHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/build-configs")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
	}
}

func (h *BuildConfigHandler) List(c *gin.Context) {
	list, err := h.repo.List()
	if err != nil {
		response.Fail(c, 10801, "获取编译配置列表失败")
		return
	}
	response.OK(c, list)
}

type buildConfigReq struct {
	Name           string `json:"name" binding:"required"`
	Category       string `json:"category" binding:"required"`
	BuildMode      string `json:"buildMode" binding:"required"`
	DockerImage    string `json:"dockerImage"`
	CPULimit       string `json:"cpuLimit"`
	MemoryLimit    string `json:"memoryLimit"`
	CacheMounts    string `json:"cacheMounts"`
	InstallCommand string `json:"installCommand"`
	BuildCommand   string `json:"buildCommand" binding:"required"`
	ArtifactDir    string `json:"artifactDir"`
	EnvVars        string `json:"envVars"`
	Description    string `json:"description"`
	Status         string `json:"status"`
}

func (h *BuildConfigHandler) Create(c *gin.Context) {
	var req buildConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10802, "参数错误")
		return
	}
	status := req.Status
	if status == "" {
		status = "online"
	}
	bc := &model.BuildConfig{
		Name:           req.Name,
		Category:       req.Category,
		BuildMode:      req.BuildMode,
		DockerImage:    req.DockerImage,
		CPULimit:       req.CPULimit,
		MemoryLimit:    req.MemoryLimit,
		CacheMounts:    req.CacheMounts,
		InstallCommand: req.InstallCommand,
		BuildCommand:   req.BuildCommand,
		ArtifactDir:    req.ArtifactDir,
		EnvVars:        req.EnvVars,
		Description:    req.Description,
		Status:         status,
	}
	if err := h.repo.Create(bc); err != nil {
		response.Fail(c, 10802, "创建失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "创建成功", bc)
}

func (h *BuildConfigHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req buildConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10803, "参数错误")
		return
	}
	bc, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10803, "编译配置不存在")
		return
	}
	bc.Name = req.Name
	bc.Category = req.Category
	bc.BuildMode = req.BuildMode
	bc.DockerImage = req.DockerImage
	bc.CPULimit = req.CPULimit
	bc.MemoryLimit = req.MemoryLimit
	bc.CacheMounts = req.CacheMounts
	bc.InstallCommand = req.InstallCommand
	bc.BuildCommand = req.BuildCommand
	bc.ArtifactDir = req.ArtifactDir
	bc.EnvVars = req.EnvVars
	bc.Description = req.Description
	if req.Status != "" {
		bc.Status = req.Status
	}
	h.repo.Update(bc)
	response.OKWithMessage(c, "更新成功", bc)
}

func (h *BuildConfigHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.repo.Delete(uint(id)); err != nil {
		response.Fail(c, 10804, "删除失败")
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}
