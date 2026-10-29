package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"df-build-server/pkg/config"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var isolatedConfigs sync.Map // *testing.T -> config.DatabaseConfig

// IsolatedPostgresConfig gives each integration test its own disposable
// database. Test application connections follow the same target, including
// tests that configure SQL execution from TEST_DB_* rather than the DSN.
func IsolatedPostgresConfig(t *testing.T) *config.DatabaseConfig {
	t.Helper()
	if cached, ok := isolatedConfigs.Load(t); ok {
		cfg := cached.(config.DatabaseConfig)
		return &cfg
	}
	dsn, ok := PostgresDSNFromEnv()
	if !ok {
		t.Skip("set TEST_DATABASE_DSN or TEST_DB_* for isolated PostgreSQL integration tests")
		return nil
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test configuration")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open PostgreSQL test administrator")
	}
	admin.SetMaxOpenConns(1)
	admin.SetMaxIdleConns(0)
	name := fmt.Sprintf("dfops_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	cancel()
	if err != nil {
		admin.Close()
		t.Fatalf("create disposable PostgreSQL database: %v", err)
	}
	t.Cleanup(func() {
		defer isolatedConfigs.Delete(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable PostgreSQL database: %v", err)
		}
		_ = admin.Close()
	})
	isolated := dsn + " dbname=" + name + " search_path=public"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL test URL")
		}
		u.Path = "/" + name
		u.RawPath = ""
		query := u.Query()
		query.Del("dbname")
		query.Set("search_path", "public")
		u.RawQuery = query.Encode()
		isolated = u.String()
	}
	for key, value := range map[string]string{"TEST_DATABASE_DSN": isolated, "TEST_DB_HOST": parsed.Host, "TEST_DB_PORT": strconv.Itoa(int(parsed.Port)), "TEST_DB_NAME": name, "TEST_DB_USER": parsed.User, "TEST_DB_PASSWORD": parsed.Password} {
		t.Setenv(key, value)
	}
	cfg := config.DatabaseConfig{DSN: isolated, Host: parsed.Host, Port: int(parsed.Port), Name: name, User: parsed.User, Password: parsed.Password, MaxOpenConns: 5, MaxIdleConns: 2}
	isolatedConfigs.Store(t, cfg)
	return &cfg
}

func PostgresConfig(t *testing.T) *config.DatabaseConfig {
	t.Helper()
	return IsolatedPostgresConfig(t)
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
		" sslmode=disable", true
}

func OpenGormPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := IsolatedPostgresConfig(t).DSN
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(5)
	pool.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = pool.Close() })
	return db
}
