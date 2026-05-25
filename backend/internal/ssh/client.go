package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"
	"df-build-server/pkg/logger"

	"golang.org/x/crypto/ssh"
)

// Client wraps SSH connections to remote servers
type Client struct {
	conn   *ssh.Client
	server *model.RemoteServer
}

// Connect establishes an SSH connection to the remote server
func Connect(server *model.RemoteServer) (*Client, error) {
	var authMethod ssh.AuthMethod

	// Decrypt credential
	credential := ""
	if server.CredentialEncrypted != "" {
		decrypted, err := crypto.Decrypt(server.CredentialEncrypted)
		if err != nil {
			return nil, fmt.Errorf("凭据解密失败: %w", err)
		}
		credential = decrypted
	}

	switch server.AuthType {
	case "password":
		authMethod = ssh.Password(credential)
	case "ssh_key":
		signer, err := ssh.ParsePrivateKey([]byte(credential))
		if err != nil {
			return nil, fmt.Errorf("SSH Key 解析失败: %w", err)
		}
		authMethod = ssh.PublicKeys(signer)
	default:
		return nil, fmt.Errorf("不支持的认证方式: %s", server.AuthType)
	}

	config := &ssh.ClientConfig{
		User:            server.Username,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", server.Host, server.Port)
	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败 (%s): %w", addr, err)
	}

	logger.Log.Infof("SSH connected to %s@%s", server.Username, addr)
	return &Client{conn: conn, server: server}, nil
}

// Exec runs a command on the remote server and returns stdout/stderr readers
func (c *Client) Exec(ctx context.Context, cmd string) (string, string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("创建 SSH session 失败: %w", err)
	}
	defer session.Close()

	// Handle context cancellation
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.Signal(ssh.SIGTERM)
			session.Close()
		case <-done:
		}
	}()

	output, err := session.CombinedOutput(cmd)
	close(done)

	if err != nil {
		return string(output), "", err
	}
	return string(output), "", nil
}

// ExecStream runs a command and returns a reader for streaming output
func (c *Client) ExecStream(ctx context.Context, cmd string) (io.Reader, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("创建 SSH session 失败: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	session.Stderr = session.Stdout // merge

	if err := session.Start(cmd); err != nil {
		session.Close()
		return nil, fmt.Errorf("命令启动失败: %w", err)
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		session.Signal(ssh.SIGTERM)
		session.Close()
	}()

	return stdout, nil
}

// Close closes the SSH connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// TestConnection attempts to connect and run a simple command
func TestConnection(server *model.RemoteServer) error {
	client, err := Connect(server)
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, _, err := client.Exec(ctx, "echo ok")
	if err != nil {
		return fmt.Errorf("命令执行失败: %w", err)
	}

	logger.Log.Infof("SSH test to %s: %s", server.Host, output)
	return nil
}

// Upload uploads a local file to the remote server via SCP/SFTP
func (c *Client) Upload(ctx context.Context, localPath, remotePath string) error {
	// Use SFTP sub-package
	sftpClient, err := NewSFTPClient(c.conn)
	if err != nil {
		return err
	}
	defer sftpClient.Close()

	return sftpClient.Upload(localPath, remotePath)
}

// CheckDir checks if a remote directory exists, creates it if not
func (c *Client) CheckDir(ctx context.Context, path string) error {
	cmd := fmt.Sprintf("test -d %s && echo exists || mkdir -p %s", path, path)
	_, _, err := c.Exec(ctx, cmd)
	return err
}

// Dial is a helper for net.Dial used in SFTP
func (c *Client) Dial(network, addr string) (net.Conn, error) {
	return c.conn.Dial(network, addr)
}
