package repository

import (
	"testing"
	"time"

	"df-build-server/internal/model"
)

func seedPreRepairFrontendApplications(t *testing.T) {
	t.Helper()
	setupRepositoryTestDB(t)
	if err := DB.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO schema_migrations (version, description) VALUES (6, 'old startup ran migration before seeding');`).Error; err != nil {
		t.Fatal(err)
	}
	for _, app := range seededApplications() {
		app.AdoptExistingDirectory = false // reproduce the old SeedApplications implementation
		if err := DB.Create(&app).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestFrontendAdoptionSeedingAndUpgrade(t *testing.T) {
	seedPreRepairFrontendApplications(t)
	find := func(name string) model.Application {
		t.Helper()
		var app model.Application
		if err := DB.Where("app_name = ?", name).First(&app).Error; err != nil {
			t.Fatal(err)
		}
		return app
	}
	untouched := find("web-binglizk")
	optOut := find("web-shouma")
	if err := DB.Model(&optOut).Updates(map[string]any{"adopt_existing_directory": false, "updated_at": optOut.CreatedAt.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	changed := find("web-yiji")
	if err := DB.Model(&changed).UpdateColumn("git_repo", "ssh://custom/yiji.git").Error; err != nil {
		t.Fatal(err)
	}
	custom := model.Application{AppName: "web-custom", AppType: "vue", VueRole: "sub", AppCode: "42", ArtifactName: "42.zip", Enabled: true}
	if err := DB.Create(&custom).Error; err != nil {
		t.Fatal(err)
	}
	// A failed build changes updated_at too. Do not infer user preferences from
	// that timestamp: recognition of its live legacy identity is tested in stages.
	if err := NewApplicationRepo().UpdateBuildStatus(find("web-bui").ID, "FAILED"); err != nil {
		t.Fatal(err)
	}
	RunMigrations()
	repaired := find("web-binglizk")
	if !repaired.AdoptExistingDirectory || !repaired.UpdatedAt.Equal(untouched.UpdatedAt) {
		t.Fatalf("untouched legacy import not repaired without editing metadata: %+v", repaired)
	}
	for _, name := range []string{"web-shouma", "web-yiji", "web-custom", "web-bui"} {
		if find(name).AdoptExistingDirectory {
			t.Fatalf("modified/custom preference overwritten: %s", name)
		}
	}
	if err := DB.Model(&repaired).Update("adopt_existing_directory", false).Error; err != nil {
		t.Fatal(err)
	}
	RunMigrations()
	SeedApplications()
	if find("web-binglizk").AdoptExistingDirectory || find("web-shouma").AdoptExistingDirectory {
		t.Fatal("repeat startup overwrote an explicit opt-out")
	}
	missing := find("web-menzhenysz")
	if err := DB.Delete(&missing).Error; err != nil {
		t.Fatal(err)
	}
	SeedApplications()
	if !find("web-menzhenysz").AdoptExistingDirectory {
		t.Fatal("legacy app seeded after migrations lost its adoption marker")
	}
}

func TestFrontendAdoptionMigrationFailureIsRetried(t *testing.T) {
	seedPreRepairFrontendApplications(t)
	if err := DB.Exec(`CREATE FUNCTION reject_adoption() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.app_name = 'web-binglizk' THEN RAISE EXCEPTION 'injected adoption failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_adoption BEFORE UPDATE ON applications FOR EACH ROW EXECUTE FUNCTION reject_adoption();`).Error; err != nil {
		t.Fatal(err)
	}
	RunMigrations()
	var count int64
	if err := DB.Raw("SELECT count(*) FROM schema_migrations WHERE version = 7").Scan(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed migration marked complete: %d %v", count, err)
	}
	if err := DB.Model(&model.Application{}).Where("adopt_existing_directory = true").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed migration partially committed: %d %v", count, err)
	}
	if err := DB.Exec("DROP TRIGGER reject_adoption ON applications; DROP FUNCTION reject_adoption();").Error; err != nil {
		t.Fatal(err)
	}
	RunMigrations()
	if err := DB.Raw("SELECT count(*) FROM schema_migrations WHERE version = 7").Scan(&count).Error; err != nil || count != 1 {
		t.Fatalf("repair not retried: %d %v", count, err)
	}
	if err := DB.Model(&model.Application{}).Where("app_name = ? AND adopt_existing_directory = true", "web-binglizk").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("legacy directory not adopted on retry: %d %v", count, err)
	}
}
