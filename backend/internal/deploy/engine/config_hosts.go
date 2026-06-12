package engine

import (
	"fmt"
	"sort"
	"strconv"
)

// SLBConfig holds VIP extracted from the hosts.slb section.
type SLBConfig struct {
	VIP string
}

// CompileHosts converts either the web-friendly flat host list or the legacy
// grouped environment hosts section into deployment hosts.
func CompileHosts(raw any) ([]Host, error) {
	return compileHosts(raw)
}

func compileHosts(raw any) ([]Host, error) {
	if raw == nil {
		return nil, nil
	}
	if flat, ok := raw.([]any); ok {
		return compileFlatHosts(flat)
	}
	groups, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("配置错误: hosts 必须是主机列表或主机分组")
	}
	if len(groups) == 0 {
		return nil, nil
	}
	acc := hostAccumulator{byAddress: map[string]int{}}
	keys := sortedAnyMapKeys(groups)
	for _, group := range keys {
		value := groups[group]
		if group == "middleware" {
			rolesByName, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("配置错误: hosts.middleware 必须是组件到IP的映射")
			}
			for _, role := range sortedAnyMapKeys(rolesByName) {
				if err := acc.addEntries(role, []string{role}, rolesByName[role]); err != nil {
					return nil, err
				}
			}
			continue
		}
		if group == "slb" {
			if err := acc.addSLBEntries(value); err != nil {
				return nil, err
			}
			continue
		}
		if err := acc.addEntries(group, defaultRolesForHostGroup(group), value); err != nil {
			return nil, err
		}
	}
	return acc.hosts, nil
}

func compileFlatHosts(raw []any) ([]Host, error) {
	hosts := make([]Host, 0, len(raw))
	for index, item := range raw {
		node, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("配置错误: hosts[%d] 必须是主机对象", index)
		}
		address := stringValue(node["address"])
		if address == "" {
			address = stringValue(node["ip"])
		}
		if address == "" {
			return nil, fmt.Errorf("配置错误: hosts[%d] 缺少 address", index)
		}
		roles := stringSliceValue(node["roles"])
		if len(roles) == 0 {
			return nil, fmt.Errorf("配置错误: hosts[%d] 缺少 roles", index)
		}
		name := stringValue(node["name"])
		if name == "" {
			name = "host-" + strconv.Itoa(index+1)
		}
		hosts = append(hosts, Host{Name: name, Address: address, Roles: mergeRoles(nil, roles)})
	}
	return hosts, nil
}

// extractSLBConfig extracts VIP from the hosts.slb section.
// It supports both the map format (with vip and nodes keys) and the old list format.
func extractSLBConfig(raw any) SLBConfig {
	groups, ok := raw.(map[string]any)
	if !ok {
		return SLBConfig{}
	}
	slbRaw, ok := groups["slb"]
	if !ok {
		return SLBConfig{}
	}
	switch value := slbRaw.(type) {
	case map[string]any:
		return SLBConfig{VIP: stringValue(value["vip"])}
	default:
		return SLBConfig{}
	}
}

type hostAccumulator struct {
	hosts     []Host
	byAddress map[string]int
}

// addSLBEntries handles both the map format (vip + nodes) and old list format for hosts.slb.
func (a *hostAccumulator) addSLBEntries(raw any) error {
	switch value := raw.(type) {
	case map[string]any:
		// Map format: {vip: "...", nodes: [{ip, name}, ...]}
		nodesRaw, ok := value["nodes"]
		if !ok {
			return fmt.Errorf("配置错误: hosts.slb 缺少 nodes 字段")
		}
		return a.addEntries("slb", defaultRolesForHostGroup("slb"), nodesRaw)
	default:
		// Old list format: treat as regular host entries
		return a.addEntries("slb", defaultRolesForHostGroup("slb"), raw)
	}
}

func (a *hostAccumulator) addEntries(prefix string, roles []string, raw any) error {
	entries, err := normalizeHostEntries(raw)
	if err != nil {
		return fmt.Errorf("配置错误: hosts.%s: %w", prefix, err)
	}
	for i, entry := range entries {
		name := entry.Name
		if name == "" {
			name = prefix + "-" + strconv.Itoa(i+1)
		}
		a.add(name, entry.Address, roles)
	}
	return nil
}

func (a *hostAccumulator) add(name string, address string, roles []string) {
	if index, ok := a.byAddress[address]; ok {
		host := a.hosts[index]
		host.Roles = mergeRoles(host.Roles, roles)
		if host.Name == "" {
			host.Name = name
		}
		a.hosts[index] = host
		return
	}
	host := Host{Name: name, Address: address, Roles: mergeRoles(nil, roles)}
	a.byAddress[address] = len(a.hosts)
	a.hosts = append(a.hosts, host)
}

type hostEntry struct {
	Name    string
	Address string
}

func normalizeHostEntries(raw any) ([]hostEntry, error) {
	switch value := raw.(type) {
	case string:
		if value == "" {
			return nil, fmt.Errorf("IP不能为空")
		}
		return []hostEntry{{Address: value}}, nil
	case []any:
		var entries []hostEntry
		for _, item := range value {
			itemEntries, err := normalizeHostEntries(item)
			if err != nil {
				return nil, err
			}
			entries = append(entries, itemEntries...)
		}
		return entries, nil
	case map[string]any:
		address := stringValue(value["ip"])
		if address == "" {
			address = stringValue(value["address"])
		}
		if address == "" {
			return nil, fmt.Errorf("缺少 ip")
		}
		return []hostEntry{{Name: stringValue(value["name"]), Address: address}}, nil
	default:
		return nil, fmt.Errorf("不支持的主机写法 %T", raw)
	}
}

func defaultRolesForHostGroup(group string) []string {
	switch group {
	case "deploy":
		return []string{"controller-render", "df-ops", "dns", "docker", "ftp", "nfs", "skywalking"}
	case "master":
		return []string{"calico", "containerd", "etcd", "kube-lb", "master", "nacos", "prepare"}
	case "node":
		return []string{"calico", "containerd", "kube-lb", "node", "plugin", "prepare"}
	case "slb":
		return []string{"slb"}
	default:
		return []string{group}
	}
}

func mergeRoles(existing []string, additional []string) []string {
	seen := map[string]bool{}
	for _, role := range existing {
		if role != "" {
			seen[role] = true
		}
	}
	for _, role := range additional {
		if role != "" {
			seen[role] = true
		}
	}
	roles := make([]string, 0, len(seen))
	for role := range seen {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func hasRole(host Host, role string) bool {
	for _, item := range host.Roles {
		if item == role {
			return true
		}
	}
	return false
}
