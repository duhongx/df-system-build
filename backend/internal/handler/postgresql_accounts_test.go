package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"df-build-server/internal/repository"
	"github.com/gin-gonic/gin"
)

func TestPostgreSQLAccountPasswordAPI(t *testing.T) {
	setupSettingsHandlerTestDB(t)
	gin.SetMode(gin.TestMode)
	h := NewSettingsHandler()
	request := func(method, username string, body string, action gin.HandlerFunc) (int, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "username", Value: username}}
		c.Request = httptest.NewRequest(method, "/settings/postgresql-accounts", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		action(c)
		var out struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Code, w.Body.String()
	}
	if code, _ := request(http.MethodPut, "", `{"postgresql_password_mode":"mixed"}`, h.Update); code == 0 {
		t.Fatal("invalid password mode accepted")
	}
	if code, _ := request(http.MethodPut, "", `{"postgresql_password_mode":"individual"}`, h.Update); code != 0 {
		t.Fatal("individual mode not saved")
	}
	if mode, err := repository.NewSettingsRepo().Read("postgresql_password_mode", true); err != nil || mode != "individual" {
		t.Fatal("password mode not persisted", err)
	}
	for _, username := range []string{"postgres", "dfX_invalid", "df_a/df_b"} {
		if code, _ := request(http.MethodPut, username, `{"password":"test-secret"}`, h.SetPostgreSQLAccountPassword); code == 0 {
			t.Fatalf("invalid override accepted: %s", username)
		}
	}
	if code, _ := request(http.MethodPut, "df_yibao", `{"password":""}`, h.SetPostgreSQLAccountPassword); code == 0 {
		t.Fatal("empty override accepted")
	}
	if code, body := request(http.MethodPut, "df_yibao", `{"password":"test-secret"}`, h.SetPostgreSQLAccountPassword); code != 0 || strings.Contains(body, "test-secret") {
		t.Fatal("save failed or exposed password")
	}
	if code, body := request(http.MethodGet, "", "", h.ListPostgreSQLPasswordOverrides); code != 0 || !strings.Contains(body, "df_yibao") || strings.Contains(body, "test-secret") || strings.Contains(body, "passwordEncrypted") {
		t.Fatal("override metadata exposed password or omitted account")
	}
	password, source, err := repository.NewPostgreSQLAccountRepo().ResolvePassword("df_yibao", "common", "individual")
	if err != nil || password != "test-secret" || source != "override" {
		t.Fatal("API did not persist override", err)
	}
	if err := repository.NewSettingsRepo().Set("postgresql_user", "retired"); err != nil {
		t.Fatal(err)
	}
	if code, body := request(http.MethodGet, "", "", h.GetAll); code != 0 || strings.Contains(body, "postgresql_user") {
		t.Fatal("single username still exposed")
	}
	if code, _ := request(http.MethodDelete, "df_yibao", "", h.DeletePostgreSQLAccountPassword); code != 0 {
		t.Fatal("clear override failed")
	}
	if _, _, err := repository.NewPostgreSQLAccountRepo().ResolvePassword("df_yibao", "common", "individual"); err == nil {
		t.Fatal("cleared individual password fell back to shared password")
	}
	code, body := request(http.MethodPost, "", `{"type":"postgresql","config":{"postgresql_host":"127.0.0.1","postgresql_port":"1","postgresql_admin_password":"test-secret"}}`, h.TestConnection)
	if code != 0 || !strings.Contains(body, `"success":false`) || !strings.Contains(body, `"username":"postgres"`) || strings.Contains(body, "test-secret") {
		t.Fatal("failed administrator probe lost details or exposed password")
	}
}
