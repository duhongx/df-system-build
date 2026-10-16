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

func TestOrdinarySQLRoutesExposeFileWorkflow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewPostgreSQLHandler().RegisterRoutes(router.Group("/api"))
	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{"POST /api/postgresql/sql-files/execute-content", "POST /api/postgresql/sql-statements/:id/execute", "POST /api/postgresql/sql-statements/:id/schema-autocomplete", "POST /api/postgresql/sql-batches/:id/execute", "GET /api/postgresql/sql-force-whitelist"} {
		if routes[route] {
			t.Errorf("retired SQL operation is still exposed: %s", route)
		}
	}
	for _, route := range []string{"GET /api/postgresql/sql-records", "GET /api/postgresql/sql-batches/:id/files/:fileId", "PUT /api/postgresql/sql-files/:id/execution-config", "POST /api/postgresql/sql-batches/:id/cancel", "PUT /api/postgresql/sql-statements/:id/content", "POST /api/postgresql/sql-files/save", "POST /api/postgresql/sql-files/import-server", "POST /api/postgresql/sql-files/:id/execute", "POST /api/postgresql/sql-files/:id/cancel", "POST /api/postgresql/sql-files/:id/skip", "POST /api/postgresql/sql-statements/:id/skip", "GET /api/postgresql/sql-files/done"} {
		if !routes[route] {
			t.Errorf("missing file operation: %s", route)
		}
	}
}
