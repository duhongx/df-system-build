package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
	"github.com/gin-gonic/gin"
)

func TestSQLUploadHTTPAcceptsArbitraryNamesAndEmptyContent(t *testing.T) {
	logger.Init("error", "stdout", "")
	initIsolatedHandlerTestDB(t)
	if err := repository.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewPostgreSQLHandler()
	router.Use(func(c *gin.Context) { c.Set("username", "uploader"); c.Next() })
	router.POST("/file", h.ParseSQL)
	router.POST("/batch", h.ParseSQLBatch)
	for _, tc := range []struct {
		path, body string
		count      int
	}{
		{"/file", `{"fileName":"空内容.sql","content":""}`, 1},
		{"/batch", `{"files":[{"fileName":"15.3_SP15__18__有视图依赖需手动执行.sql","content":"SELECT 1;"},{"fileName":"V1__1__df_bingangl.sql","content":""},{"fileName":"V1__2__df_bingangl.sql","content":"SELECT 'broken"}]}`, 3},
	} {
		request := httptest.NewRequest("POST", tc.path, bytes.NewBufferString(tc.body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				File  model.SQLChangeFile   `json:"file"`
				Files []model.SQLChangeFile `json:"files"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.Code != 0 {
			t.Fatalf("upload rejected: %s %v", recorder.Body.String(), err)
		}
		files := result.Data.Files
		if tc.path == "/file" {
			files = []model.SQLChangeFile{result.Data.File}
		}
		if len(files) != tc.count {
			t.Fatalf("lost files: %+v", files)
		}
		for _, file := range files {
			if file.CreatedUser != "uploader" || file.ImportMessage == "" || file.ExecuteStatus != "PENDING" {
				t.Fatalf("wrong uploaded metadata: %+v", file)
			}
		}
	}
}
