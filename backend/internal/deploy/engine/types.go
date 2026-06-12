package engine

import "time"

type Config struct {
	Cluster    ClusterConfig        `yaml:"cluster"`
	Hosts      []Host               `yaml:"hosts"`
	Components map[string]Component `yaml:"components"`
}

type ClusterConfig struct {
	Name        string `yaml:"name"`
	ResourceDir string `yaml:"resource_dir"`
	RemoteRoot  string `yaml:"remote_root"`
	StateDir    string `yaml:"state_dir"`
}

type Host struct {
	Name    string   `yaml:"name" json:"name"`
	Address string   `yaml:"address" json:"address"`
	Roles   []string `yaml:"roles" json:"roles"`
}

type Component struct {
	DisplayName string   `yaml:"display_name"`
	Enabled     bool     `yaml:"enabled"`
	Order       int      `yaml:"order"`
	DependsOn   []string `yaml:"depends_on"`
	TargetRoles []string `yaml:"target_roles"`
	Tasks       []Task   `yaml:"tasks"`
}

type Task struct {
	ID      string       `yaml:"id"`
	Name    string       `yaml:"name"`
	Phase   string       `yaml:"phase"`
	Actions []ActionSpec `yaml:"actions"`
}

type ActionSpec struct {
	Type            string            `yaml:"type"`
	Name            string            `yaml:"name"`
	Description     string            `yaml:"description"`
	Source          string            `yaml:"source"`
	Target          string            `yaml:"target"`
	Mode            string            `yaml:"mode"`
	Content         string            `yaml:"content"`
	TemplateVars    map[string]string `yaml:"template_vars"`
	Command         string            `yaml:"command"`
	Args            []string          `yaml:"args"`
	WorkDir         string            `yaml:"work_dir"`
	Creates         string            `yaml:"creates"`
	Service         string            `yaml:"service"`
	Package         string            `yaml:"package"`
	Packages        []string          `yaml:"packages"`
	User            string            `yaml:"user"`
	Group           string            `yaml:"group"`
	Owner           string            `yaml:"owner"`
	Home            string            `yaml:"home"`
	Shell           string            `yaml:"shell"`
	CreateHome      *bool             `yaml:"create_home"`
	State           string            `yaml:"state"`
	Enabled         *bool             `yaml:"enabled"`
	URL             string            `yaml:"url"`
	Address         string            `yaml:"address"`
	ExpectedStatus  int               `yaml:"expected_status"`
	Timeout         string            `yaml:"timeout"`
	Attempts        int               `yaml:"attempts"`
	Interval        string            `yaml:"interval"`
	Manifest        string            `yaml:"manifest"`
	File            string            `yaml:"file"`
	Kubeconfig      string            `yaml:"kubeconfig"`
	Namespace       string            `yaml:"namespace"`
	IgnoreNotFound  bool              `yaml:"ignore_not_found"`
	Marker          string            `yaml:"marker"`
	Line            string            `yaml:"line"`
	Key             string            `yaml:"key"`
	Value           string            `yaml:"value"`
	ConfigFile      string            `yaml:"config_file"`
	OnlyHostAddress string            `yaml:"only_host_address"`
	// OnlyWhen is a left=right equality test evaluated after variable
	// expansion. Empty means always run; "single=single" runs;
	// "ha=single" skips. The shape mirrors ansible's `when:` and is
	// the dfctl primitive for "this action only applies in mode X"
	// without a heavyweight expression engine. Use cases include
	// postgresql HA vs single-mode branching where some copies and
	// inits only fire in single-host topologies.
	OnlyWhen        string            `yaml:"only_when"`
	IgnoreError     bool              `yaml:"ignore_error"`
	EtcdAddresses   string            `yaml:"etcd_addresses"`
	// Scope pins where the action executes regardless of which host
	// the surrounding task targets:
	//   ""        — node side (default; routed via the per-host backend)
	//   "control" — control node (uses controlFS / controlCmd, never
	//               touches the target host). Used by preflight checks
	//               that probe offline resources on the deploy node and
	//               by render actions that produce intermediates the
	//               control side then pushes to nodes.
	Scope    string       `yaml:"scope"`
	Rollback RollbackSpec `yaml:"rollback"`
}

type RollbackSpec struct {
	Type string `yaml:"type"`
}

type TaskContext struct {
	Component string
	HostName  string
	HostAddr  string
	TaskID    string
	TaskName  string
	// Phase carries the pipeline phase this task belongs to
	// (preflight | render | deploy | test | rollback | residue).
	// Plumbed through to the log layer so the UI can group / filter
	// events by phase without inferring from action names.
	Phase string
}

type ActionResult struct {
	Context  TaskContext
	Action   string
	Target   string
	Status   string
	Duration time.Duration
	// Detail carries the human-readable failure cause when Status is
	// "失败"/"failed". Filled by Runner.failedResult by extracting
	// Reason+Detail+Suggestion from a *DeployError. The hub logger
	// prefers Detail over Target when both are non-empty so the UI's
	// SSE stream surfaces the root cause (exit code, stderr, etc.)
	// instead of just the path that failed.
	Detail string
}
