package handler

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPostgreSQLRoutesRegisterWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	NewPostgreSQLHandler().RegisterRoutes(router.Group("/api"))
}
