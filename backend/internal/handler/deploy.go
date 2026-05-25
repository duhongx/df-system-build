package handler

import (
	"df-build-server/internal/deployer"
	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type DeployHandler struct {
	planRepo *repository.DeployPlanRepo
	logRepo  *repository.DeployLogRepo
}

func NewDeployHandler() *DeployHandler {
	return &DeployHandler{
		planRepo: repository.NewDeployPlanRepo(),
		logRepo:  repository.NewDeployLogRepo(),
	}
}

func (h *DeployHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/deploy")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/components", h.GetComponents)
		g.POST("/check-packages", h.CheckPackages)
		g.POST("/check-server/:id", h.CheckServer)
		g.GET("/plan", h.GetPlan)
		g.POST("/plan", h.SavePlan)
		g.GET("/logs", h.GetLogs)
	}
}

// GetComponents returns all available components with their param definitions
func (h *DeployHandler) GetComponents(c *gin.Context) {
	components := deployer.GetComponents()
	var result []gin.H
	for _, comp := range components {
		result = append(result, gin.H{
			"code":     comp.Code(),
			"name":     comp.Name(),
			"order":    comp.Order(),
			"category": comp.Category(),
			"params":   comp.Params(),
		})
	}
	response.OK(c, result)
}

// CheckPackages validates the offline package directory
func (h *DeployHandler) CheckPackages(c *gin.Context) {
	var req struct {
		PackageDir string `json:"packageDir" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 15001, "请指定离线包目录")
		return
	}
	result := deployer.CheckPackages(req.PackageDir)
	response.OK(c, result)
}

// CheckServer validates a server's prerequisites
func (h *DeployHandler) CheckServer(c *gin.Context) {
	// TODO: get server by ID and run checks
	response.OK(c, nil)
}

// GetPlan returns the current deploy plan
func (h *DeployHandler) GetPlan(c *gin.Context) {
	plan, err := h.planRepo.GetLatest()
	if err != nil {
		response.OK(c, nil)
		return
	}
	response.OK(c, plan)
}

// SavePlan saves a deploy plan
func (h *DeployHandler) SavePlan(c *gin.Context) {
	var plan struct {
		Name        string `json:"name"`
		PackageDir  string `json:"packageDir"`
		Assignments string `json:"assignments"`
	}
	if err := c.ShouldBindJSON(&plan); err != nil {
		response.Fail(c, 15002, "参数错误")
		return
	}
	// TODO: save plan
	response.OKWithMessage(c, "方案已保存", nil)
}

// GetLogs returns deployment logs
func (h *DeployHandler) GetLogs(c *gin.Context) {
	response.OK(c, nil)
}
