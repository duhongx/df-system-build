package handler

import (
	"bytes"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSQLHistoryHTTPQueryAndExplicitSkip(t *testing.T) {
	// Same isolated PostgreSQL setup and authenticated operator as upload tests.
	setupPostgreSQLHistoryTest(t)
	svc := service.NewPostgreSQLService()
	file, rows, err := svc.ParseSQL(service.ParseSQLRequest{FileName: "V1__1__df_audit.sql", Content: "SELECT 1;", CreatedUser: "uploader"})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewPostgreSQLHandler()
	router.Use(func(c *gin.Context) { c.Set("username", "skip-user"); c.Next() })
	router.POST("/statements/:id/skip", h.SkipSQLStatement)
	router.GET("/records", h.ListSQLStatementRecords)
	call := func(method, path string, body []byte) []byte {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w.Body.Bytes()
	}
	var skip struct{ Code int }
	body := call("POST", "/statements/"+strconv.FormatUint(uint64(rows[0].ID), 10)+"/skip", nil)
	if err := json.Unmarshal(body, &skip); err != nil || skip.Code != 0 {
		t.Fatalf("skip rejected %s", body)
	}
	body = call("GET", "/records?fileId="+strconv.FormatUint(uint64(file.ID), 10)+"&action=MANUAL_SKIP", nil)
	var reply struct {
		Code int
		Data struct {
			List []struct {
				Action    string
				Submitted *bool
				Operator  string
			}
			Total int64
		}
	}
	if err := json.Unmarshal(body, &reply); err != nil || reply.Code != 0 || reply.Data.Total != 1 || len(reply.Data.List) != 1 || reply.Data.List[0].Action != "MANUAL_SKIP" || reply.Data.List[0].Submitted == nil || *reply.Data.List[0].Submitted || reply.Data.List[0].Operator != "skip-user" {
		t.Fatalf("cannot query manual skip: %s", body)
	}
}

func setupPostgreSQLHistoryTest(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
}
