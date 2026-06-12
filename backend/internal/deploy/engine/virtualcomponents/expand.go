package virtualcomponents

import "fmt"

// Inputs bundles the variables Expand needs. We pass it as a struct
// so adding new context (e.g. tenant, env) later doesn't ripple
// through every call site.
type Inputs struct {
	// UserSelectedHostIDs are the hosts the operator picked for this
	// virtual component on the UI card. Used by BindUserSelected.
	UserSelectedHostIDs []int64
	// DeployHostID is the host running dfctl-web. Used by BindDeployHost.
	// 0 means "no deploy host known" — Expand drops BindDeployHost
	// pipeline components silently in that case so that an unconfigured
	// install doesn't blow up. Callers that care about the difference
	// should pre-validate.
	DeployHostID int64
	// AllHostIDs is the inventory snapshot. Used by BindAllHosts (init,
	// check). Order is preserved so deploy logs are deterministic.
	AllHostIDs []int64
}

// ConcretePlacement is the resolved "this pipeline component runs on
// these hosts" tuple. The slice may be empty for a strategy that
// produces no targets (e.g. BindDeployHost with no DeployHostID set).
type ConcretePlacement struct {
	Component string
	HostIDs   []int64
}

// ValidateUserHostIDs checks operator-controlled target host counts
// before callers expand and persist pipeline targets.
func (c Component) ValidateUserHostIDs(hostIDs []int64) error {
	if c.MinUserHosts > 0 && len(hostIDs) < c.MinUserHosts {
		return fmt.Errorf("%s 至少选择 %d 台目标主机", c.Name, c.MinUserHosts)
	}
	if c.MaxUserHosts > 0 && len(hostIDs) > c.MaxUserHosts {
		return fmt.Errorf("%s 最多只能选择 %d 台目标主机", c.Name, c.MaxUserHosts)
	}
	return nil
}

// Expand resolves a virtual component into per-pipeline-component
// host targets. The order of the returned slice mirrors the
// registry's mapping order so deploy logs stay readable.
//
// We do NOT dedupe: if the same pipeline component appears twice in
// the same virtual component (no current case), each entry is
// returned separately. Callers that want a flat "what hosts is this
// pipeline component touching" can collapse the result themselves.
func (c Component) Expand(in Inputs) []ConcretePlacement {
	out := make([]ConcretePlacement, 0, len(c.Mappings))
	for _, m := range c.Mappings {
		var ids []int64
		switch m.HostBind {
		case BindUserSelected:
			ids = append(ids, in.UserSelectedHostIDs...)
		case BindDeployHost:
			if in.DeployHostID > 0 {
				ids = []int64{in.DeployHostID}
			}
		case BindAllHosts:
			ids = append(ids, in.AllHostIDs...)
		}
		out = append(out, ConcretePlacement{
			Component: m.Name,
			HostIDs:   ids,
		})
	}
	return out
}

// AggregateUserHosts is the reverse of Expand: given the database
// state of which hosts target which pipeline components, return the
// host set the UI should pre-select on this virtual component's card.
//
// Only mappings flagged BindUserSelected count toward the result.
// BindDeployHost / BindAllHosts pipeline components are managed by
// the system, not the operator, so their target hosts are irrelevant
// to the UI's "what did I pick last time?" question.
//
// When multiple BindUserSelected mappings point at different hosts,
// we union them. In practice this only matters for kubernetes-master
// (etcd / containerd / kube-lb / master / node / calico all share the
// same selection) so the union and any individual mapping's set
// agree.
func (c Component) AggregateUserHosts(realTargets map[string][]int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, m := range c.Mappings {
		if m.HostBind != BindUserSelected {
			continue
		}
		for _, id := range realTargets[m.Name] {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
