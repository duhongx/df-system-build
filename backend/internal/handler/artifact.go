package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type ArtifactHandler struct{}

func NewArtifactHandler() *ArtifactHandler { return &ArtifactHandler{} }

func (h *ArtifactHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/artifacts")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
	}
}

func (h *ArtifactHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	search := c.Query("search")

	var artifacts []model.Artifact
	var total int64

	query := repository.DB.Model(&model.Artifact{})
	if search != "" {
		query = query.Where("app_name LIKE ? OR artifact_name LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	if c.Query("onlyLatest") == "true" {
		query = query.Where("is_latest = ?", true)
	}
	if err := query.Count(&total).Error; err != nil {
		response.Fail(c, 10701, "获取制品列表失败")
		return
	}

	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize
	if err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&artifacts).Error; err != nil {
		response.Fail(c, 10701, "获取制品列表失败")
		return
	}

	response.OKWithPage(c, artifacts, total, page, pageSize)
}
