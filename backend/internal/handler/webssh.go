package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/crypto"
	"df-build-server/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type WebSSHHandler struct {
	serverRepo *repository.ServerMgmtRepo
	logRepo    *repository.ServerLogRepo
}

func NewWebSSHHandler() *WebSSHHandler {
	return &WebSSHHandler{
		serverRepo: repository.NewServerMgmtRepo(),
		logRepo:    repository.NewServerLogRepo(),
	}
}

func (h *WebSSHHandler) RegisterRoutes(r *gin.RouterGroup) {
	// WebSocket endpoint handles auth via query param (WS doesn't support custom headers)
	g := r.Group("/server-mgmt")
	{
		g.GET("/:id/terminal", h.Terminal)
	}
}

// Terminal handles WebSocket connection for SSH terminal
func (h *WebSSHHandler) Terminal(c *gin.Context) {
	// Authenticate via query param (WebSocket can't send custom headers)
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未提供 token"})
		return
	}
	claims, err := middleware.ParseToken(token)
	if err != nil || claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token 无效"})
		return
	}
	username := claims.Username

	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	server, err := h.serverRepo.FindByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "服务器不存在"})
		return
	}

	// Upgrade to WebSocket
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Log.Errorf("WebSocket upgrade failed: %v", err)
		return
	}
	defer ws.Close()

	// Connect SSH
	sshClient, err := h.connectSSH(server)
	if err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\033[31mSSH 连接失败: %v\033[0m\r\n", err)))
		return
	}
	defer sshClient.Close()

	// Create SSH session with PTY
	session, err := sshClient.NewSession()
	if err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\033[31m创建 session 失败: %v\033[0m\r\n", err)))
		return
	}
	defer session.Close()

	// Request PTY
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", 40, 120, modes); err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\033[31mPTY 请求失败: %v\033[0m\r\n", err)))
		return
	}

	// Get stdin/stdout pipes
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return
	}
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return
	}
	session.Stderr = session.Stdout

	// Start shell
	if err := session.Shell(); err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\033[31mShell 启动失败: %v\033[0m\r\n", err)))
		return
	}

	operator := username
	clientIP := c.ClientIP()

	// Log connection
	h.logRepo.Create(&model.ServerLog{
		ServerID:  server.ID,
		Type:      "ssh",
		Operator:  operator,
		Content:   fmt.Sprintf("连接终端 %s@%s:%d", server.Username, server.Host, server.Port),
		ClientIP:  clientIP,
		CreatedAt: time.Now(),
	})

	// Read from SSH stdout → write to WebSocket
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, err := stdoutPipe.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				data := buf[:n]
				if !utf8.Valid(data) {
					data = []byte(string(data))
				}
				if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
			}
		}
	}()

	// Read from WebSocket → write to SSH stdin
	// Protocol: plain text = terminal input, JSON with type=resize = window resize
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				session.Close()
				return
			}
			if len(msg) > 0 {
				// Check if it's a resize message (JSON)
				if msg[0] == '{' {
					var resizeMsg struct {
						Type string `json:"type"`
						Cols int    `json:"cols"`
						Rows int    `json:"rows"`
					}
					if err := json.Unmarshal(msg, &resizeMsg); err == nil && resizeMsg.Type == "resize" {
						if resizeMsg.Cols > 0 && resizeMsg.Rows > 0 {
							session.WindowChange(resizeMsg.Rows, resizeMsg.Cols)
						}
						continue
					}
				}
				stdinPipe.Write(msg)
			}
		}
	}()

	// Wait for session to end
	session.Wait()
	<-done

	// Log disconnection
	h.logRepo.Create(&model.ServerLog{
		ServerID:  server.ID,
		Type:      "ssh",
		Operator:  operator,
		Content:   "断开终端连接",
		ClientIP:  clientIP,
		CreatedAt: time.Now(),
	})
}

func (h *WebSSHHandler) connectSSH(server *model.Server) (*ssh.Client, error) {
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
		var err error
		if passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(credential), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(credential))
		}
		if err != nil {
			return nil, fmt.Errorf("SSH Key 解析失败: %w", err)
		}
		authMethod = ssh.PublicKeys(signer)
	default:
		return nil, fmt.Errorf("不支持的认证方式: %s", server.AuthType)
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
	return ssh.Dial("tcp", addr, config)
}
