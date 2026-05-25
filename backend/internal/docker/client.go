package docker

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"df-build-server/pkg/logger"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
)

// CacheMount represents a host-to-container volume mapping
type CacheMount struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
}

// AuthConfig is an alias for Docker registry auth config
type AuthConfig = types.AuthConfig

// ContainerOpts defines options for running a Docker container
type ContainerOpts struct {
	Image       string
	Command     []string
	WorkDir     string
	SourceDir   string
	CacheMounts []CacheMount
	CPULimit    string
	MemLimit    string
}

// Client wraps Docker SDK interactions
type Client struct {
	cli *client.Client
}

// NewClient creates a Docker client via SDK
func NewClient(host string) (*Client, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("Docker 客户端创建失败: %w", err)
	}
	_, err = cli.Ping(context.Background())
	if err != nil {
		return nil, fmt.Errorf("Docker 连接失败: %w", err)
	}
	return &Client{cli: cli}, nil
}

// CmdReader wraps a reader and captures exit code
type CmdReader struct {
	reader   io.ReadCloser
	ExitCode int
	done     bool
	hasError atomic.Bool
	waitFn   func() int64
}

func (r *CmdReader) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

func (r *CmdReader) Close() error {
	r.reader.Close()
	if !r.done && r.waitFn != nil {
		r.ExitCode = int(r.waitFn())
		r.done = true
	}
	// If stream reported an error but exit code is 0, override
	if r.hasError.Load() && r.ExitCode == 0 {
		r.ExitCode = 1
	}
	return nil
}

// RunContainer starts a container and returns a CmdReader for streaming output
func (c *Client) RunContainer(ctx context.Context, opts ContainerOpts) (*CmdReader, error) {
	var binds []string
	if opts.SourceDir != "" && opts.WorkDir != "" {
		binds = append(binds, fmt.Sprintf("%s:%s", opts.SourceDir, opts.WorkDir))
	}
	for _, cm := range opts.CacheMounts {
		if cm.HostPath != "" && cm.ContainerPath != "" {
			binds = append(binds, fmt.Sprintf("%s:%s", cm.HostPath, cm.ContainerPath))
		}
	}

	var memLimit int64
	if opts.MemLimit != "" { memLimit = parseMemory(opts.MemLimit) }
	var cpuQuota int64
	if opts.CPULimit != "" { cpuQuota = parseCPU(opts.CPULimit) }

	cfg := &container.Config{
		Image: opts.Image, Cmd: opts.Command, WorkingDir: opts.WorkDir,
	}
	hostCfg := &container.HostConfig{
		Binds: binds,
		Resources: container.Resources{Memory: memLimit, CPUQuota: cpuQuota},
	}

	logger.Log.Infof("Docker SDK: run %s %v", opts.Image, opts.Command)

	resp, err := c.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("容器创建失败: %w", err)
	}
	containerID := resp.ID

	if err := c.cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{}); err != nil {
		c.cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("容器启动失败: %w", err)
	}

	logReader, err := c.cli.ContainerLogs(ctx, containerID, types.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	if err != nil {
		c.cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("获取容器日志失败: %w", err)
	}

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		defer logReader.Close()
		done := ctx.Done()
		buf := make([]byte, 32*1024)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := logReader.Read(buf)
			if n > 0 {
				pw.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	cliRef := c.cli
	waitFn := func() int64 {
		statusCh, errCh := cliRef.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
		select {
		case err := <-errCh:
			if err != nil {
				return -1
			}
			return -1
		case status := <-statusCh:
			cliRef.ContainerRemove(context.Background(), containerID, types.ContainerRemoveOptions{Force: true})
			return status.StatusCode
		}
	}

	return &CmdReader{reader: pr, ExitCode: 0, waitFn: waitFn}, nil
}

// PullImage pulls a Docker image if not present locally
func (c *Client) PullImage(ctx context.Context, imageName string) error {
	_, _, err := c.cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil { return nil }

	logger.Log.Infof("Pulling image: %s", imageName)
	reader, err := c.cli.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("拉取镜像失败: %w", err)
	}
	defer reader.Close()
	io.Copy(io.Discard, reader)
	logger.Log.Infof("Image pulled: %s", imageName)
	return nil
}

// BuildImage builds a Docker image from a directory with given tags
func (c *Client) BuildImage(ctx context.Context, contextDir string, tags ...string) (*CmdReader, error) {
	return c.BuildImageWithAuth(ctx, contextDir, nil, tags...)
}

// BuildImageWithAuth builds a Docker image with registry auth for pulling FROM images
func (c *Client) BuildImageWithAuth(ctx context.Context, contextDir string, authConfigs map[string]AuthConfig, tags ...string) (*CmdReader, error) {
	logger.Log.Infof("Docker SDK: build %s tags=%v", contextDir, tags)

	tar, err := archive.TarWithOptions(contextDir, &archive.TarOptions{})
	if err != nil {
		return nil, fmt.Errorf("创建构建上下文失败: %w", err)
	}

	buildOpts := types.ImageBuildOptions{
		Tags: tags, Remove: true, ForceRemove: true,
	}
	if authConfigs != nil {
		buildOpts.AuthConfigs = authConfigs
	}

	resp, err := c.cli.ImageBuild(ctx, tar, buildOpts)
	if err != nil {
		return nil, fmt.Errorf("docker build 启动失败: %w", err)
	}

	pr, pw := io.Pipe()
	cmdReader := &CmdReader{reader: pr, ExitCode: 0}
	go func() {
		defer pw.Close()
		defer resp.Body.Close()
		decoder := json.NewDecoder(resp.Body)
		for {
			select {
			case <-ctx.Done():
				cmdReader.hasError.Store(true)
				return
			default:
			}
			var msg struct {
				Stream string `json:"stream"`
				Error  string `json:"error"`
			}
			if err := decoder.Decode(&msg); err != nil { break }
			if msg.Error != "" {
				pw.Write([]byte("ERROR: " + msg.Error + "\n"))
				cmdReader.hasError.Store(true)
				break
			}
			if msg.Stream != "" { pw.Write([]byte(msg.Stream)) }
		}
	}()

	return cmdReader, nil
}

// PushImage pushes a Docker image to a registry
func (c *Client) PushImage(ctx context.Context, imageName string) (*CmdReader, error) {
	return c.PushImageWithAuth(ctx, imageName, "e30=")
}

// PushImageWithAuth pushes a Docker image with explicit auth
func (c *Client) PushImageWithAuth(ctx context.Context, imageName, registryAuth string) (*CmdReader, error) {
	logger.Log.Infof("Docker SDK: push %s", imageName)

	resp, err := c.cli.ImagePush(ctx, imageName, types.ImagePushOptions{RegistryAuth: registryAuth})
	if err != nil {
		return nil, fmt.Errorf("docker push 启动失败: %w", err)
	}

	pr, pw := io.Pipe()
	cmdReader := &CmdReader{reader: pr, ExitCode: 0}
	go func() {
		defer pw.Close()
		defer resp.Close()
		decoder := json.NewDecoder(resp)
		for {
			select {
			case <-ctx.Done():
				cmdReader.hasError.Store(true)
				return
			default:
			}
			var msg struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			if err := decoder.Decode(&msg); err != nil { break }
			if msg.Error != "" {
				pw.Write([]byte("ERROR: " + msg.Error + "\n"))
				cmdReader.hasError.Store(true)
				break
			}
			if msg.Status != "" { pw.Write([]byte(msg.Status + "\n")) }
		}
	}()

	return cmdReader, nil
}

// Login authenticates with a Docker registry
func (c *Client) Login(ctx context.Context, registryAddr, username, password string) error {
	_, err := c.cli.RegistryLogin(ctx, types.AuthConfig{
		ServerAddress: registryAddr,
		Username:      username,
		Password:      password,
	})
	if err != nil {
		return fmt.Errorf("docker login 失败: %w", err)
	}
	logger.Log.Infof("Docker login successful: %s", registryAddr)
	return nil
}

// GetAuthStr returns base64 encoded auth for push/pull with credentials
func GetAuthStr(username, password string) string {
	authConfig := registry.AuthConfig{Username: username, Password: password}
	encodedJSON, _ := json.Marshal(authConfig)
	return base64.URLEncoding.EncodeToString(encodedJSON)
}

// ParseCacheMounts parses JSON string into CacheMount slice
func ParseCacheMounts(jsonStr string) []CacheMount {
	if jsonStr == "" || jsonStr == "[]" { return nil }
	var mounts []CacheMount
	if err := json.Unmarshal([]byte(jsonStr), &mounts); err != nil {
		logger.Log.Warnf("Failed to parse cacheMounts: %v", err)
		return nil
	}
	return mounts
}

// ReadLines reads from a reader line by line
func ReadLines(reader io.Reader, callback func(line string)) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() { callback(scanner.Text()) }
}

func parseMemory(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasSuffix(s, "g") { return parseInt64(strings.TrimSuffix(s, "g")) * 1024 * 1024 * 1024 }
	if strings.HasSuffix(s, "m") { return parseInt64(strings.TrimSuffix(s, "m")) * 1024 * 1024 }
	return parseInt64(s)
}

func parseCPU(s string) int64 { return parseInt64(strings.TrimSpace(s)) * 100000 }

func parseInt64(s string) int64 { var v int64; fmt.Sscanf(s, "%d", &v); return v }
