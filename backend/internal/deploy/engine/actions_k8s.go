package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"df-build-server/internal/deploy/engine/exec"
)

const kubernetesCAConfigJSON = `{
  "signing": {
    "default": {
      "expiry": "438000h"
    },
    "profiles": {
      "kubernetes": {
        "usages": [
          "signing",
          "key encipherment",
          "server auth",
          "client auth"
        ],
        "expiry": "438000h"
      },
      "kcfg": {
        "usages": [
          "signing",
          "key encipherment",
          "client auth"
        ],
        "expiry": "438000h"
      }
    }
  }
}
`

const kubernetesCACSRJSON = `{
  "CN": "kubernetes",
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "k8s",
      "OU": "System"
    }
  ],
  "ca": {
    "expiry": "876000h"
  }
}
`

const kubernetesAdminCSRJSON = `{
  "CN": "admin",
  "hosts": [],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "system:masters",
      "OU": "System"
    }
  ]
}
`

const kubernetesKubeProxyCSRJSON = `{
  "CN": "system:kube-proxy",
  "hosts": [],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "k8s",
      "OU": "System"
    }
  ]
}
`

const kubernetesControllerManagerCSRJSON = `{
  "CN": "system:kube-controller-manager",
  "hosts": [],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "system:kube-controller-manager",
      "OU": "System"
    }
  ]
}
`

const kubernetesSchedulerCSRJSON = `{
  "CN": "system:kube-scheduler",
  "hosts": [],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "system:kube-scheduler",
      "OU": "System"
    }
  ]
}
`

const kubernetesAggregatorProxyCSRJSON = `{
  "CN": "aggregator",
  "hosts": [],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "k8s",
      "OU": "System"
    }
  ]
}
`

const kubernetesCalicoCSRJSON = `{
  "CN": "calico",
  "hosts": [],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "k8s",
      "OU": "System"
    }
  ]
}
`

func (e *ActionExecutor) kubernetesArtifacts(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "kubernetes_artifacts.target")
	}
	if spec.URL == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "Kubernetes API 地址",
			Reason:     "API 地址为空",
			Detail:     "kubernetes_artifacts.url 不能为空",
			Suggestion: "确认 config.yml 至少配置一个 master 主机，dfctl 会自动生成 kubernetes.api_server。",
		}
	}
	if e.resourceDir == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "离线资源目录",
			Reason:     "resource_dir 为空",
			Detail:     "controller-render 需要 resource_dir 来定位 cfssl、cfssljson 和 kubectl",
			Suggestion: "检查 config.yml 的 env.resource_dir。",
		}
	}
	baseDir := spec.Target
	sslDir := filepath.Join(baseDir, "ssl")
	// 0o700 not 0o755: ssl/ holds private keys (ca-key.pem,
	// admin-key.pem, kubelet-key.pem, etcd-key.pem, ...). Even with
	// per-file 0o600 from cfssljson, a 0o755 parent dir lets local
	// non-root users list filenames and infer cluster identity.
	if err := os.MkdirAll(sslDir, 0o700); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "证书目录 " + sslDir,
			Reason:     "创建证书目录失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘空间。",
		}
	}
	files := map[string]string{
		"ca-config.json":                   kubernetesCAConfigJSON,
		"ca-csr.json":                      kubernetesCACSRJSON,
		"admin-csr.json":                   kubernetesAdminCSRJSON,
		"kube-proxy-csr.json":              kubernetesKubeProxyCSRJSON,
		"kube-controller-manager-csr.json": kubernetesControllerManagerCSRJSON,
		"kube-scheduler-csr.json":          kubernetesSchedulerCSRJSON,
		"aggregator-proxy-csr.json":        kubernetesAggregatorProxyCSRJSON,
		"calico-csr.json":                  kubernetesCalicoCSRJSON,
		"kubernetes-csr.json":              kubernetesServingCSRJSON(spec.Address, spec.Line, spec.Value),
		// etcd certificate SAN uses dedicated etcd_addresses parameter when provided,
		// falls back to spec.Line (master addresses) for backward compatibility with
		// colocated etcd-on-master topologies.
		"etcd-csr.json": kubernetesEtcdCSRJSON(etcdAddressesOrFallback(spec.EtcdAddresses, spec.Line)),
	}
	kubeletNodes := strings.Fields(spec.Key)
	for _, node := range kubeletNodes {
		files[node+"-kubelet-csr.json"] = kubernetesKubeletCSRJSON(node)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sslDir, name), []byte(content), 0o644); err != nil {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "证书配置文件 " + name,
				Reason:     "写入证书配置失败",
				Detail:     err.Error(),
				Suggestion: "检查目标目录权限和磁盘空间。",
			}
		}
	}
	cfssl := filepath.Join(e.resourceDir, "controller-render", "cfssl")
	cfssljson := filepath.Join(e.resourceDir, "controller-render", "cfssljson")
	kubectl := filepath.Join(e.resourceDir, "controller-render", "kubectl")
	if err := e.runCommand(ctx, ActionSpec{Type: "run_command", Name: "生成CA证书", Command: "bash", Args: []string{"-lc", "cd " + shellQuote(sslDir) + " && " + shellQuote(cfssl) + " gencert -initca ca-csr.json | " + shellQuote(cfssljson) + " -bare ca"}}, actionName); err != nil {
		return err
	}
	certs := []struct {
		CSR    string
		Output string
	}{
		{CSR: "admin-csr.json", Output: "admin"},
		{CSR: "kube-proxy-csr.json", Output: "kube-proxy"},
		{CSR: "kube-controller-manager-csr.json", Output: "kube-controller-manager"},
		{CSR: "kube-scheduler-csr.json", Output: "kube-scheduler"},
		{CSR: "kubernetes-csr.json", Output: "kubernetes"},
		{CSR: "aggregator-proxy-csr.json", Output: "aggregator-proxy"},
		{CSR: "etcd-csr.json", Output: "etcd"},
		{CSR: "calico-csr.json", Output: "calico"},
	}
	for _, node := range kubeletNodes {
		certs = append(certs, struct {
			CSR    string
			Output string
		}{CSR: node + "-kubelet-csr.json", Output: node + "-kubelet"})
	}
	for _, cert := range certs {
		command := "cd " + shellQuote(sslDir) + " && " + shellQuote(cfssl) + " gencert -ca=ca.pem -ca-key=ca-key.pem -config=ca-config.json -profile=kubernetes " + shellQuote(cert.CSR) + " | " + shellQuote(cfssljson) + " -bare " + shellQuote(cert.Output)
		if err := e.runCommand(ctx, ActionSpec{Type: "run_command", Name: "生成" + cert.Output + "证书", Command: "bash", Args: []string{"-lc", command}}, actionName); err != nil {
			return err
		}
	}
	configs := []kubeconfigSpec{
		{
			Path:        filepath.Join(baseDir, "kubectl.kubeconfig"),
			Cluster:     "dfhis",
			User:        "admin",
			Context:     "context-dfhis",
			Certificate: filepath.Join(sslDir, "admin.pem"),
			Key:         filepath.Join(sslDir, "admin-key.pem"),
		},
		{
			Path:        filepath.Join(baseDir, "kube-proxy.kubeconfig"),
			Cluster:     "kubernetes",
			User:        "kube-proxy",
			Context:     "default",
			Certificate: filepath.Join(sslDir, "kube-proxy.pem"),
			Key:         filepath.Join(sslDir, "kube-proxy-key.pem"),
		},
		{
			Path:        filepath.Join(baseDir, "kube-controller-manager.kubeconfig"),
			Cluster:     "kubernetes",
			User:        "system:kube-controller-manager",
			Context:     "default",
			Certificate: filepath.Join(sslDir, "kube-controller-manager.pem"),
			Key:         filepath.Join(sslDir, "kube-controller-manager-key.pem"),
		},
		{
			Path:        filepath.Join(baseDir, "kube-scheduler.kubeconfig"),
			Cluster:     "kubernetes",
			User:        "system:kube-scheduler",
			Context:     "default",
			Certificate: filepath.Join(sslDir, "kube-scheduler.pem"),
			Key:         filepath.Join(sslDir, "kube-scheduler-key.pem"),
		},
	}
	for _, node := range kubeletNodes {
		// Per-host kubelet kubeconfigs land under <baseDir>/<ip>/ to
		// keep cloudhis layout consistent with other host-specific
		// artefacts (etcd.service, kubelet.service, kube-proxy.service,
		// patroni.yml, etc. — all in <comp>/<ip>/).
		nodeDir := filepath.Join(baseDir, node)
		if err := os.MkdirAll(nodeDir, 0o755); err != nil {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "节点目录 " + nodeDir,
				Reason:     "创建节点目录失败",
				Detail:     err.Error(),
				Suggestion: "检查目标目录权限和磁盘空间。",
			}
		}
		configs = append(configs, kubeconfigSpec{
			Path:        filepath.Join(nodeDir, "kubelet.kubeconfig"),
			Cluster:     "kubernetes",
			User:        "system:node:" + node,
			Context:     "default",
			Certificate: filepath.Join(sslDir, node+"-kubelet.pem"),
			Key:         filepath.Join(sslDir, node+"-kubelet-key.pem"),
		})
	}
	for _, config := range configs {
		if err := e.createKubeconfig(ctx, actionName, kubectl, spec.URL, sslDir, config); err != nil {
			return err
		}
	}
	// Snapshot the K8s host set (master + node) we just generated certs
	// + kubeconfigs for. Downstream components (master/node/etcd/
	// containerd/kube-lb/calico) read this in preflight via
	// kubernetes_artifacts_check to fail early when the operator
	// changed host bindings without re-running controller-render —
	// otherwise they'd hit "<host-ip>-kubelet.kubeconfig not found"
	// halfway through the deploy phase with no actionable hint.
	if err := e.writeKubernetesArtifactsSnapshot(ctx, actionName, baseDir, spec.Line, spec.Key); err != nil {
		return err
	}
	return nil
}

// kubernetesArtifactsSnapshot is the on-disk view of "for which K8s
// hosts did the most recent controller-render run produce certs +
// kubeconfigs". Stored next to the kubeconfigs themselves in
// cloudhis/kubernetes/.snapshot.json.
type kubernetesArtifactsSnapshot struct {
	MasterAddresses []string `json:"master_addresses"`
	NodeAddresses   []string `json:"node_addresses"`
	AllAddresses    []string `json:"all_addresses"`
}

func (e *ActionExecutor) writeKubernetesArtifactsSnapshot(ctx TaskContext, actionName, baseDir, masterAddrs, kubeletNodes string) error {
	snap := kubernetesArtifactsSnapshot{
		MasterAddresses: strings.Fields(masterAddrs),
		NodeAddresses: subtractStringSlice(
			strings.Fields(kubeletNodes),
			strings.Fields(masterAddrs),
		),
		AllAddresses: strings.Fields(kubeletNodes),
	}
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "K8s 节点快照",
			Reason:     "序列化快照失败",
			Detail:     err.Error(),
			Suggestion: "联系 dfctl 维护方,这是引擎层的 bug。",
		}
	}
	target := filepath.Join(baseDir, kubernetesArtifactsSnapshotFile)
	if err := os.WriteFile(target, body, 0o644); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "快照文件 " + target,
			Reason:     "写入快照失败",
			Detail:     err.Error(),
			Suggestion: "检查目标目录权限和磁盘空间。",
		}
	}
	return nil
}

// kubernetesArtifactsSnapshotFile is the bare filename (relative to
// the cloudhis/kubernetes directory) where controller-render leaves a
// JSON manifest of the K8s host set it just provisioned for.
const kubernetesArtifactsSnapshotFile = ".snapshot.json"

// kubernetesArtifactsCheck verifies that the host this task is running
// on is part of the most recent controller-render snapshot. It's the
// preflight that turns "kubeconfig not found at deploy time" into a
// clear "K8s host set drifted, re-run kubernetes-master deploy" error.
//
// spec.Target points at the snapshot directory (typically
// ${cluster.resource_dir}/cloudhis/kubernetes). The host being
// checked is ctx.HostAddr (set by the runner from each fan-out task's
// inventory entry).
func (e *ActionExecutor) kubernetesArtifactsCheck(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Target == "" {
		return pathRequiredError(ctx, actionName, "kubernetes_artifacts_check.target")
	}
	if ctx.HostAddr == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "目标主机",
			Reason:     "无法确定当前主机地址",
			Detail:     "ctx.HostAddr 为空,无法核对快照",
			Suggestion: "联系 dfctl 维护方,这是引擎层的 bug。",
		}
	}
	snapPath := filepath.Join(spec.Target, kubernetesArtifactsSnapshotFile)
	body, err := os.ReadFile(snapPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "快照文件 " + snapPath,
				Reason:     "K8s 节点快照不存在",
				Detail:     "尚未运行 controller-render 生成 K8s 证书和 kubeconfig",
				Suggestion: "先执行 kubernetes-master 部署(它会触发 controller-render 生成所有节点的证书和 kubeconfig),再部署当前组件。",
			}
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "快照文件 " + snapPath,
			Reason:     "读取快照失败",
			Detail:     err.Error(),
			Suggestion: "检查 cloudhis/kubernetes 目录权限,或重新执行 kubernetes-master 部署。",
		}
	}
	var snap kubernetesArtifactsSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "快照文件 " + snapPath,
			Reason:     "快照格式无效",
			Detail:     err.Error(),
			Suggestion: "重新执行 kubernetes-master 部署以重建快照。",
		}
	}
	for _, addr := range snap.AllAddresses {
		if addr == ctx.HostAddr {
			return nil
		}
	}
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "目标主机 " + ctx.HostAddr,
		Reason:     "K8s 节点集合已变化",
		Detail: fmt.Sprintf(
			"主机 %s 不在最近一次 controller-render 生成的节点集合中(snapshot=%v)",
			ctx.HostAddr,
			snap.AllAddresses,
		),
		Suggestion: "在 components 页面调整 host 选择后,需要先重新部署 kubernetes-master 让 controller-render 为新节点生成证书和 kubeconfig,再部署当前组件。",
	}
}

// subtractStringSlice returns the elements of `from` that are not in
// `remove`, preserving order. Used to derive the node-only address
// list from the (master+node) kubelet roster minus the master roster.
func subtractStringSlice(from, remove []string) []string {
	if len(from) == 0 {
		return nil
	}
	skip := make(map[string]struct{}, len(remove))
	for _, x := range remove {
		skip[x] = struct{}{}
	}
	out := make([]string, 0, len(from))
	for _, x := range from {
		if _, ok := skip[x]; ok {
			continue
		}
		out = append(out, x)
	}
	return out
}

func kubernetesEtcdCSRJSON(addresses string) string {
	hosts := append(strings.Fields(addresses), "127.0.0.1")
	var quoted []string
	for _, host := range hosts {
		quoted = append(quoted, fmt.Sprintf("    %q", host))
	}
	return `{
  "CN": "etcd",
  "hosts": [
` + strings.Join(quoted, ",\n") + `
  ],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "k8s",
      "OU": "System"
    }
  ]
}
`
}

// etcdAddressesOrFallback returns etcdAddresses if non-empty, otherwise falls back
// to fallback (typically master addresses for colocated etcd-on-master topologies).
func etcdAddressesOrFallback(etcdAddresses string, fallback string) string {
	if strings.TrimSpace(etcdAddresses) != "" {
		return etcdAddresses
	}
	return fallback
}

func kubernetesServingCSRJSON(vip string, masterAddresses string, serviceIP string) string {
	hosts := []string{"127.0.0.1"}
	if vip != "" {
		hosts = append(hosts, vip)
	}
	hosts = append(hosts, strings.Fields(masterAddresses)...)
	if serviceIP != "" {
		hosts = append(hosts, serviceIP)
	}
	hosts = append(hosts,
		"kubernetes",
		"kubernetes.default",
		"kubernetes.default.svc",
		"kubernetes.default.svc.cluster",
		"kubernetes.default.svc.cluster.local",
	)
	var quoted []string
	for _, host := range hosts {
		quoted = append(quoted, fmt.Sprintf("    %q", host))
	}
	return `{
  "CN": "kubernetes",
  "hosts": [
` + strings.Join(quoted, ",\n") + `
  ],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "k8s",
      "OU": "System"
    }
  ]
}
`
}

func kubernetesKubeletCSRJSON(nodeName string) string {
	return `{
  "CN": "system:node:` + nodeName + `",
  "hosts": [
    "` + nodeName + `"
  ],
  "key": {
    "algo": "rsa",
    "size": 2048
  },
  "names": [
    {
      "C": "CN",
      "ST": "HangZhou",
      "L": "XS",
      "O": "system:nodes",
      "OU": "System"
    }
  ]
}
`
}

type kubeconfigSpec struct {
	Path        string
	Cluster     string
	User        string
	Context     string
	Certificate string
	Key         string
}

func (e *ActionExecutor) createKubeconfig(ctx TaskContext, actionName string, kubectl string, apiServer string, sslDir string, spec kubeconfigSpec) error {
	commands := [][]string{
		{"config", "set-cluster", spec.Cluster, "--certificate-authority=" + filepath.Join(sslDir, "ca.pem"), "--embed-certs=true", "--server", apiServer, "--kubeconfig", spec.Path},
		{"config", "set-credentials", spec.User, "--client-certificate=" + spec.Certificate, "--client-key=" + spec.Key, "--embed-certs=true", "--kubeconfig", spec.Path},
		{"config", "set-context", spec.Context, "--cluster=" + spec.Cluster, "--user=" + spec.User, "--kubeconfig", spec.Path},
		{"config", "use-context", spec.Context, "--kubeconfig", spec.Path},
	}
	for _, args := range commands {
		if err := e.runCommand(ctx, ActionSpec{Type: "run_command", Name: "生成" + filepath.Base(spec.Path), Command: kubectl, Args: args}, actionName); err != nil {
			return err
		}
	}
	// kubectl writes the kubeconfig as 0o600 by default but does NOT
	// reapply the mode on subsequent set-* invocations against the
	// same file — and on some systems (umask=0022) the initial create
	// produces 0o644. Force 0o600 explicitly so embedded client cert
	// + key are not world-readable. kubeconfig is effectively a K8s
	// super-user credential when the embedded user maps to admin /
	// kube-controller-manager / kube-scheduler.
	//
	// IsNotExist is tolerated: tests inject a mock kubectl that
	// records commands but never produces a real kubeconfig file. In
	// production kubectl always writes the file before returning, so
	// the chmod path is exercised.
	if err := os.Chmod(spec.Path, 0o600); err != nil && !os.IsNotExist(err) {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "kubeconfig " + spec.Path,
			Reason:     "收紧 kubeconfig 权限失败",
			Detail:     err.Error(),
			Suggestion: "检查目标路径权限。",
		}
	}
	return nil
}

func (e *ActionExecutor) kubectl(ctx TaskContext, spec ActionSpec, actionName string, verb string) error {
	if spec.File == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "K8s 资源文件",
			Reason:     "file 为空",
			Detail:     "kubectl action file 不能为空",
			Suggestion: "补充要 apply/delete 的 YAML 文件路径。",
		}
	}
	args := []string{}
	if spec.Kubeconfig != "" {
		args = append(args, "--kubeconfig", spec.Kubeconfig)
	}
	if spec.Namespace != "" {
		args = append(args, "-n", spec.Namespace)
	}
	args = append(args, verb, "-f", spec.File)
	if verb == "delete" && spec.IgnoreNotFound {
		args = append(args, "--ignore-not-found=true")
	}
	// kubectl runs on the control node, talking to the cluster's
	// apiserver via kubeconfig. Per design: deploy nodes never need
	// kubectl installed.
	return e.runControlCommand(ctx, ActionSpec{Type: "run_command", Name: actionName, Command: "kubectl", Args: args}, actionName)
}

// runControlCommand mirrors runCommand but dispatches via
// e.controlCmd instead of e.backend.Cmd. Used by Actions that the
// design pins to the control node (kubectl, cfssl, etc).
func (e *ActionExecutor) runControlCommand(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Command == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "命令配置 command",
			Reason:     "命令为空",
			Detail:     "run_command.command 不能为空",
			Suggestion: "补充明确的命令和参数，不要把整段 shell 写成一个字符串。",
		}
	}
	opts := exec.RunOptions{Context: e.executionContext(), WorkDir: spec.WorkDir}
	if err := opts.Context.Err(); err != nil {
		return err
	}
	if spec.Timeout != "" {
		parsedTimeout, err := time.ParseDuration(spec.Timeout)
		if err != nil {
			return timeoutError(ctx, actionName, spec.Timeout, err)
		}
		opts.Timeout = parsedTimeout
	}
	result := e.controlCmd.RunWithOptions(opts, spec.Command, spec.Args...)
	if result.Err != nil || result.ExitCode != 0 {
		detail := strings.TrimSpace(string(result.Stderr))
		if detail == "" {
			detail = strings.TrimSpace(string(result.Stdout))
		}
		if detail == "" && result.Err != nil {
			detail = result.Err.Error()
		}
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "命令 " + strings.Join(append([]string{spec.Command}, spec.Args...), " "),
			Reason:     "命令执行失败",
			Detail:     fmt.Sprintf("exit_code=%d output=%s", result.ExitCode, detail),
			Suggestion: "根据命令输出定位失败原因；必要时单独在控制节点执行该命令确认环境。",
		}
	}
	return nil
}
