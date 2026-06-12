// Package virtualcomponents bridges the gap between two views of "what
// is a component":
//
//  1. The dfctl engine sees 26 fine-grained pipeline components (etcd,
//     containerd, calico, controller-render, ...). It cares about
//     execution order, dependencies and per-component task lists.
//
//  2. The UI operator wants ~18 high-level groupings ("Kubernetes
//     Master", "Init", "Nexus") that match the conceptual deploy
//     steps. They don't want to know calico exists; they want a
//     single "I'm installing K8s on these nodes" checkbox.
//
// This package is the single source of truth for the mapping. It
// describes:
//
//   - which virtual components the UI exposes,
//   - which pipeline components each one expands to,
//   - how the operator's host selection translates to per-pipeline
//     component host targets.
//
// Handlers go through `All()` / `Find()` for what to show the UI, and
// through `Expand()` / `AggregateUserHosts()` when reading/writing
// component_targets in the database.
package virtualcomponents

import "sort"

// Component is one user-facing component card.
type Component struct {
	// Name is the stable identifier the UI sends back on save.
	Name string
	// DisplayName is the localized label shown to operators.
	DisplayName string
	// Description is the short paragraph under the card title.
	Description string
	// Order drives the deploy sequence and the UI ordering. Lower
	// numbers run first.
	Order int
	// Mappings declares which pipeline components participate when
	// this virtual component is deployed, and how each maps to host
	// targets.
	Mappings []Mapping
	// MinUserHosts / MaxUserHosts constrain operator-selected hosts
	// for BindUserSelected mappings. Zero means "not constrained".
	MinUserHosts int
	MaxUserHosts int
}

// Mapping ties one pipeline component to a host-binding strategy.
type Mapping struct {
	Name     string
	HostBind HostBindingStrategy
}

// HostBindingStrategy decides how a pipeline component picks its
// target hosts when the parent virtual component is deployed.
type HostBindingStrategy int

const (
	// BindUserSelected: the pipeline component runs on whatever
	// hosts the operator selected for the parent virtual component.
	// Used for the bulk of "real" components like postgresql or nfs.
	BindUserSelected HostBindingStrategy = iota
	// BindDeployHost: the pipeline component runs on the dfctl-web
	// host (the deploy/runner machine). The operator's selection is
	// ignored. Used for components like docker (only the deploy host
	// has the offline image tarballs locally) or plugin (kubectl
	// apply runs from the deploy host's kubeconfig).
	BindDeployHost
	// BindAllHosts: the pipeline component runs on every host in the
	// inventory. Used for cluster-wide passes like check or init.
	BindAllHosts
)

// registry is the canonical list. Edit here to add or rearrange
// virtual components.
//
// Order ranges:
//
//	-20..-1  → infra-prep (init, check)
//	0..50    → mid-tier (nexus/nfs/slb/k8s/db/middleware)
//	100+     → finalizers (plugin, dns)
//
// Some pipeline components are intentionally absent from the registry
// because they don't belong on a UI surface — they're either covered
// by a parent virtual component (etcd inside kubernetes-master) or
// triggered as a side effect (preflight inside init).
var registry = []Component{
	{
		Name:        "init",
		DisplayName: "初始化",
		Description: "部署前预检:在部署机校验配置(CIDR/环境名)和离线包完整性,并逐主机确认离线资源就绪。不做操作系统配置(防火墙/内核参数等由操作系统初始化阶段完成,见环境检查)。",
		Order:       -20,
		Mappings: []Mapping{
			{Name: "preflight", HostBind: BindAllHosts},
			{Name: "prepare", HostBind: BindAllHosts},
		},
	},
	{
		Name:        "check",
		DisplayName: "环境检查",
		Description: "操作系统初始化配置验收:hostname/firewalld/SELinux/Swap/ulimit/离线 yum 源/时间同步/SSH 互信等。只读检查,失败即中止,不改动主机。必须先于初始化和部署执行。",
		Order:       -30,
		Mappings: []Mapping{
			{Name: "check", HostBind: BindAllHosts},
		},
	},
	{
		Name:        "nexus",
		DisplayName: "Nexus 私服",
		Description: "在选定主机上部署 Nexus 镜像私服,然后在部署机上把离线 docker 镜像 push 进去。",
		Order:       0,
		Mappings: []Mapping{
			{Name: "nexus", HostBind: BindUserSelected},
			// docker 永远跑在部署机:它的工作是 docker load + tag + push
			// 到 nexus,只有部署机上有离线 .tar 资源。
			{Name: "docker", HostBind: BindDeployHost},
		},
	},
	{
		Name:        "nfs",
		DisplayName: "NFS 共享",
		Description: "在选定主机上部署 NFS server,创建共享目录供 K8s 业务使用。",
		Order:       5,
		Mappings: []Mapping{
			{Name: "nfs", HostBind: BindUserSelected},
		},
	},
	{
		Name:         "slb",
		DisplayName:  "负载均衡 (haproxy)",
		Description:  "在选定主机上部署一台 haproxy,代理 kube-apiserver / ingress(80/443) / postgres patroni / minio。永远 1 台,无 keepalived 无 VIP。",
		Order:        10,
		MinUserHosts: 1,
		MaxUserHosts: 1,
		Mappings: []Mapping{
			{Name: "slb", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "kubernetes-master",
		DisplayName: "Kubernetes Master",
		Description: "选 1/3 台主机作为 K8s 控制面。系统自动展开:在部署机生成证书,在选中主机部署 etcd / containerd / kube-lb / master / kubelet / calico CNI。master 主机同时也跑业务 pod(单/三节点合一场景)。",
		Order:       20,
		Mappings: []Mapping{
			// controller-render 必须在部署机上跑(cfssl + kubectl 都装在那)
			{Name: "controller-render", HostBind: BindDeployHost},
			{Name: "etcd", HostBind: BindUserSelected},
			{Name: "containerd", HostBind: BindUserSelected},
			{Name: "kube-lb", HostBind: BindUserSelected},
			{Name: "master", HostBind: BindUserSelected},
			// node 也部署在 master 主机上,master 自带 kubelet/kube-proxy
			{Name: "node", HostBind: BindUserSelected},
			{Name: "calico", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "kubernetes-node",
		DisplayName: "Kubernetes Worker",
		Description: "选 0/N 台主机作为 K8s worker。仅在 6 节点(master/worker 分离)场景下使用,1/3 节点全合一时无需选。",
		Order:       30,
		Mappings: []Mapping{
			{Name: "containerd", HostBind: BindUserSelected},
			{Name: "kube-lb", HostBind: BindUserSelected},
			{Name: "node", HostBind: BindUserSelected},
			// calico-node DaemonSet 调度到所有 K8s 节点(master+worker),
			// 通过 hostPath 读 /etc/calico/ssl/ 下的证书去连 etcd。
			// ansible 时代 08.calico.yml hosts: [master, node],worker
			// 也跑 calico role 拿证书。dfctl 必须在 worker 也铺一份证书,
			// 否则 worker 上的 calico-node pod 起不来,Pod sandbox 创建
			// 失败,所有业务 pod ContainerCreating 卡死。
			{Name: "calico", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "postgresql",
		DisplayName: "PostgreSQL",
		Description: "1 台单机或 3 台 Patroni HA 集群。集群模式下客户端通过 SLB 5000 端口访问,单机直连。",
		Order:       40,
		Mappings: []Mapping{
			{Name: "postgresql", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "redis",
		DisplayName: "Redis",
		Description: "单机部署。",
		Order:       45,
		Mappings: []Mapping{
			{Name: "redis", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "rabbitmq",
		DisplayName: "RabbitMQ",
		Description: "单机部署。早期支持集群,因网络抖动导致脑裂改成单机。客户端直连。",
		Order:       50,
		Mappings: []Mapping{
			{Name: "rabbitmq", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "elasticsearch",
		DisplayName: "Elasticsearch",
		Description: "1 台单机或 3 台集群。",
		Order:       55,
		Mappings: []Mapping{
			{Name: "elasticsearch", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "minio",
		DisplayName: "MinIO",
		Description: "1 台单机或 4 台集群。集群模式下客户端通过 SLB 8000/9000 端口访问,单机直连。",
		Order:       60,
		Mappings: []Mapping{
			{Name: "minio", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "ftp",
		DisplayName: "FTP (vsftpd)",
		Description: "在选定主机上部署 vsftpd,创建数据目录。",
		Order:       70,
		Mappings: []Mapping{
			{Name: "ftp", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "skywalking",
		DisplayName: "SkyWalking",
		Description: "OAP + Web 应用监控,后端写 Elasticsearch。",
		Order:       75,
		Mappings: []Mapping{
			{Name: "skywalking", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "df-ops",
		DisplayName: "DF-Ops",
		Description: "运维管理应用,自带内置 PostgreSQL + Redis。",
		Order:       80,
		Mappings: []Mapping{
			{Name: "df-ops", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "nacos",
		DisplayName: "Nacos",
		Description: "K8s 上以 Service 形式部署的注册/配置中心,后端连 PostgreSQL。",
		Order:       85,
		Mappings: []Mapping{
			{Name: "nacos", HostBind: BindUserSelected},
		},
	},
	{
		Name:        "plugin",
		DisplayName: "K8s 业务插件",
		Description: "在 K8s 集群里 apply 业务相关 manifest:nfs-provisioner / his configmap / his ingress / CoreDNS DaemonSet。固定从部署机执行 kubectl。",
		Order:       100,
		Mappings: []Mapping{
			{Name: "plugin", HostBind: BindDeployHost},
		},
	},
	{
		Name:        "dns",
		DisplayName: "DNS (CoreDNS)",
		Description: "部署 CoreDNS,把 his.com 的 6 个域名指向 SLB 主机 IP,业务通过域名访问 ingress。永远跟 SLB 配套部署。",
		Order:       110,
		Mappings: []Mapping{
			{Name: "dns", HostBind: BindUserSelected},
		},
	},
}

// All returns the registered virtual components ordered by Order ASC.
// Callers may mutate the returned slice freely without affecting the
// registry.
func All() []Component {
	out := make([]Component, len(registry))
	copy(out, registry)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// Find looks up a virtual component by name. Returns ok=false if name
// isn't registered, so callers can decide whether to 404 the request.
func Find(name string) (Component, bool) {
	for _, c := range registry {
		if c.Name == name {
			return c, true
		}
	}
	return Component{}, false
}

// PipelineComponents flattens the registry's mappings into a deduped
// list of pipeline component names. Used by the storage layer to know
// which pipeline names are "owned" by a virtual component vs being
// surfaced raw to the user.
func PipelineComponents() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range registry {
		for _, m := range c.Mappings {
			if seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

// VirtualComponentForPipeline returns the virtual component that
// "owns" a given pipeline component name. Returns ok=false when the
// pipeline component is not part of any registered mapping (e.g. a
// pipeline component the UI doesn't yet surface).
//
// When more than one virtual component claims the same pipeline
// component (etcd is in kubernetes-master only; containerd is in both
// kubernetes-master and kubernetes-node), the first match wins by
// registry order. Callers needing the full set should use the
// `Owners` function instead.
func VirtualComponentForPipeline(pipelineName string) (Component, bool) {
	for _, c := range registry {
		for _, m := range c.Mappings {
			if m.Name == pipelineName {
				return c, true
			}
		}
	}
	return Component{}, false
}

// Owners returns every virtual component that contains the given
// pipeline component. This matters for shared mappings — when a host
// is targeted to a pipeline component that lives in two virtual
// components (e.g. containerd inside both kubernetes-master and
// kubernetes-node), the UI needs to attribute the host to the right
// "card" depending on whether it was originally selected as master or
// worker.
func Owners(pipelineName string) []Component {
	var out []Component
	for _, c := range registry {
		for _, m := range c.Mappings {
			if m.Name == pipelineName {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
