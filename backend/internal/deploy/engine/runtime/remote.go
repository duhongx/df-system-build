package runtime

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"df-build-server/internal/deploy/engine"
	"df-build-server/internal/deploy/engine/exec"
	"df-build-server/internal/deploy/engine/store"
)

// runRemoteComponent dispatches one (component, phase) pair against
// every target host directly from the control node. Replaces the
// legacy "rsync /opt/his-deploy + ssh remote dfctl" pipeline.
//
// Execution model: task fan-out (matches ansible's default).
//
// BuildPlanForPhases emits the plan ordered as
//
//	[task1@h1, task1@h2, task1@h3, task2@h1, task2@h2, task2@h3, ...]
//
// We walk the plan in this exact order. SSH connections are reused
// per-host via backendPool, and per-host ActionExecutor instances
// are cached so we don't rebuild them on every fan-out hop.
//
// Why task-then-host instead of host-then-task: cluster components
// (etcd, postgresql patroni, elasticsearch, ...) require all peers
// to start within the same window so raft / cluster bootstrap can
// form a quorum. Host-serial execution makes the first peer wait
// 60s for siblings that haven't been provisioned yet, timing out
// before they ever start. Task fan-out installs the binary on every
// peer first, then issues `restart --no-block` on every peer, so by
// the time the test phase runs all members are alive.
//
// Errors propagate as-is on the first failure; the caller decides
// whether to roll back. SSH state for the still-pending hosts is
// torn down by `pool.closeAll`.
func (r *Runtime) runRemoteComponent(ctx context.Context, depID int64, cfg *engine.Config, comp, phase, host string, continueOnError bool) error {
	settings, err := r.store.GetDeploymentSettings(ctx)
	if err != nil {
		return fmt.Errorf("读取连接设置: %w", err)
	}

	var phases []string
	if phase == "deploy" {
		phases = engine.DeployPhases()
	} else {
		phases = []string{phase}
	}

	plan, err := engine.BuildPlanForPhases(cfg, comp, phases, host)
	if err != nil {
		if isNoTask(err) {
			return nil
		}
		return err
	}

	pool := r.poolFor(depID, settings)
	defer pool.closeAll()

	factoryCfg := *cfg
	controlCloudhisRoot := ""
	if factoryCfg.Cluster.RemoteRoot != "" {
		controlCloudhisRoot = filepath.Join(factoryCfg.Cluster.RemoteRoot, "cloudhis")
	}

	// Per-host runner cache. NewActionExecutor / Runner are cheap
	// structs but each captures its backend reference, so reusing
	// them on subsequent fan-out hops to the same host avoids the
	// allocation churn and (more importantly) keeps log scoping
	// stable.
	runners := map[string]*engine.Runner{}
	singleTask := make([]engine.PlannedTask, 1)
	for _, item := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := item.Host.Address
		if key == "" {
			key = item.Host.Name
		}
		runner, ok := runners[key]
		if !ok {
			backend, err := pool.get(item.Host)
			if err != nil {
				r.publishHostError(depID, comp, item.Host, "连接目标主机失败", err)
				return fmt.Errorf("连接 %s (%s) 失败: %w", item.Host.Name, item.Host.Address, err)
			}
			executor := engine.NewActionExecutor(engine.ActionExecutorOptions{
				ResourceDir:         factoryCfg.Cluster.ResourceDir,
				StateDir:            factoryCfg.Cluster.StateDir,
				Backend:             &backend,
				ControlCloudhisRoot: controlCloudhisRoot,
			})
			runner = &engine.Runner{
				Executor:        executor,
				Logger:          r.hub.Logger(depID, comp),
				ContinueOnError: continueOnError,
			}
			runners[key] = runner
		}
		singleTask[0] = item
		if err := runner.ExecuteContext(ctx, singleTask); err != nil {
			return err
		}
	}
	return nil
}

// backendPool caches ssh+sftp connections keyed by host address for
// the lifetime of one deployment dispatch. Closed once at the end of
// the dispatch via closeAll.
// HostCredentials carries the per-host SSH credentials resolved from an
// external source (in df-build-system: the Server Management registry). It
// lets the runtime dial each host with its own credentials instead of a single
// global SSH user/key from DeploymentSettings.
type HostCredentials struct {
	User           string
	Password       string
	PrivateKey     []byte
	PrivateKeyPath string
	Port           int
	HostKey        []byte
}

// CredentialResolver maps a host address (or name) to its SSH credentials.
// Returning ok=false means "no per-host credentials; fall back to the global
// DeploymentSettings defaults". Injected via runtime.Options so the engine
// stays independent of df-build-server's model/repository packages.
type CredentialResolver func(addr string) (HostCredentials, bool)

type backendPool struct {
	settings *store.DeploymentSettings
	resolver CredentialResolver
	mu       sync.Mutex
	entries  map[string]*pooledBackend
	// localAddrs is the set of IPv4 addresses bound to the dfctl-web
	// host. Used by get() to short-circuit a real SSH dial when the
	// target IS the deploy machine — saves a session, dodges the
	// "ssh to localhost may not be enabled" pitfall, and keeps
	// LocalBackend semantics for BindDeployHost components like
	// controller-render.
	localAddrs map[string]bool
}

type pooledBackend struct {
	rb      *exec.RemoteBackend // nil for local short-circuit entries
	backend exec.Backend
}

func (r *Runtime) poolFor(_ int64, settings *store.DeploymentSettings) *backendPool {
	return &backendPool{
		settings:   settings,
		resolver:   r.credResolver,
		entries:    map[string]*pooledBackend{},
		localAddrs: localIPv4Set(),
	}
}

// localIPv4Set returns the IPv4 addresses bound to this machine,
// minus loopbacks. Mirrors the helper in package web — duplicated
// here to keep runtime independent of web. If enumeration fails we
// return an empty set, which means we conservatively dial SSH for
// every host (current behaviour); never returns nil.
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
		v4 := ip.To4()
		if v4 == nil {
			continue
		}
		out[v4.String()] = true
	}
	return out
}

func (p *backendPool) get(host engine.Host) (exec.Backend, error) {
	addr := host.Address
	if addr == "" {
		addr = host.Name
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pb, ok := p.entries[addr]; ok {
		return pb.backend, nil
	}
	// Short-circuit: target IS the deploy machine — no SSH needed.
	// Return a LocalBackend so render-phase artefacts and node-side
	// commands all run in-process. controller-render relies on this
	// (cfssl + kubectl bundled into the offline tree on the deploy
	// node, no SSH self-connect required).
	if p.localAddrs[addr] {
		be := exec.NewLocalBackend()
		p.entries[addr] = &pooledBackend{rb: nil, backend: be}
		return be, nil
	}
	// Per-host credentials from the Server registry take precedence over the
	// global DeploymentSettings SSH user/key. This is the df-build-system path
	// (Requirement 2.5/2.6): credentials are read live from model.Server at
	// dial time, so a credential rotation is picked up without re-binding.
	if p.resolver != nil {
		if creds, ok := p.resolver(addr); ok {
			user := creds.User
			if user == "" {
				user = "root"
			}
			port := creds.Port
			if port <= 0 {
				port = 22
			}
			dialAddr := addr
			if !strings.Contains(addr, ":") {
				dialAddr = fmt.Sprintf("%s:%d", addr, port)
			}
			rb, be, err := exec.NewRemoteBackend(exec.SSHConfig{
				Addr:           dialAddr,
				User:           user,
				Password:       creds.Password,
				PrivateKey:     creds.PrivateKey,
				PrivateKeyPath: creds.PrivateKeyPath,
				HostKey:        creds.HostKey,
			})
			if err != nil {
				return exec.Backend{}, err
			}
			p.entries[addr] = &pooledBackend{rb: rb, backend: be}
			return be, nil
		}
	}
	keyPath := p.settings.SSHPrivateKeyPath
	if keyPath == "" {
		// Fall back to the convention every Linux/macOS deploy node
		// satisfies. We probe id_ed25519 first because it's the
		// modern default; id_rsa is the legacy fallback. Operators
		// who keep both or rotate keys can override via the settings
		// UI without restarting the service.
		home, err := os.UserHomeDir()
		if err != nil {
			return exec.Backend{}, fmt.Errorf("解析私钥默认路径: %w", err)
		}
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			candidate := filepath.Join(home, ".ssh", name)
			if _, err := os.Stat(candidate); err == nil {
				keyPath = candidate
				break
			}
		}
		if keyPath == "" {
			return exec.Backend{}, fmt.Errorf("未找到默认 SSH 私钥 (尝试 ~/.ssh/{id_ed25519,id_ecdsa,id_rsa}); 请在「全局配置」里设置 ssh_private_key_path")
		}
	}
	user := p.settings.SSHUser
	if user == "" {
		user = "root"
	}
	port := p.settings.SSHPort
	if port <= 0 {
		port = 22
	}
	dialAddr := addr
	// Append the configured port unless host.Address already carries
	// one. SSHConfig.Addr defaults to :22 when none is provided, but
	// we want operator-configured ssh_port to win over the default.
	if !strings.Contains(addr, ":") {
		dialAddr = fmt.Sprintf("%s:%d", addr, port)
	}
	rb, be, err := exec.NewRemoteBackend(exec.SSHConfig{
		Addr:           dialAddr,
		User:           user,
		PrivateKeyPath: keyPath,
	})
	if err != nil {
		return exec.Backend{}, err
	}
	p.entries[addr] = &pooledBackend{rb: rb, backend: be}
	return be, nil
}

func (p *backendPool) closeAll() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pb := range p.entries {
		// Local short-circuit entries have rb==nil; nothing to
		// close on those — LocalBackend doesn't hold a connection.
		if pb.rb != nil {
			_ = pb.rb.Close()
		}
	}
	p.entries = nil
}

// publishHostError ships an error frame to the SSE hub so the UI sees
// a single tidy "host X 连接失败: …" line instead of the raw stack the
// caller would otherwise wrap into a generic deploy error.
func (r *Runtime) publishHostError(depID int64, component string, host engine.Host, reason string, err error) {
	addr := host.Address
	if addr == "" {
		addr = host.Name
	}
	r.hub.Publish(context.Background(), depID, LogEntry{
		Component: component,
		Host:      host.Name,
		Action:    reason,
		Type:      "remote_log",
		Status:    "failed",
		IsError:   true,
		Detail:    fmt.Sprintf("host=%s err=%s", addr, err.Error()),
	})
}

// hubLineWriter / readLine kept for legacy callers in this package; no
// longer used by the new direct-SSH path. We leave them here so any
// future helper that needs newline-buffered fan-out doesn't have to
// reinvent it.
type hubLineWriter struct {
	hub       *Hub
	depID     int64
	component string
	host      string
	buf       bytes.Buffer
}

func newHubLineWriter(hub *Hub, depID int64, component, host string) *hubLineWriter {
	return &hubLineWriter{hub: hub, depID: depID, component: component, host: host}
}

func (w *hubLineWriter) Write(p []byte) (int, error) {
	if w == nil || w.hub == nil {
		return len(p), nil
	}
	w.buf.Write(p)
	for {
		line, ok := readLine(&w.buf)
		if !ok {
			break
		}
		if line == "" {
			continue
		}
		w.publish(line)
	}
	return len(p), nil
}

func (w *hubLineWriter) flush() {
	if w == nil {
		return
	}
	if w.buf.Len() == 0 {
		return
	}
	tail := strings.TrimRight(w.buf.String(), "\r\n")
	w.buf.Reset()
	if tail != "" {
		w.publish(tail)
	}
}

func (w *hubLineWriter) publish(line string) {
	w.hub.Publish(context.Background(), w.depID, LogEntry{
		Component: w.component,
		Host:      w.host,
		Action:    "远程输出",
		Type:      "remote_log",
		Status:    "info",
		Detail:    line,
	})
}

func readLine(buf *bytes.Buffer) (string, bool) {
	idx := bytes.IndexByte(buf.Bytes(), '\n')
	if idx < 0 {
		return "", false
	}
	raw := buf.Next(idx + 1)
	line := strings.TrimRight(string(raw[:idx]), "\r")
	return line, true
}
