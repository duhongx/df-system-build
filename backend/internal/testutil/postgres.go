package testutil

import (
	"os"
	"testing"

	"df-build-server/pkg/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func PostgresConfig(t *testing.T) *config.DatabaseConfig {
	t.Helper()
	if dsn, ok := PostgresDSNFromEnv(); ok {
		return &config.DatabaseConfig{DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2}
	}
	t.Skip("set TEST_DATABASE_DSN or TEST_DB_HOST/TEST_DB_NAME/TEST_DB_USER to run PostgreSQL integration tests")
	return nil
}

func PostgresDSNFromEnv() (string, bool) {
	if dsn := os.Getenv("TEST_DATABASE_DSN"); dsn != "" {
		return dsn, true
	}
	host := os.Getenv("TEST_DB_HOST")
	name := os.Getenv("TEST_DB_NAME")
	user := os.Getenv("TEST_DB_USER")
	if host == "" || name == "" || user == "" {
		return "", false
	}
	port := os.Getenv("TEST_DB_PORT")
	if port == "" {
		port = "5432"
	}
	return "host=" + host +
		" port=" + port +
		" user=" + user +
		" password=" + os.Getenv("TEST_DB_PASSWORD") +
		" dbname=" + name +
		" sslmode=disable search_path=devops", true
}

func OpenGormPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn, ok := PostgresDSNFromEnv()
	if !ok {
		t.Skip("set TEST_DATABASE_DSN or TEST_DB_HOST/TEST_DB_NAME/TEST_DB_USER to run PostgreSQL integration tests")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	return db
}
