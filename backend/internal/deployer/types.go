package deployer

import (
	"context"

	"df-build-server/internal/model"
)

// ParamDef defines a parameter that a component needs
type ParamDef struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
	Type        string `json:"type"` // text / password / number / select
	Options     []string `json:"options,omitempty"`
	Description string `json:"description"`
}

// DeployOpts contains all info needed to deploy a component
type DeployOpts struct {
	Hosts      []model.Server        // Target servers
	Params     map[string]string     // User-provided parameters
	PackageDir string                // Path to offline packages
	OnLog      func(line string)     // Log callback
}

// Component is the interface all deployable components must implement
type Component interface {
	Code() string
	Name() string
	Order() int
	Category() string                                    // infra / kubernetes / middleware
	Params() []ParamDef                                  // Parameters the user needs to fill
	RequiredFiles() []string                             // Files needed in the package dir
	Deploy(ctx context.Context, opts *DeployOpts) error
	Verify(ctx context.Context, opts *DeployOpts) error
	Cleanup(ctx context.Context, opts *DeployOpts) error
	OutputFields(opts *DeployOpts) map[string]string     // Fields for environment report
}

// StepStatus represents the status of a single deploy step
type StepStatus struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // pending / running / success / failed / skipped
	Duration int    `json:"duration"`
	Error    string `json:"error,omitempty"`
}
