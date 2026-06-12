package conflict

import (
	"testing"

	"df-build-server/internal/deploy/engine/store"

	"pgregory.net/rapid"
)

// TestPropertyValidateMatchesClosedForm checks CP-3: Validate returns no
// conflicts iff, for every server, no hard-conflict pair and no K8s+business
// mix is co-located.
func TestPropertyValidateMatchesClosedForm(t *testing.T) {
	allComponents := append(append([]string{}, K8sComponents...), BusinessComponents...)

	rapid.Check(t, func(t *rapid.T) {
		// Random bindings: each component -> random subset of hosts {1,2,3}.
		var targets []*store.ComponentTargets
		for _, comp := range allComponents {
			var hosts []int64
			for hid := int64(1); hid <= 3; hid++ {
				if rapid.Bool().Draw(t, comp+"@"+itoa(hid)) {
					hosts = append(hosts, hid)
				}
			}
			if len(hosts) > 0 {
				targets = append(targets, &store.ComponentTargets{ComponentName: comp, HostIDs: hosts})
			}
		}

		got := len(Validate(targets)) == 0
		want := !hasAnyConflict(targets)
		if got != want {
			t.Fatalf("Validate-empty=%v but closed-form-clean=%v for %v", got, want, targets)
		}
	})
}

// hasAnyConflict is the independent closed-form oracle.
func hasAnyConflict(targets []*store.ComponentTargets) bool {
	compsByHost := map[int64]map[string]bool{}
	for _, ct := range targets {
		for _, hid := range ct.HostIDs {
			if compsByHost[hid] == nil {
				compsByHost[hid] = map[string]bool{}
			}
			compsByHost[hid][ct.ComponentName] = true
		}
	}
	for _, set := range compsByHost {
		for _, p := range HardConflicts {
			if set[p.A] && set[p.B] {
				return true
			}
		}
		k8s, biz := false, false
		for c := range set {
			if IsK8s(c) {
				k8s = true
			}
			if IsBusiness(c) {
				biz = true
			}
		}
		if k8s && biz {
			return true
		}
	}
	return false
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
