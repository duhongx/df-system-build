package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"df-build-server/pkg/crypto"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

func TestWorkbenchAPIAuthenticationTargetAndCSV(t *testing.T) {
	db := testutil.OpenGormPostgres(t)
	if err := db.AutoMigrate(&model.OperationsDatabase{}, &model.DatabaseQueryRecord{}); err != nil {
		t.Fatal(err)
	}
	dsn, _ := testutil.PostgresDSNFromEnv()
	pg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := crypto.Encrypt(pg.Password)
	if err != nil {
		t.Fatal(err)
	}
	row := model.OperationsDatabase{Name: "workbench-handler", Host: pg.Host, Port: int(pg.Port), Database: pg.Database, Username: pg.User, SSLMode: "disable", PasswordEncrypted: secret, Revision: 3}
	if err = db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	oldDB, oldConfig := repository.DB, config.AppConfig
	repository.DB = db
	config.AppConfig = &config.Config{JWT: config.JWTConfig{Secret: "workbench-test", ExpireHours: 1}}
	t.Cleanup(func() { repository.DB = oldDB; config.AppConfig = oldConfig })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewOperationsHandler(service.NewOperationsService(db, t.TempDir())).RegisterRoutes(router.Group("/api"))
	token, err := middleware.GenerateToken(1, "workbench-operator")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	base := fmt.Sprintf("/api/operations/databases/%d", row.ID)
	for _, path := range []string{"/catalog", "/objects/1", "/statements", "/query-records", "/slow-analysis", "/access", "/access/privileges?role=postgres"} {
		if w := call("GET", base+path, "", false); w.Code != http.StatusUnauthorized {
			t.Fatalf("unprotected %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/access/preview", "/access/apply"} {
		if w := call("POST", base+path, `{}`, false); w.Code != http.StatusUnauthorized {
			t.Fatalf("unprotected permission mutation: %s", path)
		}
	}
	if w := call("POST", base+"/query", `{"sql":"select 1","revision":3}`, false); w.Code != http.StatusUnauthorized {
		t.Fatal("query accessible without authentication")
	}
	w := call("POST", base+"/query", `{"sql":"select current_database(),$1::text","parameters":["绑定值"],"revision":3}`, true)
	var body struct {
		Code int
		Data service.WorkbenchResult
	}
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != 0 || body.Data.Status != "SUCCESS" || body.Data.DatabaseID != row.ID || body.Data.Rows[0][1] != "绑定值" {
		t.Fatalf("query response %s %v", w.Body.String(), err)
	}
	var history model.DatabaseQueryRecord
	if err = db.First(&history, body.Data.RecordID).Error; err != nil || history.Operator != "workbench-operator" || history.Revision != 3 {
		t.Fatalf("execution identity %+v %v", history, err)
	}
	w = call("POST", base+"/query", `{"sql":"select i from generate_series(1,1200) i","mode":"export","revision":3}`, true)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") || w.Header().Get("X-Query-Record-ID") == "" || strings.Count(w.Body.String(), "\n") != 1201 {
		t.Fatalf("CSV response %s %d", w.Header(), w.Body.Len())
	}
	w = call("POST", base+"/query", `{"sql":"delete from operations_databases","mode":"export","revision":3}`, true)
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") || !strings.Contains(w.Body.String(), "FAILED") {
		t.Fatalf("failed export served as CSV: %s", w.Body.String())
	}
	w = call("POST", base+"/query", `{"sql":"select 1","revision":2}`, true)
	if !strings.Contains(w.Body.String(), "连接配置已变更") {
		t.Fatalf("stale target accepted: %s", w.Body.String())
	}
	w = call("GET", base+"/query-records", "", true)
	if !strings.Contains(w.Body.String(), "workbench-operator") {
		t.Fatal("execution records unavailable")
	}
}
