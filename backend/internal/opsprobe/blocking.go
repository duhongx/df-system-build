package opsprobe

import "sort"

type BlockingEdge struct {
	BlockerPID int `json:"blockerPid"`
	WaitingPID int `json:"waitingPid"`
}
type BlockingGroup struct {
	RootPID             int      `json:"rootPid"`
	WaitingPIDs         []int    `json:"waitingPids"`
	MaxWaitSeconds      *float64 `json:"maxWaitSeconds"`
	PreparedTransaction bool     `json:"preparedTransaction"`
	MissingSession      bool     `json:"missingSession"`
	Cycle               bool     `json:"cycle"`
}
type SessionSummary struct {
	Total             int `json:"total"`
	Active            int `json:"active"`
	Idle              int `json:"idle"`
	IdleInTransaction int `json:"idleInTransaction"`
	Waiting           int `json:"waiting"`
	LongTransactions  int `json:"longTransactions"`
}

// BuildBlockingGraph uses server-returned edges, preserving multiple blockers,
// external/missing sessions and prepared transactions (PID 0).
func BuildBlockingGraph(d *Diagnostic) {
	d.BlockingEdges = []BlockingEdge{}
	d.BlockingGroups = []BlockingGroup{}
	d.SessionSummary = SessionSummary{}
	byPID := map[int]*Activity{}
	cycles := map[int]bool{}
	for i := range d.Activities {
		a := &d.Activities[i]
		byPID[a.PID] = a
		if a.DBName == d.Database && (a.BackendType == "client backend" || a.BackendType == "") {
			s := &d.SessionSummary
			s.Total++
			switch a.State {
			case "active":
				s.Active++
			case "idle":
				s.Idle++
			case "idle in transaction", "idle in transaction (aborted)":
				s.IdleInTransaction++
			}
			if len(a.BlockingPIDs) > 0 {
				s.Waiting++
			}
			if a.TransactionSeconds >= 300 {
				s.LongTransactions++
			}
		}
		seen := map[int]bool{}
		for _, pid := range a.BlockingPIDs {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			d.BlockingEdges = append(d.BlockingEdges, BlockingEdge{pid, a.PID})
		}
		for _, lock := range d.Locks {
			if lock.PID == a.PID && !lock.Granted && lock.WaitStart != nil && (a.LockWaitSeconds == nil || lock.WaitSeconds > *a.LockWaitSeconds) {
				v := lock.WaitSeconds
				a.LockWaitSeconds = &v
			}
		}
	}
	var roots func(int, map[int]bool) []int
	roots = func(pid int, path map[int]bool) []int {
		if path[pid] {
			cycles[pid] = true
			return []int{pid}
		}
		a := byPID[pid]
		if a == nil || len(a.BlockingPIDs) == 0 {
			return []int{pid}
		}
		next := map[int]bool{}
		for k, v := range path {
			next[k] = v
		}
		next[pid] = true
		set := map[int]bool{}
		for _, p := range a.BlockingPIDs {
			for _, root := range roots(p, next) {
				set[root] = true
			}
		}
		out := []int{}
		for p := range set {
			out = append(out, p)
		}
		sort.Ints(out)
		return out
	}
	grouped := map[int]map[int]bool{}
	for i := range d.Activities {
		a := &d.Activities[i]
		a.RootBlockingPIDs = []int{}
		if len(a.BlockingPIDs) == 0 {
			continue
		}
		a.RootBlockingPIDs = roots(a.PID, map[int]bool{})
		if a.DBName != d.Database {
			continue
		}
		for _, root := range a.RootBlockingPIDs {
			if grouped[root] == nil {
				grouped[root] = map[int]bool{}
			}
			grouped[root][a.PID] = true
		}
	}
	for root, waiters := range grouped {
		g := BlockingGroup{RootPID: root, WaitingPIDs: []int{}, PreparedTransaction: root == 0, MissingSession: root != 0 && byPID[root] == nil, Cycle: cycles[root]}
		for pid := range waiters {
			g.WaitingPIDs = append(g.WaitingPIDs, pid)
			if a := byPID[pid]; a != nil && a.LockWaitSeconds != nil && (g.MaxWaitSeconds == nil || *a.LockWaitSeconds > *g.MaxWaitSeconds) {
				v := *a.LockWaitSeconds
				g.MaxWaitSeconds = &v
			}
		}
		sort.Ints(g.WaitingPIDs)
		d.BlockingGroups = append(d.BlockingGroups, g)
	}
	sort.Slice(d.BlockingGroups, func(i, j int) bool {
		if len(d.BlockingGroups[i].WaitingPIDs) != len(d.BlockingGroups[j].WaitingPIDs) {
			return len(d.BlockingGroups[i].WaitingPIDs) > len(d.BlockingGroups[j].WaitingPIDs)
		}
		return d.BlockingGroups[i].RootPID < d.BlockingGroups[j].RootPID
	})
}
