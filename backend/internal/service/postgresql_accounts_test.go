package service

import (
	"context"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestPostgreSQLBusinessUsernameValidation(t *testing.T) {
	for _, name := range []string{"df_zhushuju", "df_lc_menzhen", "df_lc_zhuyuan", "df_yibao", "df_yaokufang"} {
		if err := ValidatePostgreSQLBusinessUsername(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"postgres", "dfX_user", "df_a/df_b", "df_a b", "df_" + strings.Repeat("x", 61)} {
		if ValidatePostgreSQLBusinessUsername(name) == nil {
			t.Fatalf("accepted invalid username %q", name)
		}
	}
}

// Requires an isolated PostgreSQL cluster with host password authentication.
func TestPostgreSQLAccountPasswordsAndConnections(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	setSQLExecutionSettingsToTestDatabase(t)
	settings := repository.NewSettingsRepo()
	common, special := "common p@ss'$", "special p@ss&'"
	if err := settings.Set("postgresql_password", common); err != nil {
		t.Fatal(err)
	}
	for name, password := range map[string]string{"df_account_common": common, "df_account_special": special, "df_account_override_fail": common, "dfX_account_excluded": common} {
		query := "CREATE ROLE " + quoteIdent(name) + " LOGIN PASSWORD '" + escapeSQLString(password) + "'; CREATE SCHEMA " + quoteIdent(name) + " AUTHORIZATION " + quoteIdent(name)
		if err := repository.DB.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.DB.Exec("CREATE ROLE df_account_no_login NOLOGIN").Error; err != nil {
		t.Fatal(err)
	}
	repo := repository.NewPostgreSQLAccountRepo()
	if err := repo.SetPassword("df_account_special", special); err != nil {
		t.Fatal(err)
	}
	var stored model.PostgreSQLAccountPassword
	if err := repository.DB.First(&stored, "username = ?", "df_account_special").Error; err != nil {
		t.Fatal(err)
	}
	if stored.PasswordEncrypted == special || strings.Contains(stored.PasswordEncrypted, special) {
		t.Fatal("override stored in plaintext")
	}
	var storedSetting model.Settings
	if err := repository.DB.First(&storedSetting, "key = ?", "postgresql_password").Error; err != nil {
		t.Fatal(err)
	}
	if storedSetting.Value == common || !strings.HasPrefix(storedSetting.Value, "enc:v1:") {
		t.Fatal("common password stored in plaintext")
	}

	values := map[string]string{}
	for _, key := range []string{"postgresql_host", "postgresql_port", "postgresql_database", "postgresql_admin_password", "postgresql_password"} {
		value, err := settings.Read(key, true)
		if err != nil {
			t.Fatal(err)
		}
		values[key] = value
	}
	values["postgresql_password_mode"] = "shared"
	accounts, err := DiscoverPostgreSQLAccounts(context.Background(), values)
	if err != nil || len(accounts) != 3 {
		t.Fatalf("df_ prefix / LOGIN filtering failed: %+v %v", accounts, err)
	}
	// A saved independent password must never affect shared mode.
	result := TestPostgreSQLBusinessAccount(context.Background(), values, "df_account_special")
	if result.Success || result.PasswordSource != "common" {
		t.Fatalf("shared mode used independent credentials: %+v", result)
	}
	if result := TestPostgreSQLBusinessAccount(context.Background(), values, "df_account_common"); !result.Success || result.PasswordSource != "common" {
		t.Fatalf("shared login failed: %+v", result)
	}
	svc := NewPostgreSQLService()
	cfg, err := svc.loadReleaseSQLConfig("df_account_special")
	if err != nil || cfg.Password != common {
		t.Fatal("shared SQL used independent password", err)
	}

	values["postgresql_password_mode"] = "individual"
	if err := settings.Set("postgresql_password_mode", "individual"); err != nil {
		t.Fatal(err)
	}
	if result := TestPostgreSQLBusinessAccount(context.Background(), values, "df_account_common"); result.Success || !strings.Contains(result.Message, "尚未配置密码") {
		t.Fatalf("individual mode fell back to common password: %+v", result)
	}
	for _, name := range []string{"df_account_common", "df_account_override_fail"} {
		if err := repo.SetPassword(name, common); err != nil {
			t.Fatal(err)
		}
	}
	report := TestPostgreSQLConnections(context.Background(), values)
	if !report.Success || len(report.Results) != 4 {
		t.Fatalf("individual-password test failed: %+v", report)
	}
	for _, r := range report.Results {
		if r.Username != "postgres" && r.PasswordSource != "override" {
			t.Fatal("individual mode mixed password sources")
		}
	}
	// Even unreadable shared credentials are irrelevant in individual mode.
	if err := repository.DB.Model(&model.Settings{}).Where("key = ?", "postgresql_password").Update("value", "enc:v1:invalid").Error; err != nil {
		t.Fatal(err)
	}
	file, _, err := svc.ParseSQL(ParseSQLRequest{FileName: "V1__001__df_account_special.sql", Content: "CREATE TABLE execution_identity AS SELECT current_user::text AS account, current_schema()::text AS schema;"})
	if err != nil {
		t.Fatal(err)
	}
	done, _, err := svc.ExecuteSQLFile(context.Background(), file.ID, "operator")
	if err != nil || done.ExecuteStatus != "SUCCESS" {
		t.Fatalf("individual SQL failed: %+v %v", done, err)
	}
	var identity struct{ Account, Schema string }
	if err := repository.DB.Raw("SELECT * FROM df_account_special.execution_identity").Scan(&identity).Error; err != nil {
		t.Fatal(err)
	}
	if identity.Account != "df_account_special" || identity.Schema != "df_account_special" {
		t.Fatalf("wrong SQL identity: %+v", identity)
	}

	if err := repo.SetPassword("df_account_override_fail", "incorrect-password"); err != nil {
		t.Fatal(err)
	}
	failed := TestPostgreSQLBusinessAccount(context.Background(), values, "df_account_override_fail")
	if failed.Success || failed.PasswordSource != "override" {
		t.Fatalf("wrong individual password fell back: %+v", failed)
	}
	report = TestPostgreSQLConnections(context.Background(), values)
	if report.Success || len(report.Results) != 4 || !report.Results[0].Success {
		t.Fatalf("failed account hid other results: %+v", report)
	}
	if err := repo.Delete("df_account_override_fail"); err != nil {
		t.Fatal(err)
	}
	if result := TestPostgreSQLBusinessAccount(context.Background(), values, "df_account_override_fail"); result.Success || !strings.Contains(result.Message, "尚未配置密码") {
		t.Fatalf("clear password fell back: %+v", result)
	}
	values["postgresql_password_mode"] = "shared"
	if result := TestPostgreSQLBusinessAccount(context.Background(), values, "df_account_override_fail"); !result.Success || result.PasswordSource != "common" {
		t.Fatalf("switch to shared mode failed: %+v", result)
	}

	if err := repository.DB.Model(&model.PostgreSQLAccountPassword{}).Where("username = ?", "df_account_special").Update("password_encrypted", "invalid-ciphertext").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ResolvePassword("df_account_special", common, "individual"); err == nil {
		t.Fatal("individual decryption failure fell back")
	}
	if password, _, err := repo.ResolvePassword("df_account_special", common, "shared"); err != nil || password != common {
		t.Fatal("shared mode read corrupt independent password", err)
	}
	if err := settings.Set("postgresql_password_mode", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set("postgresql_password", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.loadReleaseSQLConfig("df_account_special"); err == nil {
		t.Fatal("shared mode accepted missing common password")
	}

	// Instance queries use postgres even if a legacy business username is stored.
	if err := settings.Set("postgresql_user", "nonexistent_legacy_user"); err != nil {
		t.Fatal(err)
	}
	info, err := svc.GetInstanceInfo(context.Background())
	if err != nil || info.Status != "UP" || info.CurrentUser != "postgres" {
		t.Fatalf("instance did not use administrator: %+v %v", info, err)
	}
}
