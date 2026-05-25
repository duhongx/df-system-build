package handler

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/crypto"
	"df-build-server/pkg/logger"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type WebSFTPHandler struct {
	serverRepo *repository.ServerMgmtRepo
	logRepo    *repository.ServerLogRepo
}

func NewWebSFTPHandler() *WebSFTPHandler {
	return &WebSFTPHandler{
		serverRepo: repository.NewServerMgmtRepo(),
		logRepo:    repository.NewServerLogRepo(),
	}
}

func (h *WebSFTPHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/server-mgmt")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/:id/sftp/list", h.ListFiles)
		g.POST("/:id/sftp/mkdir", h.Mkdir)
		g.POST("/:id/sftp/delete", h.Delete)
		g.POST("/:id/sftp/rename", h.Rename)
		g.POST("/:id/sftp/upload", h.Upload)
		g.GET("/:id/sftp/download", h.Download)
	}
}

type fileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

func (h *WebSFTPHandler) ListFiles(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	path := c.DefaultQuery("path", "/")

	sftpClient, sshClient, err := h.connectSFTP(uint(id))
	if err != nil {
		response.Fail(c, 12101, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	entries, err := sftpClient.ReadDir(path)
	if err != nil {
		response.Fail(c, 12101, "读取目录失败: "+err.Error())
		return
	}

	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		files = append(files, fileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			Size:    entry.Size(),
			IsDir:   entry.IsDir(),
			Mode:    entry.Mode().String(),
			ModTime: entry.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	response.OK(c, gin.H{"path": path, "files": files})
}

func (h *WebSFTPHandler) Mkdir(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12102, "参数错误")
		return
	}

	sftpClient, sshClient, err := h.connectSFTP(uint(id))
	if err != nil {
		response.Fail(c, 12102, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	if err := sftpClient.MkdirAll(req.Path); err != nil {
		response.Fail(c, 12102, "创建目录失败: "+err.Error())
		return
	}

	h.logOp(c, uint(id), "sftp", fmt.Sprintf("创建目录: %s", req.Path))
	response.OKWithMessage(c, "目录已创建", nil)
}

func (h *WebSFTPHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12103, "参数错误")
		return
	}

	sftpClient, sshClient, err := h.connectSFTP(uint(id))
	if err != nil {
		response.Fail(c, 12103, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	// Check if it's a directory
	info, err := sftpClient.Stat(req.Path)
	if err != nil {
		response.Fail(c, 12103, "文件不存在: "+err.Error())
		return
	}

	if info.IsDir() {
		// Remove directory recursively using SSH command (sftp doesn't support recursive delete)
		session, _ := sshClient.NewSession()
		if session != nil {
			session.Run(fmt.Sprintf("rm -rf %s", req.Path))
			session.Close()
		}
	} else {
		if err := sftpClient.Remove(req.Path); err != nil {
			response.Fail(c, 12103, "删除失败: "+err.Error())
			return
		}
	}

	h.logOp(c, uint(id), "sftp", fmt.Sprintf("删除: %s", req.Path))
	response.OKWithMessage(c, "删除成功", nil)
}

func (h *WebSFTPHandler) Rename(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		OldPath string `json:"oldPath" binding:"required"`
		NewPath string `json:"newPath" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12104, "参数错误")
		return
	}

	sftpClient, sshClient, err := h.connectSFTP(uint(id))
	if err != nil {
		response.Fail(c, 12104, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	if err := sftpClient.Rename(req.OldPath, req.NewPath); err != nil {
		response.Fail(c, 12104, "重命名失败: "+err.Error())
		return
	}

	h.logOp(c, uint(id), "sftp", fmt.Sprintf("重命名: %s → %s", req.OldPath, req.NewPath))
	response.OKWithMessage(c, "重命名成功", nil)
}

func (h *WebSFTPHandler) Upload(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	remotePath := c.PostForm("path")
	if remotePath == "" {
		remotePath = "/"
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.Fail(c, 12105, "文件上传失败")
		return
	}
	defer file.Close()

	sftpClient, sshClient, err := h.connectSFTP(uint(id))
	if err != nil {
		response.Fail(c, 12105, "SFTP 连接失败: "+err.Error())
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	destPath := filepath.Join(remotePath, header.Filename)
	remoteFile, err := sftpClient.Create(destPath)
	if err != nil {
		response.Fail(c, 12105, "创建远程文件失败: "+err.Error())
		return
	}
	defer remoteFile.Close()

	written, err := io.Copy(remoteFile, file)
	if err != nil {
		response.Fail(c, 12105, "写入远程文件失败: "+err.Error())
		return
	}

	h.logOp(c, uint(id), "sftp", fmt.Sprintf("上传文件: %s (%d bytes)", destPath, written))
	response.OKWithMessage(c, "上传成功", gin.H{"path": destPath, "size": written})
}

func (h *WebSFTPHandler) Download(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	remotePath := c.Query("path")
	if remotePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path 参数必填"})
		return
	}

	sftpClient, sshClient, err := h.connectSFTP(uint(id))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "SFTP 连接失败"})
		return
	}
	defer sftpClient.Close()
	defer sshClient.Close()

	remoteFile, err := sftpClient.Open(remotePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	defer remoteFile.Close()

	stat, _ := remoteFile.Stat()
	fileName := filepath.Base(remotePath)

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
	c.Header("Content-Type", "application/octet-stream")
	if stat != nil {
		c.Header("Content-Length", strconv.FormatInt(stat.Size(), 10))
	}

	io.Copy(c.Writer, remoteFile)

	h.logOp(c, uint(id), "sftp", fmt.Sprintf("下载文件: %s", remotePath))
}

func (h *WebSFTPHandler) connectSFTP(serverID uint) (*sftp.Client, *ssh.Client, error) {
	server, err := h.serverRepo.FindByID(serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("服务器不存在")
	}

	credential := ""
	if server.CredentialEncrypted != "" {
		credential, _ = crypto.Decrypt(server.CredentialEncrypted)
	}

	var authMethod ssh.AuthMethod
	switch server.AuthType {
	case "password":
		authMethod = ssh.Password(credential)
	case "certificate":
		passphrase := ""
		if server.CertPassphrase != "" {
			passphrase, _ = crypto.Decrypt(server.CertPassphrase)
		}
		var signer ssh.Signer
		if passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(credential), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(credential))
		}
		if err != nil {
			return nil, nil, fmt.Errorf("SSH Key 解析失败: %w", err)
		}
		authMethod = ssh.PublicKeys(signer)
	default:
		return nil, nil, fmt.Errorf("不支持的认证方式")
	}

	timeout := time.Duration(server.ConnTimeout) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	config := &ssh.ClientConfig{
		User:            server.Username,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}

	addr := fmt.Sprintf("%s:%d", server.Host, server.Port)
	sshClient, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, nil, fmt.Errorf("SSH 连接失败: %w", err)
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("SFTP 初始化失败: %w", err)
	}

	return sftpClient, sshClient, nil
}

func (h *WebSFTPHandler) logOp(c *gin.Context, serverID uint, logType, content string) {
	h.logRepo.Create(&model.ServerLog{
		ServerID:  serverID,
		Type:      logType,
		Operator:  middleware.GetCurrentUsername(c),
		Content:   content,
		ClientIP:  c.ClientIP(),
		CreatedAt: time.Now(),
	})
	logger.Log.Infof("[SFTP] server=%d op=%s", serverID, content)
}
