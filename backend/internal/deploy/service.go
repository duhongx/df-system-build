// Package deploy is the root of the deployment-management subsystem.
//
// It migrates the HIS infrastructure offline-deployment engine (originally the
// standalone his-deploy / dfctl project) into df-build-system. The subsystem
// deploys ~23 infrastructure components (K8s system, middleware, base services)
// to target hosts over direct SSH, rendering runtime config from PostgreSQL on
// the fly and streaming logs over SSE.
//
// Boundaries:
//   - Target hosts reuse the existing Server Management feature (model.Server);
//     no duplicate host table is maintained.
//   - Business data is persisted in PostgreSQL via GORM (engine/store.Store is
//     implemented by repository.GormStore).
//   - Rollback markers live in State_Dir on each target host's local
//     filesystem, NOT in PostgreSQL.
//   - The 4.5 GB offline resource tree lives on disk as Resource_Dir (installed
//     via offline bundle); only small text artifacts are embedded.
package deploy

import (
	"path/filepath"
	"time"

	deployrepo "df-build-server/internal/deploy/repository"
	"df-build-server/internal/deploy/engine/runtime"
	"df-build-server/internal/deploy/engine/store"

	"gorm.io/gorm"
)

// Service is the deployment-management orchestrator facade. It wires the
// migrated engine runtime to df-build-system's PostgreSQL store and Server
// Management credential source, and exposes the pieces HTTP handlers need.
type Service struct {
	rt    *runtime.Runtime
	store store.Store
}

// Config configures the Service.
type Config struct {
	// ResourceDir is the on-disk offline-resource root (cluster.resource_dir).
	ResourceDir string
	// RunsDir is the parent directory for per-run rendered YAML + logs.
	RunsDir string
	// Timeout is the per-run deadline (default 30m when zero).
	Timeout time.Duration
}

// NewService builds the deployment-management service backed by the given DB.
func NewService(db *gorm.DB, cfg Config) *Service {
	st := deployrepo.NewGormStore(db)
	rt := runtime.New(runtime.Options{
		Store:              st,
		ResourceDir:        cfg.ResourceDir,
		RunsDir:            cfg.RunsDir,
		Timeout:            cfg.Timeout,
		TargetMode:         "ssh",
		CredentialResolver: NewServerCredentialResolver(db),
	})
	return &Service{rt: rt, store: st}
}

// Runtime exposes the engine runtime (Submit / Manager / Hub) to handlers.
func (s *Service) Runtime() *runtime.Runtime { return s.rt }

// Store exposes the persistence layer to handlers for config/target/state reads.
func (s *Service) Store() store.Store { return s.store }

// DefaultRunsDir returns the conventional runs directory under a workspace root.
func DefaultRunsDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, "deployment-runs")
}
