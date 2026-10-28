package opsprobe

import (
	"testing"
	"time"
)

func TestBlockingGraphPreservesRootsAndWaitEvidence(t *testing.T) {
	now := time.Now()
	d := &Diagnostic{Database: "app", Activities: []Activity{
		{PID: 1, DBName: "app", BackendType: "client backend", State: "idle in transaction", TransactionSeconds: 800},
		{PID: 2, DBName: "app", BackendType: "client backend", State: "active", BlockingPIDs: []int{1, 1}, ElapsedSeconds: 90},
		{PID: 3, DBName: "app", BackendType: "client backend", State: "active", BlockingPIDs: []int{2, 4}},
		{PID: 4, DBName: "other", BackendType: "client backend"},
		{PID: 5, DBName: "app", BackendType: "client backend", BlockingPIDs: []int{0}},
		{PID: 6, DBName: "app", BackendType: "client backend", BlockingPIDs: []int{999}},
	}, Locks: []Lock{{PID: 2, Granted: false, WaitStart: &now, WaitSeconds: 12}}}
	BuildBlockingGraph(d)
	if len(d.BlockingEdges) != 5 {
		t.Fatalf("edges %+v", d.BlockingEdges)
	}
	if d.SessionSummary.Total != 5 || d.SessionSummary.Active != 2 || d.SessionSummary.IdleInTransaction != 1 || d.SessionSummary.LongTransactions != 1 {
		t.Fatalf("summary %+v", d.SessionSummary)
	}
	if got := d.Activities[2].RootBlockingPIDs; len(got) != 2 || got[0] != 1 || got[1] != 4 {
		t.Fatalf("multiple roots lost: %v", got)
	}
	if d.Activities[1].LockWaitSeconds == nil || *d.Activities[1].LockWaitSeconds != 12 || d.Activities[2].LockWaitSeconds != nil {
		t.Fatal("query age used as lock wait duration")
	}
	groups := map[int]BlockingGroup{}
	for _, g := range d.BlockingGroups {
		groups[g.RootPID] = g
	}
	if len(groups[1].WaitingPIDs) != 2 || !groups[0].PreparedTransaction || !groups[999].MissingSession {
		t.Fatalf("groups %+v", groups)
	}
	BuildBlockingGraph(d)
	if len(d.BlockingEdges) != 5 {
		t.Fatal("rebuilding duplicated edges")
	}
}

func TestBlockingGraphCycleDoesNotRecurseForever(t *testing.T) {
	d := &Diagnostic{Database: "app", Activities: []Activity{{PID: 1, DBName: "app", BlockingPIDs: []int{2}}, {PID: 2, DBName: "app", BlockingPIDs: []int{1}}}}
	BuildBlockingGraph(d)
	if len(d.BlockingGroups) == 0 || !d.BlockingGroups[0].Cycle {
		t.Fatalf("missing cycle marker: %+v", d.BlockingGroups)
	}
}
