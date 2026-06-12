package handler

import (
	"df-build-server/internal/deploy/defaults"
	"df-build-server/internal/deploy/engine"
	"df-build-server/internal/deploy/engine/virtualcomponents"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

// registerComponentExtras mounts the component detail/defaults/tasks endpoints
// (parity with his-deploy's /components/defaults and /components/{name}/tasks).
func (h *Handler) registerComponentExtras(g *gin.RouterGroup) {
	g.GET("/components/defaults", h.ComponentDefaults)
	g.GET("/components/:name/tasks", h.ComponentTasks)
}

// ComponentDefaults returns the factory default parameters per component so the
// UI can offer a "restore defaults" action.
func (h *Handler) ComponentDefaults(c *gin.Context) {
	all, err := defaults.All()
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	response.OK(c, all)
}

type taskActionDTO struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type taskRowDTO struct {
	Component string          `json:"component"`
	Phase     string          `json:"phase"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Actions   []taskActionDTO `json:"actions"`
}

// ComponentTasks returns the planned pipeline tasks (and their actions) for a
// component or virtual component, loaded from the embedded pipeline definition.
func (h *Handler) ComponentTasks(c *gin.Context) {
	name := c.Param("name")
	cfg, err := engine.LoadDefaultPipelineConfig()
	if err != nil {
		response.Fail(c, 5001, "加载 pipeline 失败: "+err.Error())
		return
	}

	var pipelineNames []string
	if vc, ok := virtualcomponents.Find(name); ok {
		for _, m := range vc.Mappings {
			pipelineNames = append(pipelineNames, m.Name)
		}
	} else if _, ok := cfg.Components[name]; ok {
		pipelineNames = []string{name}
	} else {
		response.Fail(c, 4040, "组件不存在: "+name)
		return
	}

	rows := []taskRowDTO{}
	for _, pname := range pipelineNames {
		comp, ok := cfg.Components[pname]
		if !ok {
			continue
		}
		for _, t := range comp.Tasks {
			actions := make([]taskActionDTO, 0, len(t.Actions))
			for _, a := range t.Actions {
				actions = append(actions, taskActionDTO{Type: a.Type, Name: a.Name})
			}
			rows = append(rows, taskRowDTO{
				Component: pname,
				Phase:     t.Phase,
				ID:        t.ID,
				Name:      t.Name,
				Actions:   actions,
			})
		}
	}
	response.OK(c, gin.H{"name": name, "pipelineComponents": pipelineNames, "items": rows})
}
