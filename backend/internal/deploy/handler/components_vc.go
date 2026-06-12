package handler

import (
	"context"
	"net"

	"df-build-server/internal/deploy/conflict"
	"df-build-server/internal/deploy/engine/store"
	"df-build-server/internal/deploy/engine/virtualcomponents"
)

// vcSummary is the virtual-component card the components page renders.
type vcSummary struct {
	Name                 string  `json:"name"`
	DisplayName          string  `json:"displayName"`
	Description          string  `json:"description"`
	Category             string  `json:"category"`
	Order                int     `json:"order"`
	Enabled              bool    `json:"enabled"`
	RequireHostSelection bool    `json:"requireHostSelection"`
	AutoBindNote         string  `json:"autoBindNote"`
	HostIDs              []int64 `json:"hostIds"`
	PipelineComponents   []string `json:"pipelineComponents"`
	DeployState          string  `json:"deployState"`
	MinUserHosts         int     `json:"minUserHosts"`
	MaxUserHosts         int     `json:"maxUserHosts"`
}

// localIPv4Set returns the non-loopback IPv4 addresses of this machine, used to
// resolve which Server record is the deploy host (for BindDeployHost mappings).
func localIPv4Set() map[string]bool {
	out := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			out[v4.String()] = true
		}
	}
	return out
}

// deployContext resolves the inventory (all server IDs) and the deploy host ID
// (the Server whose address is bound to this machine), used by Expand.
func (h *Handler) deployContext() (allHostIDs []int64, deployHostID int64) {
	hosts, err := h.svc.Store().ListHosts(context.Background())
	if err != nil {
		return nil, 0
	}
	local := localIPv4Set()
	for _, hsp := range hosts {
		allHostIDs = append(allHostIDs, hsp.ID)
		if local[hsp.Address] && deployHostID == 0 {
			deployHostID = hsp.ID
		}
	}
	return allHostIDs, deployHostID
}

func autoBindNote(vc virtualcomponents.Component) (requireHosts bool, note string) {
	hasUser, hasAll, hasDeploy := false, false, false
	for _, m := range vc.Mappings {
		switch m.HostBind {
		case virtualcomponents.BindUserSelected:
			hasUser = true
		case virtualcomponents.BindAllHosts:
			hasAll = true
		case virtualcomponents.BindDeployHost:
			hasDeploy = true
		}
	}
	if hasUser {
		return true, ""
	}
	if hasAll {
		return false, "系统自动管理：所有主机"
	}
	if hasDeploy {
		return false, "系统自动管理：部署机执行"
	}
	return false, "系统自动管理"
}

func vcCategory(vc virtualcomponents.Component) string {
	for _, m := range vc.Mappings {
		if conflict.IsK8s(m.Name) {
			return "k8s"
		}
	}
	for _, m := range vc.Mappings {
		if conflict.IsBusiness(m.Name) {
			return "business"
		}
	}
	return "other"
}

// buildVCSummary assembles one card from the virtual component + store state.
func (h *Handler) buildVCSummary(
	vc virtualcomponents.Component,
	enabledByPipeline map[string]bool,
	ownTargets map[string][]int64,
	deployState string,
) vcSummary {
	hostIDs := vc.AggregateUserHosts(ownTargets)
	if hostIDs == nil {
		hostIDs = []int64{}
	}
	enabled := len(vc.Mappings) > 0
	pipelineNames := make([]string, 0, len(vc.Mappings))
	for _, m := range vc.Mappings {
		pipelineNames = append(pipelineNames, m.Name)
		if !enabledByPipeline[m.Name] {
			enabled = false
		}
	}
	requireHosts, note := autoBindNote(vc)
	if deployState == "" {
		deployState = store.DeployStateNotDeployed
	}
	return vcSummary{
		Name:                 vc.Name,
		DisplayName:          vc.DisplayName,
		Description:          vc.Description,
		Category:             vcCategory(vc),
		Order:                vc.Order,
		Enabled:              enabled,
		RequireHostSelection: requireHosts,
		AutoBindNote:         note,
		HostIDs:              hostIDs,
		PipelineComponents:   pipelineNames,
		DeployState:          deployState,
		MinUserHosts:         vc.MinUserHosts,
		MaxUserHosts:         vc.MaxUserHosts,
	}
}
