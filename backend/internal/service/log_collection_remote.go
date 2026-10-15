package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type logRemoteFile interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

type logRemote interface {
	RealPath(string) (string, error)
	ReadDir(string) ([]os.FileInfo, error)
	OpenFile(string) (logRemoteFile, error)
	Close() error
}

type logSFTP struct {
	*sftp.Client
	conn      net.Conn
	stop      func() bool
	ioTimeout time.Duration
}

// Deadlines apply only during requested operations, not while a large file is
// being compressed locally and SSH's background reader is idle.
func (r *logSFTP) operation() func() {
	_ = r.conn.SetDeadline(time.Now().Add(r.ioTimeout))
	return func() { _ = r.conn.SetDeadline(time.Time{}) }
}
func (r *logSFTP) RealPath(p string) (string, error) {
	defer r.operation()()
	return r.Client.RealPath(p)
}
func (r *logSFTP) ReadDir(p string) ([]os.FileInfo, error) {
	defer r.operation()()
	return r.Client.ReadDir(p)
}
func (r *logSFTP) OpenFile(p string) (logRemoteFile, error) {
	defer r.operation()()
	file, err := r.Client.Open(p)
	if err != nil {
		return nil, err
	}
	return &logSFTPFile{File: file, remote: r}, nil
}

type logSFTPFile struct {
	*sftp.File
	remote *logSFTP
}

func (f *logSFTPFile) Read(p []byte) (int, error) {
	defer f.remote.operation()()
	return f.File.Read(p)
}
func (f *logSFTPFile) Stat() (os.FileInfo, error) { defer f.remote.operation()(); return f.File.Stat() }
func (f *logSFTPFile) Close() error               { defer f.remote.operation()(); return f.File.Close() }
func (r *logSFTP) Close() error {
	r.stop()
	// Closing the transport also unblocks outstanding SFTP/SSH operations.
	return r.conn.Close()
}

func connectLogRemote(ctx context.Context, host model.Server) (logRemote, error) {
	credential, err := crypto.Decrypt(host.CredentialEncrypted)
	if err != nil {
		return nil, fmt.Errorf("读取主机凭据失败: %w", err)
	}
	var auth ssh.AuthMethod
	switch host.AuthType {
	case "password":
		auth = ssh.Password(credential)
	case "certificate":
		var signer ssh.Signer
		if host.CertPassphrase != "" {
			pass, decryptErr := crypto.Decrypt(host.CertPassphrase)
			if decryptErr != nil {
				return nil, fmt.Errorf("读取私钥密码失败: %w", decryptErr)
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(credential), []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(credential))
		}
		if err != nil {
			return nil, fmt.Errorf("SSH 私钥解析失败: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	default:
		return nil, fmt.Errorf("不支持的主机认证方式: %s", host.AuthType)
	}
	timeout := time.Duration(host.ConnTimeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	port := host.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host.Host, strconv.Itoa(port))
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	success := false
	defer func() {
		if !success {
			stop()
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	sshConn, channels, requests, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User: host.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		return nil, fmt.Errorf("SSH 认证/握手失败: %w", err)
	}
	client := ssh.NewClient(sshConn, channels, requests)
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("SFTP 初始化失败: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	success = true
	return &logSFTP{Client: sftpClient, conn: conn, stop: stop, ioTimeout: 60 * time.Second}, nil
}
