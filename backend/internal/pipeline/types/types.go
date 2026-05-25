package types

import (
	"context"
	"fmt"

	"df-build-server/internal/model"
)

// StageResult holds the outcome of a stage execution
type StageResult struct {
	ExitCode int
	Output   string
	Error    string
}

// StageRunner is the interface all stage implementations must satisfy
type StageRunner interface {
	Name() string
	Code() string
	Run(ctx context.Context, pCtx *PipelineContext) (*StageResult, error)
}

// PipelineContext carries shared state across stages
type PipelineContext struct {
	PipelineID     uint
	Pipeline       *model.Pipeline
	Workspace      *Workspace
	GitBranch      string
	GitRepo        string
	BuildConfig    *model.BuildConfig
	AppType        string
	AppName        string
	ArtifactName   string
	BuildCommand   string
	InstallCommand string
	ArtifactMD5    string

	// Vue micro-frontend fields
	VueRole string
	AppCode string

	// Java gateway flag
	IsGateway bool

	// Deploy-related fields
	DeployMode       string
	K8sNamespace     string
	ImageName        string
	NodePort         int
	IngressHost      string                // Deprecated: backward-compatible single host
	Ingresses        []model.IngressConfig // Parsed list of Ingress configs
	ConfigMapContent string

	// Callbacks
	OnLog func(pipelineID, stageID uint, line string, stream string)
}

// Workspace manages build directories
type Workspace struct {
	BaseDir    string
	PipelineID uint
	AppName    string
}

// SourceDir returns the source code directory path
func (w *Workspace) SourceDir() string {
	return fmt.Sprintf("%s/%s/pipeline-%d/source", w.BaseDir, w.AppName, w.PipelineID)
}

// PipelineDir returns the pipeline working directory
func (w *Workspace) PipelineDir() string {
	return fmt.Sprintf("%s/%s/pipeline-%d", w.BaseDir, w.AppName, w.PipelineID)
}
