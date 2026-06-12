// Package render builds the YAML files dfctl's engine still expects
// (config.yml + custom.yml) from dfctl-web's database state. Every
// deployment run gets its own directory so old runs are debuggable
// without depending on the live DB.
package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"df-build-server/internal/deploy/engine/store"
)

// Inputs bundles the directories render targets so callers can compose
// the run dir however they like.
type Inputs struct {
	// RunDir is the directory the rendered files land in. Created
	// recursively if missing; never wiped (caller owns lifecycle).
	RunDir string
	// ResourceDir is forwarded to cluster.resource_dir. The engine
	// uses it to locate offline rpm/tar.gz files.
	ResourceDir string
}

// Output is what render produces: paths that the engine consumes plus
// counters useful for diagnostic logging.
type Output struct {
	ConfigPath string
	CustomPath string
	HostCount  int
	CompCount  int
}

// FromStore reads the relevant tables and writes config.yml +
// custom.yml under inputs.RunDir. Returns the on-disk paths so callers
// can pass them straight to the dfctl engine.
//
// The translation between the new "component → host" data model and
// dfctl's existing "host has roles, component has target_roles"
// schema happens here. We synthesize a `__deploy_<component>` role on
// each targeted host and pin every component's target_roles to that
// synthetic role; the engine's set-intersection logic then picks the
// right hosts without any change of its own.
func FromStore(ctx context.Context, st store.Store, in Inputs) (*Output, error) {
	if err := os.MkdirAll(in.RunDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir run dir: %w", err)
	}

	depSet, err := st.GetDeploymentSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load deployment settings: %w", err)
	}
	netSet, err := st.GetNetworkSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load network settings: %w", err)
	}
	envEntries, err := st.ListEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}
	hosts, err := st.ListHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("load hosts: %w", err)
	}
	enabled, err := st.ListEnabledComponents(ctx)
	if err != nil {
		return nil, fmt.Errorf("load enabled components: %w", err)
	}
	overrides, err := st.ListOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("load overrides: %w", err)
	}
	allTargets, err := st.ListAllTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("load component targets: %w", err)
	}
	// Hard-block strong-conflict groups before we render. The README's
	// 'Host Binding Constraints' section documents the matrix; we
	// enforce the same matrix here so an operator who clicks past the
	// UI / drives the API directly can't end up with two services
	// fighting over the same systemd unit name on the same host.
	if err := checkStrongConflicts(allTargets); err != nil {
		return nil, err
	}

	// Build a host-id → []synthetic-role map for assembling host YAML.
	rolesByHost := map[int64][]string{}
	// And a component → synthetic-role map for splicing into custom.yml.
	syntheticByComp := map[string]string{}
	for _, ct := range allTargets {
		role := syntheticRole(ct.ComponentName)
		syntheticByComp[ct.ComponentName] = role
		for _, hostID := range ct.HostIDs {
			// Two roles per (host, component) binding:
			//   - synthetic role pins target_roles routing
			//   - bare component name fuels role-based variable
			//     derivation in addDerivedDeploymentVariables
			//     (e.g. ${nexus.address} for docker login).
			// Without the bare alias, ${nexus.address} would leak
			// through templates as literal text.
			rolesByHost[hostID] = append(rolesByHost[hostID], role, ct.ComponentName)
		}
	}

	// Assemble config.yml.
	cfgDoc := map[string]any{}
	if len(envEntries) > 0 {
		envMap := map[string]any{}
		for _, e := range envEntries {
			envMap[e.Key] = e.Value
		}
		cfgDoc["env"] = envMap
	}
	cfgDoc["cluster"] = map[string]any{
		"resource_dir": in.ResourceDir,
		"remote_root":  depSet.RemoteRoot,
	}
	cfgDoc["deploy_components"] = componentNames(enabled)
	cfgDoc["hosts"] = hostsToYAML(hosts, rolesByHost)
	cfgDoc["network"] = networkToYAML(netSet)

	cfgPath := filepath.Join(in.RunDir, "config.yml")
	if err := writeYAML(cfgPath, cfgDoc); err != nil {
		return nil, err
	}

	// Assemble custom.yml. The engine treats it as a map keyed by
	// component name under a top-level `components:` key (matching the
	// legacy hand-edited custom.yml). Each entry carries the operator's
	// free-form params plus the synthetic target_roles override so the
	// engine's existing role-based routing keeps working.
	componentsDoc := map[string]any{}
	for _, o := range overrides {
		// Defensive copy so we don't mutate the store's cached map.
		params := map[string]any{}
		for k, v := range o.Params {
			params[k] = v
		}
		if role, ok := syntheticByComp[o.ComponentName]; ok {
			params["target_roles"] = []string{role}
		}
		componentsDoc[o.ComponentName] = params
	}
	// Components without overrides still need their target_roles
	// pointed at the synthetic role.
	for comp, role := range syntheticByComp {
		if _, exists := componentsDoc[comp]; exists {
			continue
		}
		componentsDoc[comp] = map[string]any{
			"target_roles": []string{role},
		}
	}
	customDoc := map[string]any{
		"components": componentsDoc,
	}
	customPath := filepath.Join(in.RunDir, "custom.yml")
	if err := writeYAML(customPath, customDoc); err != nil {
		return nil, err
	}

	return &Output{
		ConfigPath: cfgPath,
		CustomPath: customPath,
		HostCount:  len(hosts),
		CompCount:  len(enabled),
	}, nil
}

// syntheticRole turns a component name into the role string the render
// layer assigns to every host targeted by that component. The leading
// double-underscore prevents collisions with operator-defined roles
// from the legacy YAML era.
func syntheticRole(component string) string {
	return "__deploy_" + component
}

// CheckStrongConflicts is the exported wrapper of the internal
// strong-conflict check. The targets handler calls it on PUT to
// surface 422 *before* the operator hits Deploy and the render
// pipeline complains. Sharing the same code path keeps the policy
// in one place — render.go is still the source of truth for the
// conflict groups.
func CheckStrongConflicts(allTargets []*store.ComponentTargets) error {
	return checkStrongConflicts(allTargets)
}

// strongConflictGroups lists the component sets that absolutely
// cannot share a host because they collide on systemd service name,
// data directory, or both. See README's "主机绑定约束" section for the
// rationale; this slice is the runtime-enforced version.
var strongConflictGroups = []struct {
	Members []string
	Reason  string
}{
	{
		Members: []string{"etcd", "postgresql", "dns"},
		Reason:  "三个组件都注册 systemd etcd.service / /var/lib/etcd / /etc/etcd/ssl,共部署会互相覆盖",
	},
	{
		Members: []string{"redis", "df-ops"},
		Reason:  "都注册 systemd redis.service (df-ops 用 yum redis6),共部署会互相覆盖 service unit",
	},
	{
		Members: []string{"postgresql", "df-ops"},
		Reason:  "都装 postgresql.service + /opt/PostgreSQL/14/* (df-ops 是 ops 平台内置 PostgreSQL),共部署会互相覆盖配置和 data",
	},
}

// checkStrongConflicts inspects the component_targets snapshot for
// any host that's bound to two members of the same conflict group.
// Returns a 422-friendly error listing every offending (host, group)
// tuple so the operator can fix all conflicts in one pass.
func checkStrongConflicts(allTargets []*store.ComponentTargets) error {
	hostsByComp := map[string]map[int64]bool{}
	for _, ct := range allTargets {
		set := hostsByComp[ct.ComponentName]
		if set == nil {
			set = map[int64]bool{}
			hostsByComp[ct.ComponentName] = set
		}
		for _, hid := range ct.HostIDs {
			set[hid] = true
		}
	}
	type violation struct {
		HostID  int64
		Members []string
		Reason  string
	}
	var violations []violation
	for _, group := range strongConflictGroups {
		// Per host: collect members of this group bound to it.
		hostMembers := map[int64][]string{}
		for _, member := range group.Members {
			for hid := range hostsByComp[member] {
				hostMembers[hid] = append(hostMembers[hid], member)
			}
		}
		for hid, members := range hostMembers {
			if len(members) >= 2 {
				violations = append(violations, violation{
					HostID:  hid,
					Members: members,
					Reason:  group.Reason,
				})
			}
		}
	}
	if len(violations) == 0 {
		return nil
	}
	parts := make([]string, 0, len(violations))
	for _, v := range violations {
		parts = append(parts, fmt.Sprintf("host_id=%d 冲突组件 [%s]: %s",
			v.HostID, strings.Join(v.Members, ", "), v.Reason))
	}
	return fmt.Errorf("主机绑定冲突 (共 %d 处),修正后再部署:\n  %s",
		len(violations), strings.Join(parts, "\n  "))
}

func componentNames(enabled []*store.EnabledComponent) []string {
	out := make([]string, 0, len(enabled))
	for _, e := range enabled {
		out = append(out, e.Name)
	}
	return out
}

func hostsToYAML(hosts []*store.HostSpec, rolesByHost map[int64][]string) []map[string]any {
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		entry := map[string]any{
			"name":    h.Name,
			"address": h.Address,
		}
		// Hosts the operator created but didn't yet target are still
		// emitted (the engine's host inventory needs them) but with
		// a sentinel role so they don't accidentally match real
		// component target_roles. Empty would be rejected by the
		// engine validator.
		roles := rolesByHost[h.ID]
		if len(roles) == 0 {
			roles = []string{"__unassigned"}
		}
		entry["roles"] = roles
		if len(h.Metadata) > 0 {
			entry["metadata"] = h.Metadata
		}
		out = append(out, entry)
	}
	return out
}

func networkToYAML(n *store.NetworkSettings) map[string]any {
	out := map[string]any{
		"service_cidr":        n.ServiceCIDR,
		"cluster_cidr":        n.ClusterCIDR,
		"node_cidr_mask_size": n.NodeCIDRMaskSize,
	}
	if n.VIP != "" {
		out["vip"] = n.VIP
	}
	return out
}

func writeYAML(path string, doc map[string]any) error {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	// 0o600: rendered yaml carries plaintext passwords
	// (superuser_password / repl_password / admin_password etc.).
	// 0o644 was leaving them readable by any local OS user.
	//
	// Atomic write: temp file + rename so a partial write (disk full,
	// crash) never leaves a half-written yaml that the engine would
	// fail to parse on its next load.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dfctl-render-*")
	if err != nil {
		return fmt.Errorf("create tmp for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close tmp %s: %w", path, err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod tmp %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
