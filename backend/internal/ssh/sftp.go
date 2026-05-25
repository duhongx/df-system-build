package ssh

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"df-build-server/pkg/logger"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

// SFTPClient wraps SFTP operations
type SFTPClient struct {
	client *sftp.Client
}

// NewSFTPClient creates an SFTP client from an existing SSH connection
func NewSFTPClient(conn *gossh.Client) (*SFTPClient, error) {
	client, err := sftp.NewClient(conn)
	if err != nil {
		return nil, fmt.Errorf("SFTP 客户端创建失败: %w", err)
	}
	return &SFTPClient{client: client}, nil
}

// Upload copies a local file to the remote path
func (s *SFTPClient) Upload(localPath, remotePath string) error {
	// Open local file
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer localFile.Close()

	// Ensure remote directory exists
	remoteDir := filepath.Dir(remotePath)
	s.client.MkdirAll(remoteDir)

	// Create remote file
	remoteFile, err := s.client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("创建远端文件失败: %w", err)
	}
	defer remoteFile.Close()

	// Copy
	bytes, err := io.Copy(remoteFile, localFile)
	if err != nil {
		return fmt.Errorf("文件传输失败: %w", err)
	}

	logger.Log.Infof("SFTP uploaded %s → %s (%d bytes)", localPath, remotePath, bytes)
	return nil
}

// Close closes the SFTP client
func (s *SFTPClient) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}
