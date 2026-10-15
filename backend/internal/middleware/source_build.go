package middleware

import (
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"net/http"
)

func SourceBuildRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := repository.RequireSourceBuild(); err != nil {
			response.FailWithStatus(c, http.StatusForbidden, 10805, err.Error())
			c.Abort()
			return
		}
		c.Next()
	}
}
