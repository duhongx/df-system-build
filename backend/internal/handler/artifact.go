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
	query.Count(&total)

	if pageSize <= 0 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&artifacts)

	response.OKWithPage(c, artifacts, total, page, pageSize)
}
