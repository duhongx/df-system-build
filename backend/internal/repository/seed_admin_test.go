package repository

import (
	"testing"

	"df-build-server/internal/model"
	"golang.org/x/crypto/bcrypt"
)

func TestSeedCoreDataAdminPasswordAndExistingAccountPreserved(t *testing.T) {
	setupRepositoryTestDB(t)
	SeedCoreData()
	var admin model.User
	if err := DB.Where("username = ?", "admin").First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte("cloudhis@2123")); err != nil {
		t.Fatal("fresh database admin does not accept the configured default password")
	}
	if bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte("123456")) == nil {
		t.Fatal("fresh database still accepts the former default password")
	}
	customHash, err := bcrypt.GenerateFromPassword([]byte("existing-admin-custom-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&admin).Updates(map[string]any{"password_hash": string(customHash), "email": "existing@example.com"}).Error; err != nil {
		t.Fatal(err)
	}
	SeedCoreData()
	var reloaded model.User
	if err := DB.First(&reloaded, admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PasswordHash != string(customHash) || reloaded.Email != "existing@example.com" {
		t.Fatal("repeat startup overwrote the existing administrator account")
	}
	var count int64
	if err := DB.Model(&model.User{}).Where("username = ?", "admin").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("admin seeding is not idempotent: count=%d error=%v", count, err)
	}
}
