package handler

import (
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestLatestArtifactsFilterBeforePagination(t *testing.T) {
	repository.DB = testutil.OpenGormPostgres(t)
	if err := repository.DB.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []model.Artifact{{AppName: "one", ArtifactName: "one.jar", IsLatest: true}, {AppName: "two", ArtifactName: "two.jar", IsLatest: true}, {AppName: "old", ArtifactName: "old.jar", IsLatest: false}} {
		if err := repository.DB.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	seen := map[uint]bool{}
	for _, page := range []string{"1", "2"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/artifacts?onlyLatest=true&pageSize=1&page="+page, nil)
		NewArtifactHandler().List(c)
		var body struct {
			Code int `json:"code"`
			Data struct {
				List  []model.Artifact `json:"list"`
				Total int64            `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code != 0 || body.Data.Total != 2 || len(body.Data.List) != 1 || !body.Data.List[0].IsLatest || seen[body.Data.List[0].ID] {
			t.Fatalf("page %s: %s", page, w.Body.String())
		}
		seen[body.Data.List[0].ID] = true
	}
}
