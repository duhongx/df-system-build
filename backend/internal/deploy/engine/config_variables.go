package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func deploymentVariables(envFile deploymentEnvFile, customFile deploymentCustomFile, cluster ClusterConfig) map[string]string {
	vars := map[string]string{
		"cluster.name":         cluster.Name,
		"cluster.resource_dir": cluster.ResourceDir,
		"cluster.remote_root":  cluster.RemoteRoot,
		"cluster.state_dir":    cluster.StateDir,
		"env.name":             envFile.Env.Name,
		"env.resource_dir":     envFile.Env.ResourceDir,
		"env.remote_root":      envFile.Env.RemoteRoot,
		"env.state_dir":        envFile.Env.StateDir,
	}
	flattenInto(vars, "network", envFile.Network)
	flattenInto(vars, "vars", envFile.Vars)
	flattenInto(vars, "global", customFile.Global)
	for component, values := range customFile.Components {
		flattenInto(vars, component, values)
		flattenInto(vars, "components."+component, values)
	}
	return vars
}

func addDerivedDeploymentVariables(vars map[string]string, hosts []Host, slbCfg SLBConfig) {
	addressesByRole := map[string][]string{}
	for _, host := range hosts {
		for _, role := range host.Roles {
			addressesByRole[role] = append(addressesByRole[role], host.Address)
		}
		if _, exists := vars["kubernetes.api_server"]; !exists && hasRole(host, "master") {
			vars["kubernetes.api_server"] = "https://" + host.Address + ":6443"
		}
	}
	for role := range addressesByRole {
		sort.Strings(addressesByRole[role])
	}

	// slb.address is the canonical "where do clients reach the haproxy
	// frontend?" value:
	//
	//   - If at least one host is targeted to the slb component → the
	//     first slb host's IP. (We mandate exactly one in production
	//     but tolerate more for tests.)
	//   - Otherwise → fall back to the first master IP so single-node
	//     installations without an SLB still produce a usable address
	//     for templates that reference ${slb.address}.
	//
	// slb.vip is kept as an alias pointing at the same value so any
	// pipeline still using the old name keeps working without churn
	// during the cutover.
	slbAddress := ""
	if slbHosts := addressesByRole["slb"]; len(slbHosts) > 0 {
		slbAddress = slbHosts[0]
	} else if masters := addressesByRole["master"]; len(masters) > 0 {
		slbAddress = masters[0]
	}
	// slbCfg.VIP only survives as a manual override for legacy
	// hosts.slb.vip configurations.
	if slbCfg.VIP != "" {
		slbAddress = slbCfg.VIP
	}
	vars["slb.address"] = slbAddress
	vars["slb.vip"] = slbAddress

	if masters := addressesByRole["master"]; len(masters) > 0 {
		vars["master.addresses"] = strings.Join(masters, " ")
	}
	if nodes := addressesByRole["node"]; len(nodes) > 0 {
		vars["node.addresses"] = strings.Join(nodes, " ")
	}
	kubernetesNodeAddresses := uniqueStrings(append(append([]string{}, addressesByRole["master"]...), addressesByRole["node"]...))
	if len(kubernetesNodeAddresses) > 0 {
		vars["kubernetes.node_addresses"] = strings.Join(kubernetesNodeAddresses, " ")
		vars["kubernetes.node_count"] = strconv.Itoa(len(kubernetesNodeAddresses))
	}
	if endpoints := etcdEndpoints(addressesByRole["etcd"]); endpoints != "" {
		vars["etcd.endpoints"] = endpoints
		vars["etcd.nodes"] = etcdInitialCluster(addressesByRole["etcd"])
	}
	if pgHosts := addressesByRole["postgresql"]; len(pgHosts) > 0 {
		vars["postgresql.mode"] = deploymentModeForCount(len(pgHosts))
		vars["postgresql.nacos_host"] = pgHosts[0]
		vars["postgresql.nacos_port"] = "5432"
		// Cluster postgres (HA): nacos goes through the SLB on the
		// patroni rw port, so failover is transparent.
		if len(pgHosts) >= 2 {
			if slbAddr := vars["slb.address"]; slbAddr != "" {
				vars["postgresql.nacos_host"] = slbAddr
			}
			vars["postgresql.nacos_port"] = "5000"
		}
		if vars["nacos.db_name"] == "" {
			if dfOpsDBName := vars["df-ops.db_name"]; dfOpsDBName != "" {
				vars["nacos.db_name"] = dfOpsDBName
			} else {
				vars["nacos.db_name"] = "df_his"
			}
		}
		vars["postgresql.etcd_nodes"] = postgresqlEtcdInitialCluster(pgHosts)
		vars["postgresql.patroni_etcd_hosts"] = postgresqlPatroniEtcdHosts(pgHosts)
		vars["postgresql.backup_address"] = postgresqlBackupAddress(pgHosts)
		vars["postgresql.etcd_csr_hosts_json"] = jsonHostLines(uniqueStrings(append(append([]string{}, pgHosts...), "127.0.0.1")))
		serverCSRHosts := append([]string{}, pgHosts...)
		if vip := vars["slb.vip"]; vip != "" {
			serverCSRHosts = append(serverCSRHosts, vip)
		}
		vars["postgresql.server_csr_hosts_json"] = jsonHostLines(uniqueStrings(append(serverCSRHosts, "127.0.0.1")))
	}
	if esHosts := addressesByRole["elasticsearch"]; len(esHosts) > 0 {
		vars["elasticsearch.seed_hosts"] = quotedList(esHosts)
		vars["elasticsearch.initial_master_nodes"] = quotedList(esHosts)
		vars["elasticsearch.seed_hosts_yaml"] = yamlQuotedLines(esHosts)
		vars["elasticsearch.initial_master_nodes_yaml"] = yamlQuotedLines(esHosts)
		vars["elasticsearch.node_count"] = strconv.Itoa(len(esHosts))
		vars["elasticsearch.mode"] = deploymentModeForCount(len(esHosts))
	}
	if minioHosts := addressesByRole["minio"]; len(minioHosts) > 0 {
		vars["minio.mode"] = deploymentModeForCount(len(minioHosts))
		vars["minio.cluster_volumes"] = minioClusterVolumes(minioHosts, vars["minio.data_dir"])
	}
	if masters := addressesByRole["master"]; len(masters) > 0 {
		vars["nacos.cluster_lines"] = nacosClusterLines(masters)
	}
	if cidr := stringValue(envNetworkValue(vars, "network.service_cidr")); cidr != "" {
		vars["kubernetes.service_ip"] = firstServiceIP(cidr)
		vars["kubernetes.dns_service_ip"] = nthServiceIP(cidr, 2)
		vars["plugin.cluster_dns_svc_ip"] = nthServiceIP(cidr, 2)
	}
	if vars["kubernetes.local_dns_cache"] == "" {
		vars["kubernetes.local_dns_cache"] = "169.254.20.10"
	}
	// node_cidr_len comes from the network plan. The UI / store / render
	// surface this as `node_cidr_mask_size` (the K8s-correct term, matches
	// kube-controller-manager's --node-cidr-mask-size flag); older
	// hand-written config.yml used `node_cidr_len`. Accept both, preferring
	// the canonical node_cidr_mask_size, so editing it in the UI actually
	// takes effect.
	if vars["kubernetes.node_cidr_len"] == "" && vars["network.node_cidr_mask_size"] != "" {
		vars["kubernetes.node_cidr_len"] = vars["network.node_cidr_mask_size"]
	}
	if vars["kubernetes.node_cidr_len"] == "" && vars["network.node_cidr_len"] != "" {
		vars["kubernetes.node_cidr_len"] = vars["network.node_cidr_len"]
	}
	if vars["plugin.local_dns_cache"] == "" {
		vars["plugin.local_dns_cache"] = vars["kubernetes.local_dns_cache"]
	}
	if vars["node.enable_local_dns_cache"] == "" {
		vars["node.enable_local_dns_cache"] = "true"
	}
	if vars["node.kube_reserved_enabled"] == "" {
		vars["node.kube_reserved_enabled"] = "no"
	}
	if vars["node.sys_reserved_enabled"] == "" {
		vars["node.sys_reserved_enabled"] = "no"
	}
	if vars["node.resolv_conf"] == "" {
		vars["node.resolv_conf"] = "/etc/resolv.conf"
	}
	if vars["node.kubelet_root_dir"] == "" {
		vars["node.kubelet_root_dir"] = "/var/lib/kubelet"
	}
	if vars["kubernetes.node_cidr_len"] == "" {
		vars["kubernetes.node_cidr_len"] = "24"
	}
	for role, addresses := range addressesByRole {
		if len(addresses) > 0 {
			vars[role+".address"] = addresses[0]
			vars[role+".addresses"] = strings.Join(addresses, " ")
		}
	}
	// Ensure etcd.addresses is always set to avoid ${etcd.addresses} leaking as literal
	// when etcd role has no dedicated hosts (e.g., K8s internal etcd or single-node deploy).
	// Empty value triggers fallback to master addresses in kubernetes_artifacts action.
	if _, ok := vars["etcd.addresses"]; !ok {
		vars["etcd.addresses"] = ""
	}
	vars["haproxy.master_servers"] = haproxyServerLines(addressesByRole["master"], "6443", "check inter 5s fall 2 rise 2 weight 1")
	vars["haproxy.node_http_servers"] = haproxyServerLines(addressesByRole["node"], "80", "check inter 5s fall 2 rise 2 weight 1")
	vars["haproxy.node_https_servers"] = haproxyServerLines(addressesByRole["node"], "443", "check inter 5s fall 2 rise 2 weight 1")
	// rabbitmq listen entries: SLB exposes 5672 (AMQP) + 15672
	// (management) so HA rabbitmq clusters get a single client-facing
	// VIP. Single-node deployments still get the listen block but
	// haproxy points at one backend (or the no-backend sentinel).
	vars["haproxy.rabbitmq_servers"] = haproxyServerLines(addressesByRole["rabbitmq"], "5672", "check inter 5s fall 2 rise 2 weight 1")
	vars["haproxy.rabbitmq_management_servers"] = haproxyServerLines(addressesByRole["rabbitmq"], "15672", "check inter 5s fall 2 rise 2 weight 1")
	// minio + postgresql servers degrade to "# no backend" when the
	// component is single-node. The slb haproxy.cfg always defines the
	// listen blocks so the layout is stable, but with a sentinel server
	// line haproxy still parses successfully and clients that do hit the
	// listen port get a clean refused response instead of a half-open
	// connection.
	vars["haproxy.minio_console_servers"] = haproxyClusterServerLines(addressesByRole["minio"], "8000", "check inter 5s fall 2 rise 2 weight 1")
	vars["haproxy.minio_api_servers"] = haproxyClusterServerLines(addressesByRole["minio"], "9000", "check inter 5s fall 2 rise 2 weight 1")
	vars["haproxy.postgresql_servers"] = haproxyClusterServerLines(addressesByRole["postgresql"], "5432", "verify none maxconn 5000 check check-ssl port 8008")
	vars["kube_lb.master_servers"] = kubeLBServerLines(addressesByRole["master"])
}

func deploymentModeForCount(count int) string {
	if count <= 1 {
		return "single"
	}
	return "cluster"
}

func applyVariablesToConfig(cfg *Config, vars map[string]string) {
	cfg.Cluster.Name = replaceVariables(cfg.Cluster.Name, vars)
	cfg.Cluster.ResourceDir = replaceVariables(cfg.Cluster.ResourceDir, vars)
	cfg.Cluster.RemoteRoot = replaceVariables(cfg.Cluster.RemoteRoot, vars)
	cfg.Cluster.StateDir = replaceVariables(cfg.Cluster.StateDir, vars)
	for componentName, component := range cfg.Components {
		component.DisplayName = replaceVariables(component.DisplayName, vars)
		for taskIndex, task := range component.Tasks {
			task.ID = replaceVariables(task.ID, vars)
			task.Name = replaceVariables(task.Name, vars)
			task.Phase = replaceVariables(task.Phase, vars)
			for actionIndex, action := range task.Actions {
				task.Actions[actionIndex] = replaceVariablesInAction(action, vars)
			}
			component.Tasks[taskIndex] = task
		}
		cfg.Components[componentName] = component
	}
}

func replaceVariablesInAction(action ActionSpec, vars map[string]string) ActionSpec {
	action.Type = replaceVariables(action.Type, vars)
	action.Name = replaceVariables(action.Name, vars)
	action.Description = replaceVariables(action.Description, vars)
	action.Source = replaceVariables(action.Source, vars)
	action.Target = replaceVariables(action.Target, vars)
	action.Mode = replaceVariables(action.Mode, vars)
	action.Content = replaceVariables(action.Content, vars)
	action.Command = replaceVariables(action.Command, vars)
	action.WorkDir = replaceVariables(action.WorkDir, vars)
	action.Creates = replaceVariables(action.Creates, vars)
	action.Service = replaceVariables(action.Service, vars)
	action.Package = replaceVariables(action.Package, vars)
	action.User = replaceVariables(action.User, vars)
	action.Group = replaceVariables(action.Group, vars)
	action.Owner = replaceVariables(action.Owner, vars)
	action.Home = replaceVariables(action.Home, vars)
	action.Shell = replaceVariables(action.Shell, vars)
	action.URL = replaceVariables(action.URL, vars)
	action.Address = replaceVariables(action.Address, vars)
	action.Manifest = replaceVariables(action.Manifest, vars)
	action.File = replaceVariables(action.File, vars)
	action.Kubeconfig = replaceVariables(action.Kubeconfig, vars)
	action.Namespace = replaceVariables(action.Namespace, vars)
	action.Marker = replaceVariables(action.Marker, vars)
	action.Line = replaceVariables(action.Line, vars)
	action.Key = replaceVariables(action.Key, vars)
	action.Value = replaceVariables(action.Value, vars)
	action.ConfigFile = replaceVariables(action.ConfigFile, vars)
	action.OnlyHostAddress = replaceVariables(action.OnlyHostAddress, vars)
	action.OnlyWhen = replaceVariables(action.OnlyWhen, vars)
	action.EtcdAddresses = replaceVariables(action.EtcdAddresses, vars)
	action.Args = replaceVariablesInSlice(action.Args, vars)
	action.Packages = replaceVariablesInSlice(action.Packages, vars)
	if action.TemplateVars != nil {
		next := map[string]string{}
		for key, value := range action.TemplateVars {
			next[replaceVariables(key, vars)] = replaceVariables(value, vars)
		}
		action.TemplateVars = next
	}
	return action
}

// ReplaceVariablesInAction is the exported version of replaceVariablesInAction.
func ReplaceVariablesInAction(action ActionSpec, vars map[string]string) ActionSpec {
	return replaceVariablesInAction(action, vars)
}

func replaceVariablesInSlice(values []string, vars map[string]string) []string {
	if len(values) == 0 {
		return values
	}
	next := make([]string, len(values))
	for i, value := range values {
		next[i] = replaceVariables(value, vars)
	}
	return next
}

func replaceVariables(value string, vars map[string]string) string {
	for key, replacement := range vars {
		value = strings.ReplaceAll(value, "${"+key+"}", replacement)
	}
	return value
}

func flattenInto(out map[string]string, prefix string, values map[string]any) {
	for key, value := range values {
		fullKey := prefix + "." + key
		if nested, ok := value.(map[string]any); ok {
			flattenInto(out, fullKey, nested)
			continue
		}
		out[fullKey] = fmt.Sprint(value)
	}
}

func envNetworkValue(vars map[string]string, key string) any {
	return vars[key]
}

func firstServiceIP(cidr string) string {
	return nthServiceIP(cidr, 1)
}

func nthServiceIP(cidr string, offset int) string {
	base := strings.Split(cidr, "/")[0]
	parts := strings.Split(base, ".")
	if len(parts) != 4 {
		return ""
	}
	last, err := strconv.Atoi(parts[3])
	if err != nil {
		return ""
	}
	parts[3] = strconv.Itoa(last + offset)
	return strings.Join(parts, ".")
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func quotedList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, `"`+value+`"`)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func yamlQuotedLines(values []string) string {
	if len(values) == 0 {
		return "  []"
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		lines = append(lines, "\n  - \""+value+"\"")
	}
	return strings.Join(lines, "\n") + "\n"
}

func jsonHostLines(values []string) string {
	lines := make([]string, 0, len(values))
	for index, value := range values {
		suffix := ","
		if index == len(values)-1 {
			suffix = ""
		}
		lines = append(lines, `    "`+value+`"`+suffix)
	}
	return strings.Join(lines, "\n")
}

func nacosClusterLines(addresses []string) string {
	if len(addresses) == 0 {
		return ""
	}
	lines := make([]string, 0, len(addresses))
	for _, address := range addresses {
		lines = append(lines, address+":8848")
	}
	return strings.Join(lines, "\n")
}

func haproxyServerLines(addresses []string, port string, options string) string {
	if len(addresses) == 0 {
		return "        # no backend servers configured"
	}
	var lines []string
	for _, address := range addresses {
		lines = append(lines, "\n        server "+address+" "+address+":"+port+" "+options)
	}
	return strings.Join(lines, "\n") + "\n"
}

// haproxyClusterServerLines is the variant for components that only
// need haproxy fronting when they're deployed as a cluster (≥ 2 hosts).
// When the component runs single-node — the typical case for redis,
// postgresql, minio in resource-constrained installations — clients
// connect directly to the host instead of going through the SLB, so
// emitting backend `server` lines would point haproxy at nothing
// useful. Returning a sentinel comment keeps the listen block valid
// haproxy syntax while signalling "no LB needed here".
func haproxyClusterServerLines(addresses []string, port string, options string) string {
	if len(addresses) < 2 {
		return "        # single-node deployment, clients connect to the component directly"
	}
	return haproxyServerLines(addresses, port, options)
}

func kubeLBServerLines(addresses []string) string {
	if len(addresses) == 0 {
		return "        # no kube-apiserver backend configured"
	}
	var lines []string
	for _, address := range addresses {
		lines = append(lines, "        server "+address+":6443    max_fails=2 fail_timeout=3s;")
	}
	return strings.Join(lines, "\n")
}

func etcdEndpoints(addresses []string) string {
	if len(addresses) == 0 {
		return ""
	}
	var endpoints []string
	for _, address := range addresses {
		endpoints = append(endpoints, "https://"+address+":2379")
	}
	return strings.Join(endpoints, ",")
}

func etcdInitialCluster(addresses []string) string {
	var nodes []string
	for _, address := range addresses {
		nodes = append(nodes, "etcd-"+address+"=https://"+address+":2380")
	}
	return strings.Join(nodes, ",")
}

func postgresqlEtcdInitialCluster(addresses []string) string {
	var nodes []string
	for _, address := range addresses {
		nodes = append(nodes, "etcd-"+address+"=https://"+address+":2380")
	}
	return strings.Join(nodes, ",")
}

func postgresqlPatroniEtcdHosts(addresses []string) string {
	var hosts []string
	for _, address := range addresses {
		hosts = append(hosts, address+":2379")
	}
	return strings.Join(hosts, ",")
}

func postgresqlBackupAddress(addresses []string) string {
	if len(addresses) >= 3 {
		return addresses[2]
	}
	if len(addresses) > 0 {
		return addresses[0]
	}
	return ""
}

func minioClusterVolumes(addresses []string, dataDir string) string {
	if dataDir == "" {
		dataDir = "/opt/minio/data"
	}
	if len(addresses) <= 1 {
		return dataDir
	}
	volumes := make([]string, 0, len(addresses))
	for _, address := range addresses {
		volumes = append(volumes, "http://"+address+dataDir)
	}
	return " " + strings.Join(volumes, " ") + "  "
}
