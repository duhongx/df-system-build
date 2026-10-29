package service

import (
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"testing"
)

func initIsolatedServiceTestDB(t *testing.T) {
	t.Helper()
	previous := repository.DB
	if err := repository.InitDB(testutil.IsolatedPostgresConfig(t)); err != nil {
		t.Fatalf("init isolated test database: %v", err)
	}
	current := repository.DB
	t.Cleanup(func() {
		pool, err := current.DB()
		if err == nil {
			_ = pool.Close()
		}
		repository.DB = previous
	})
}
