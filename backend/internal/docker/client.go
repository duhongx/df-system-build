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
	"time"

	"df-build-server/pkg/logger"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
)

// AuthConfig is an alias for Docker registry auth config
type AuthConfig = types.AuthConfig

// Client wraps Docker SDK interactions
type Client struct {
	cli *client.Client
}

func (c *Client) Close() error {
	if c == nil || c.cli == nil {
		return nil
	}
	return c.cli.Close()
}

// NewClient creates a Docker client via SDK
func NewClient(ctx context.Context, host string) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("Docker 客户端创建失败: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = cli.Ping(pingCtx)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("Docker 连接失败: %w", err)
	}
	return &Client{cli: cli}, nil
}

// CmdReader wraps a reader and captures exit code
type CmdReader struct {
	reader     io.ReadCloser
	ExitCode   int
	hasError   atomic.Bool
	streamDone <-chan struct{}
	streamErr  error
	stopStream func()
}

func (r *CmdReader) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

func (r *CmdReader) Close() error {
	r.reader.Close()
	if r.streamDone != nil {
		if r.stopStream != nil {
			r.stopStream()
		}
		<-r.streamDone
		if r.streamErr != nil {
			r.ExitCode = 1
			return r.streamErr
		}
	}
	// If stream reported an error but exit code is 0, override
	if r.hasError.Load() && r.ExitCode == 0 {
		r.ExitCode = 1
	}
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

	defer tar.Close()
	resp, err := c.cli.ImageBuild(ctx, tar, buildOpts)
	if err != nil {
		return nil, fmt.Errorf("docker build 启动失败: %w", err)
	}

	return dockerJSONStream(ctx, resp.Body, func(ctx context.Context, _ string) error {
		if len(tags) == 0 {
			return fmt.Errorf("构建结果缺少目标标签，无法确认镜像")
		}
		var imageID string
		for _, tag := range tags {
			info, _, err := c.cli.ImageInspectWithRaw(ctx, tag)
			if err != nil {
				return fmt.Errorf("确认构建镜像 %s 失败: %w", tag, err)
			}
			if info.ID == "" || (imageID != "" && info.ID != imageID) {
				return fmt.Errorf("构建镜像标签指向不一致: %s", tag)
			}
			imageID = info.ID
		}
		return nil
	}), nil
}

// PushImage pushes a Docker image to a registry
func (c *Client) PushImage(ctx context.Context, imageName string) (*CmdReader, error) {
	return c.PushImageWithAuth(ctx, imageName, "e30=")
}

func (c *Client) ResolveImageDigest(ctx context.Context, imageName, auth string) (string, error) {
	remote, err := c.cli.DistributionInspect(ctx, imageName, auth)
	if err != nil {
		return "", err
	}
	digest := remote.Descriptor.Digest.String()
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return "", fmt.Errorf("仓库返回无效镜像 digest")
	}
	repo := strings.Split(imageName, "@")[0]
	if colon, slash := strings.LastIndex(repo, ":"), strings.LastIndex(repo, "/"); colon > slash {
		repo = repo[:colon]
	}
	return repo + "@" + digest, nil
}

// PushImageWithAuth pushes a Docker image with explicit auth
func (c *Client) PushImageWithAuth(ctx context.Context, imageName, registryAuth string) (*CmdReader, error) {
	logger.Log.Infof("Docker SDK: push %s", imageName)

	resp, err := c.cli.ImagePush(ctx, imageName, types.ImagePushOptions{RegistryAuth: registryAuth})
	if err != nil {
		return nil, fmt.Errorf("docker push 启动失败: %w", err)
	}

	return dockerJSONStream(ctx, resp, func(ctx context.Context, digest string) error {
		if digest == "" {
			return fmt.Errorf("推送响应提前结束，未收到镜像 %s 的完成 digest", imageName)
		}
		remote, err := c.cli.DistributionInspect(ctx, imageName, registryAuth)
		if err != nil {
			return fmt.Errorf("确认仓库镜像 %s 失败: %w", imageName, err)
		}
		if remote.Descriptor.Digest.String() != digest {
			return fmt.Errorf("仓库镜像 digest 不一致: %s；推送=%s；仓库=%s", imageName, digest, remote.Descriptor.Digest)
		}
		return nil
	}), nil
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

// ReadLines reads from a reader line by line
func ReadLines(reader io.Reader, callback func(line string)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		callback(scanner.Text())
	}
	return scanner.Err()
}
