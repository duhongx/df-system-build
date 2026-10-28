package opsremote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Conn struct {
	SSH         *ssh.Client
	SFTP        *sftp.Client
	Fingerprint string
	done        chan struct{}
	once        sync.Once
}

func Connect(ctx context.Context, server model.Server, pin string) (*Conn, error) {
	credential, err := crypto.Decrypt(server.CredentialEncrypted)
	if err != nil {
		return nil, fmt.Errorf("解密 SSH 凭据失败")
	}
	var auth ssh.AuthMethod
	switch server.AuthType {
	case "password":
		auth = ssh.Password(credential)
	case "certificate", "ssh_key":
		var signer ssh.Signer
		if server.CertPassphrase != "" {
			pass, e := crypto.Decrypt(server.CertPassphrase)
			if e != nil {
				return nil, fmt.Errorf("解密 SSH 私钥密码失败")
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(credential), []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(credential))
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 私钥失败")
		}
		auth = ssh.PublicKeys(signer)
	default:
		return nil, fmt.Errorf("未知 SSH 认证方式")
	}
	timeout := time.Duration(server.ConnTimeout) * time.Second
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 10 * time.Second
	}
	port := server.Port
	if port == 0 {
		port = 22
	}
	address := net.JoinHostPort(server.Host, strconv.Itoa(port))
	c := &Conn{done: make(chan struct{})}
	cfg := &ssh.ClientConfig{User: server.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		c.Fingerprint = ssh.FingerprintSHA256(key)
		if pin != "" && pin != c.Fingerprint {
			return fmt.Errorf("SSH 主机指纹与已保存值不同，请核实服务器身份")
		}
		return nil
	}}
	raw, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	raw.SetDeadline(deadline)
	cc, chans, reqs, err := ssh.NewClientConn(raw, address, cfg)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	raw.SetDeadline(time.Time{})
	c.SSH = ssh.NewClient(cc, chans, reqs)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-c.done:
		}
	}()
	c.SFTP, err = sftp.NewClient(c.SSH)
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.done)
		if c.SSH != nil {
			_ = c.SSH.Close()
		}
	})
}
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("远程输出超过 %d 字节限制", b.limit)
	}
	return b.Buffer.Write(p)
}
func (c *Conn) Run(ctx context.Context, command string, limit int) ([]byte, error) {
	session, err := c.SSH.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	out := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: 8192}
	session.Stdout = out
	session.Stderr = stderr
	if err := session.Start(command); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("远程命令失败: %w；%s", err, stderr.String())
		}
	}
	return out.Bytes(), nil
}
func (c *Conn) WriteFile(name string, data []byte, mode uint32) error {
	f, err := c.SFTP.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if err := f.Chmod(os.FileMode(mode)); err != nil {
		f.Close()
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (c *Conn) ReadFile(name string, limit int64) ([]byte, error) {
	f, err := c.SFTP.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// A zero limit is used for complete inspection result files. Progress and
	// other small control files continue to use explicit limits.
	if limit == 0 {
		return io.ReadAll(f)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fmt.Errorf("远程文件超过大小限制")
	}
	return data, err
}
func (c *Conn) EnsureRunner(ctx context.Context) (string, string, error) {
	uname, err := c.Run(ctx, "uname -sm", 1024)
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(string(uname))
	if len(fields) != 2 || fields[0] != "Linux" {
		return "", "", fmt.Errorf("仅支持 Linux 主机，当前为 %s", strings.TrimSpace(string(uname)))
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[fields[1]]
	binary, digest, err := Runner(arch)
	if err != nil {
		return "", "", err
	}
	home, err := c.SFTP.Getwd()
	if err != nil {
		return "", "", err
	}
	base := path.Join(home, ".dfops", "inspection")
	if err := c.SFTP.MkdirAll(base); err != nil {
		return "", "", err
	}
	if err := c.SFTP.Chmod(base, 0700); err != nil {
		return "", "", err
	}
	exe := path.Join(base, "runner-"+digest[:16])
	if _, err := c.SFTP.Stat(exe); err != nil {
		tmp := exe + ".upload-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := c.WriteFile(tmp, binary, 0700); err != nil {
			return "", "", err
		}
		defer c.SFTP.Remove(tmp)
		actual, err := c.Run(ctx, "sha256sum -- "+Quote(tmp), 1024)
		if err != nil {
			return "", "", err
		}
		if !strings.HasPrefix(string(actual), digest+" ") {
			return "", "", fmt.Errorf("执行器上传校验失败")
		}
		if err := c.SFTP.PosixRename(tmp, exe); err != nil {
			return "", "", err
		}
	}
	// Check cached binaries too; an old/corrupted file cannot be executed silently.
	actual, err := c.Run(ctx, "sha256sum -- "+Quote(exe), 1024)
	if err != nil || !strings.HasPrefix(string(actual), digest+" ") {
		return "", "", fmt.Errorf("远程执行器校验失败，请核对缓存文件")
	}
	return exe, base, nil
}
func (c *Conn) JSONFile(name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFile(name, b, 0600)
}
func Digest(b []byte) string { h := sha256.Sum256(b); return fmt.Sprintf("%x", h) }
