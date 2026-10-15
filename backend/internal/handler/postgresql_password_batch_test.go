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

func TestPostgreSQLPasswordBatchAtomic(t *testing.T) {
	setupSettingsHandlerTestDB(t)
	gin.SetMode(gin.TestMode)
	h := NewSettingsHandler()
	repo := repository.NewPostgreSQLAccountRepo()
	settings := repository.NewSettingsRepo()
	for username, password := range map[string]string{"df_batch_a": "before-a", "df_batch_b": "before-b", "df_batch_unselected": "keep-password"} {
		if err := repo.SetPassword(username, password); err != nil {
			t.Fatal(err)
		}
	}
	if err := settings.Set("postgresql_password_mode", "shared"); err != nil {
		t.Fatal(err)
	}
	request := func(body string) (int, string) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/settings/postgresql-accounts/passwords/batch", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.SavePostgreSQLAccountPasswords(c)
		var out struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Code, w.Body.String()
	}
	assertPassword := func(username, expected string) {
		password, _, err := repo.ResolvePassword(username, "", "individual")
		if err != nil || password != expected {
			t.Fatalf("password unexpectedly changed for %s", username)
		}
	}
	for _, body := range []string{
		`{"items":[]}`,
		`{"items":[{"username":"df_batch_a","password":"changed"},{"username":"postgres","password":"changed"}]}`,
		`{"items":[{"username":"df_batch_a","password":"changed"},{"username":"df_batch_a","password":"other"}]}`,
		`{"items":[{"username":"df_batch_a","password":"changed"},{"username":"df_batch_b","password":""}]}`,
		`{"items":[{"username":"df_batch_a","password":"changed"}],"config":{"docker_registry_url":"not-allowed"}}`,
		`{"items":[{"username":"df_batch_a","password":"changed"}],"config":{"postgresql_password_mode":"mixed"}}`,
	} {
		if code, _ := request(body); code == 0 {
			t.Fatal("invalid batch was accepted")
		}
		assertPassword("df_batch_a", "before-a")
	}
	code, body := request(`{"items":[{"username":"df_batch_a","password":"same-password"},{"username":"df_batch_b","password":"same-password"}],"config":{"postgresql_password_mode":"individual"}}`)
	if code != 0 || !strings.Contains(body, `"updatedCount":2`) || strings.Contains(body, "same-password") {
		t.Fatal("batch save failed or exposed password")
	}
	assertPassword("df_batch_a", "same-password")
	assertPassword("df_batch_b", "same-password")
	assertPassword("df_batch_unselected", "keep-password")
	if mode, _ := settings.Read("postgresql_password_mode", true); mode != "individual" {
		t.Fatal("mode and password not saved together")
	}

	// An individual edit after a batch only changes that user's password.
	if err := repo.SetPassword("df_batch_b", "individual-password"); err != nil {
		t.Fatal(err)
	}
	assertPassword("df_batch_a", "same-password")
	assertPassword("df_batch_b", "individual-password")

	// Fail the settings write after password writes; neither may commit.
	if err := repository.DB.Exec(`CREATE FUNCTION fail_password_mode_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key = 'postgresql_password_mode' THEN RAISE EXCEPTION 'simulated settings failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER fail_password_mode_write BEFORE UPDATE ON settings FOR EACH ROW EXECUTE FUNCTION fail_password_mode_write();`).Error; err != nil {
		t.Fatal(err)
	}
	if code, body := request(`{"items":[{"username":"df_batch_a","password":"must-rollback"},{"username":"df_batch_b","password":"must-rollback"}],"config":{"postgresql_password_mode":"shared"}}`); code == 0 || strings.Contains(body, "must-rollback") {
		t.Fatal("failed transaction reported success or exposed password")
	}
	assertPassword("df_batch_a", "same-password")
	assertPassword("df_batch_b", "individual-password")
	if mode, _ := settings.Read("postgresql_password_mode", true); mode != "individual" {
		t.Fatal("failed settings write changed mode")
	}
	if err := repository.DB.Exec("DROP TRIGGER fail_password_mode_write ON settings; DROP FUNCTION fail_password_mode_write()").Error; err != nil {
		t.Fatal(err)
	}

	// Shared mode can be saved without touching individual credentials.
	if code, _ := request(`{"items":[],"config":{"postgresql_password_mode":"shared","postgresql_password":"shared-password"}}`); code != 0 {
		t.Fatal("shared save failed")
	}
	if password, source, err := repo.ResolvePassword("df_batch_b", "shared-password", "shared"); err != nil || password != "shared-password" || source != "common" {
		t.Fatal("shared mode not applied")
	}
	assertPassword("df_batch_b", "individual-password")
}
