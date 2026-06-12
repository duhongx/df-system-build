package repository

import (
	"context"
	"testing"

	"df-build-server/internal/deploy/engine/store"
	"df-build-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) *GormStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Server{},
		&model.DeploymentComponentTarget{},
		&model.DeploymentComponentState{},
		&model.DeploymentComponentOverride{},
		&model.Deployment{},
		&model.DeploymentRunLog{},
		&model.DeploymentEnvEntry{},
		&model.DeploymentEnabledComponent{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewGormStore(db)
}

func TestComponentDeployStateTransitions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Default state for an unknown component is not_deployed.
	st, err := s.GetComponentDeployState(ctx, "etcd")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if st.Status != store.DeployStateNotDeployed {
		t.Fatalf("default state = %q, want %q", st.Status, store.DeployStateNotDeployed)
	}

	// Transition through the state machine; the row must be upserted, not duplicated.
	for _, want := range []string{store.DeployStateDeployed, store.DeployStateFailed, store.DeployStateNotDeployed} {
		if err := s.SetComponentDeployState(ctx, "etcd", want, 7); err != nil {
			t.Fatalf("set %q: %v", want, err)
		}
		got, err := s.GetComponentDeployState(ctx, "etcd")
		if err != nil {
			t.Fatalf("get after set %q: %v", want, err)
		}
		if got.Status != want {
			t.Fatalf("state = %q, want %q", got.Status, want)
		}
		if got.LastDeploymentID != 7 {
			t.Fatalf("last deployment id = %d, want 7", got.LastDeploymentID)
		}
	}

	all, err := s.ListComponentDeployStates(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 state row (upsert), got %d", len(all))
	}
}

func TestReplaceTargetsDedupAndScope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Duplicate host IDs must collapse to a unique set.
	if err := s.ReplaceTargets(ctx, "redis", []int64{1, 1, 2}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	ct, err := s.GetTargets(ctx, "redis")
	if err != nil {
		t.Fatalf("get targets: %v", err)
	}
	if len(ct.HostIDs) != 2 {
		t.Fatalf("expected 2 unique hosts, got %v", ct.HostIDs)
	}

	// Replace is idempotent and overwrites prior selection.
	if err := s.ReplaceTargets(ctx, "redis", []int64{3}); err != nil {
		t.Fatalf("replace2: %v", err)
	}
	ct, _ = s.GetTargets(ctx, "redis")
	if len(ct.HostIDs) != 1 || ct.HostIDs[0] != 3 {
		t.Fatalf("expected [3], got %v", ct.HostIDs)
	}

	// CountTargetsByHost reflects current bindings.
	n, comps, err := s.CountTargetsByHost(ctx, 3)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 || len(comps) != 1 || comps[0] != "redis" {
		t.Fatalf("count=%d comps=%v, want 1 [redis]", n, comps)
	}
}

func TestDeploymentLogAppendAndListSince(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dep := &store.Deployment{TaskType: "deploy", TargetComponent: "etcd", Status: "RUNNING"}
	if err := s.CreateDeployment(ctx, dep); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if dep.ID == 0 {
		t.Fatal("deployment id not assigned")
	}

	// Append 3 logs with auto-assigned monotonic sequence.
	for i := 0; i < 3; i++ {
		if err := s.AppendDeploymentLog(ctx, &store.DeploymentLog{DeploymentID: dep.ID, Detail: "line"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	all, err := s.ListDeploymentLogs(ctx, dep.ID, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(all))
	}
	for i, l := range all {
		if l.Sequence != int64(i+1) {
			t.Fatalf("log %d sequence = %d, want %d", i, l.Sequence, i+1)
		}
	}

	// ListSince(1) returns only sequences > 1.
	since, err := s.ListDeploymentLogs(ctx, dep.ID, 1, 0)
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(since) != 2 || since[0].Sequence != 2 {
		t.Fatalf("expected 2 logs starting at seq 2, got %d", len(since))
	}
}
