package runtime_test

import (
	"context"
	"testing"

	"df-build-server/internal/deploy/engine/runtime"
	"df-build-server/internal/deploy/engine/store"
	deployrepo "df-build-server/internal/deploy/repository"
	"df-build-server/internal/model"
	"df-build-server/internal/testutil"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"pgregory.net/rapid"
)

func newStore(t *rapid.T, dsn string) *deployrepo.GormStore {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := db.AutoMigrate(&model.Deployment{}, &model.DeploymentRunLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return deployrepo.NewGormStore(db)
}

// TestPropertySSEReplayContiguous checks CP-6: a subscriber attaching at any
// offset receives a contiguous, gap-free, in-order prefix of all log lines for
// the run (replayed from the store).
func TestPropertySSEReplayContiguous(t *testing.T) {
	dsn, ok := testutil.PostgresDSNFromEnv()
	if !ok {
		t.Skip("set TEST_DATABASE_DSN or TEST_DB_HOST/TEST_DB_NAME/TEST_DB_USER to run PostgreSQL integration tests")
	}
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		st := newStore(t, dsn)

		dep := &store.Deployment{TaskType: "deploy", TargetComponent: "etcd", Status: "running"}
		if err := st.CreateDeployment(ctx, dep); err != nil {
			t.Fatalf("create deployment: %v", err)
		}

		n := rapid.IntRange(0, 50).Draw(t, "n")
		for i := 0; i < n; i++ {
			if err := st.AppendDeploymentLog(ctx, &store.DeploymentLog{DeploymentID: dep.ID, Detail: "line"}); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
		afterSeq := int64(rapid.IntRange(0, n).Draw(t, "afterSeq"))

		hub := runtime.NewHub(st)
		// Mark finished so Subscribe replays history then closes the channel.
		hub.Close(dep.ID, "success")

		ch, _, _ := hub.Subscribe(ctx, dep.ID, afterSeq)

		var got []int64
		for entry := range ch {
			got = append(got, entry.Sequence)
		}

		// Expect sequences afterSeq+1 .. n, contiguous and ordered.
		wantLen := n - int(afterSeq)
		if wantLen < 0 {
			wantLen = 0
		}
		if len(got) != wantLen {
			t.Fatalf("replay len=%d want=%d (n=%d afterSeq=%d)", len(got), wantLen, n, afterSeq)
		}
		for i, seq := range got {
			if seq != afterSeq+int64(i)+1 {
				t.Fatalf("gap at %d: got seq %d want %d", i, seq, afterSeq+int64(i)+1)
			}
		}
	})
}
