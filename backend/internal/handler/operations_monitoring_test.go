package handler

import (
	"df-build-server/internal/service"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestMonitoringAndServiceControlRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewOperationsHandler(service.NewOperationsService(nil, t.TempDir())).RegisterRoutes(r.Group("/api"))
	for _, item := range []struct{ method, path string }{{"GET", "/components"}, {"POST", "/components"}, {"POST", "/components/1/collect"}, {"GET", "/collections/component/1"}, {"POST", "/configured-inspections"}, {"POST", "/databases/1/inspection"}, {"GET", "/hosts/1/services"}, {"POST", "/hosts/1/services"}, {"POST", "/hosts/1/services/1/operations"}, {"GET", "/hosts/1/services/1/logs"}, {"GET", "/hosts/1/services/1/status"}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(item.method, "/api/operations"+item.path, nil))
		if w.Code != 401 {
			t.Fatalf("unauthorized %s %s returned %d", item.method, item.path, w.Code)
		}
	}
}
