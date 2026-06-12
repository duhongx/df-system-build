// Package conflict enforces host-binding co-location rules for deployment
// components: the hard-conflict component groups (shared systemd service /
// data dir / install path) and the K8s-vs-business separation rule.
//
// It operates on the engine store's ComponentTargets snapshot so the same
// policy is reusable from the targets handler (PUT-time rejection) and the
// deployment run pre-check.
package conflict

import (
	"fmt"
	"sort"

	"df-build-server/internal/deploy/engine/store"
)

// K8sComponents are the Kubernetes-system components. They must run only on
// K8s nodes and never co-locate with business components.
var K8sComponents = []string{
	"controller-render", "containerd", "etcd", "kube-lb", "master", "node", "calico", "plugin",
}

// BusinessComponents are middleware / base-infra components that must not
// share a host with K8s-system components.
var BusinessComponents = []string{
	"postgresql", "redis", "rabbitmq", "elasticsearch", "minio", "nacos",
	"nfs", "ftp", "dns", "skywalking", "df-ops", "docker", "nexus", "slb",
}

// Pair is a hard-conflict component pair (cannot share a host).
type Pair struct {
	A      string
	B      string
	Reason string
}

// HardConflicts are component pairs that collide on systemd service name,
// data directory, or install path.
var HardConflicts = []Pair{
	{"etcd", "postgresql", "都注册 systemd etcd.service / /var/lib/etcd / /etc/etcd/ssl，共部署会互相覆盖"},
	{"etcd", "dns", "都注册 systemd etcd.service / /var/lib/etcd / /etc/etcd/ssl，共部署会互相覆盖"},
	{"postgresql", "dns", "都注册 systemd etcd.service / /var/lib/etcd / /etc/etcd/ssl，共部署会互相覆盖"},
	{"redis", "df-ops", "都注册 systemd redis.service（df-ops 用 yum redis6），共部署会互相覆盖 service unit"},
	{"postgresql", "df-ops", "都装 postgresql.service + /opt/PostgreSQL/14/*，共部署会互相覆盖配置和 data"},
}

var k8sSet = toSet(K8sComponents)
var businessSet = toSet(BusinessComponents)

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// IsK8s reports whether a component code belongs to the K8s system set.
func IsK8s(code string) bool { return k8sSet[code] }

// IsBusiness reports whether a component code is a business component.
func IsBusiness(code string) bool { return businessSet[code] }

// Conflict describes one co-location violation on a host.
type Conflict struct {
	ServerID   int64    `json:"serverId"`
	Components []string `json:"components"`
	Reason     string   `json:"reason"`
}

func (c Conflict) String() string {
	return fmt.Sprintf("server_id=%d 冲突组件 [%v]: %s", c.ServerID, c.Components, c.Reason)
}

// Validate returns every co-location violation in the given bindings. An empty
// result means all bindings satisfy both the hard-conflict pairs and the
// K8s-vs-business separation rule.
func Validate(targets []*store.ComponentTargets) []Conflict {
	// Build host -> set(components).
	compsByHost := map[int64]map[string]bool{}
	for _, ct := range targets {
		if ct == nil {
			continue
		}
		for _, hid := range ct.HostIDs {
			set := compsByHost[hid]
			if set == nil {
				set = map[string]bool{}
				compsByHost[hid] = set
			}
			set[ct.ComponentName] = true
		}
	}

	var out []Conflict
	// Stable host iteration order for deterministic output.
	hostIDs := make([]int64, 0, len(compsByHost))
	for hid := range compsByHost {
		hostIDs = append(hostIDs, hid)
	}
	sort.Slice(hostIDs, func(i, j int) bool { return hostIDs[i] < hostIDs[j] })

	for _, hid := range hostIDs {
		set := compsByHost[hid]

		// Hard-conflict pairs.
		for _, p := range HardConflicts {
			if set[p.A] && set[p.B] {
				out = append(out, Conflict{
					ServerID:   hid,
					Components: sortedPair(p.A, p.B),
					Reason:     p.Reason,
				})
			}
		}

		// K8s vs business separation.
		var k8s, biz []string
		for comp := range set {
			if IsK8s(comp) {
				k8s = append(k8s, comp)
			} else if IsBusiness(comp) {
				biz = append(biz, comp)
			}
		}
		if len(k8s) > 0 && len(biz) > 0 {
			sort.Strings(k8s)
			sort.Strings(biz)
			out = append(out, Conflict{
				ServerID:   hid,
				Components: append(append([]string{}, k8s...), biz...),
				Reason:     "K8s 体系组件与业务组件不能同机部署（kubelet 资源预留 / 内核调优 / iptables 规则会与业务进程互相干扰）",
			})
		}
	}
	return out
}

func sortedPair(a, b string) []string {
	if a <= b {
		return []string{a, b}
	}
	return []string{b, a}
}
