package handler

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/crypto"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
)

type ServerMgmtHandler struct {
	repo    *repository.ServerMgmtRepo
	logRepo *repository.ServerLogRepo
}

func NewServerMgmtHandler() *ServerMgmtHandler {
	return &ServerMgmtHandler{
		repo:    repository.NewServerMgmtRepo(),
		logRepo: repository.NewServerLogRepo(),
	}
}

func (h *ServerMgmtHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/server-mgmt")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.GET("/:id", h.Get)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.POST("/:id/test", h.TestConnection)
		g.GET("/:id/logs", h.GetLogs)
	}
}

func (h *ServerMgmtHandler) List(c *gin.Context) {
	search := c.Query("search")
	servers, err := h.repo.List(search)
	if err != nil {
		response.Fail(c, 12001, "获取服务器列表失败")
		return
	}
	response.OK(c, servers)
}

func (h *ServerMgmtHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	server, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 12002, "服务器不存在")
		return
	}
	response.OK(c, server)
}

type serverMgmtReq struct {
	Host              string `json:"host" binding:"required"`
	Remark            string `json:"remark"`
	Port              int    `json:"port"`
	Username          string `json:"username" binding:"required"`
	AuthType          string `json:"authType" binding:"required"`
	Credential        string `json:"credential"`
	CertPassphrase    string `json:"certPassphrase"`
	ConnTimeout       int    `json:"connTimeout"`
	ForbiddenCommands string `json:"forbiddenCommands"`
	SortOrder         int    `json:"sortOrder"`
}

func (h *ServerMgmtHandler) Create(c *gin.Context) {
	var req serverMgmtReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12003, "参数错误")
		return
	}

	server := &model.Server{
		Host:              req.Host,
		Remark:            req.Remark,
		Port:              req.Port,
		Username:          req.Username,
		AuthType:          req.AuthType,
		ConnTimeout:       req.ConnTimeout,
		ForbiddenCommands: req.ForbiddenCommands,
		SortOrder:         req.SortOrder,
		Status:            "unknown",
		CreatedBy:         middleware.GetCurrentUsername(c),
	}
	if server.Port == 0 {
		server.Port = 22
	}
	if server.ConnTimeout == 0 {
		server.ConnTimeout = 10
	}

	// Encrypt credential
	if req.Credential != "" {
		encrypted, err := crypto.Encrypt(req.Credential)
		if err != nil {
			response.Fail(c, 12003, "凭据加密失败")
			return
		}
		server.CredentialEncrypted = encrypted
	}
	if req.CertPassphrase != "" {
		encrypted, err := crypto.Encrypt(req.CertPassphrase)
		if err != nil {
			response.Fail(c, 12003, "证书密码加密失败")
			return
		}
		server.CertPassphrase = encrypted
	}

	if err := h.repo.Create(server); err != nil {
		response.Fail(c, 12003, "创建失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "创建成功", server)
}

func (h *ServerMgmtHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	server, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 12004, "服务器不存在")
		return
	}

	var req serverMgmtReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12004, "参数错误")
		return
	}

	server.Host = req.Host
	server.Remark = req.Remark
	server.Port = req.Port
	server.Username = req.Username
	server.AuthType = req.AuthType
	server.ConnTimeout = req.ConnTimeout
	server.ForbiddenCommands = req.ForbiddenCommands
	server.SortOrder = req.SortOrder

	if server.Port == 0 {
		server.Port = 22
	}

	// Update credential only if provided
	if req.Credential != "" {
		encrypted, _ := crypto.Encrypt(req.Credential)
		server.CredentialEncrypted = encrypted
	}
	if req.CertPassphrase != "" {
		encrypted, _ := crypto.Encrypt(req.CertPassphrase)
		server.CertPassphrase = encrypted
	}

	if err := h.repo.Update(server); err != nil {
		response.Fail(c, 12004, "更新失败")
		return
	}
	response.OKWithMessage(c, "更新成功", server)
}

func (h *ServerMgmtHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.repo.Delete(uint(id)); err != nil {
		response.Fail(c, 12005, "删除失败")
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

func (h *ServerMgmtHandler) TestConnection(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	server, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 12006, "服务器不存在")
		return
	}

	// Decrypt credential
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
			response.Fail(c, 12006, "SSH Key 解析失败: "+err.Error())
			return
		}
		authMethod = ssh.PublicKeys(signer)
	default:
		response.Fail(c, 12006, "不支持的认证方式")
		return
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	_ = ctx // used for potential future cancellation
	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		server.Status = "offline"
		h.repo.Update(server)
		response.Fail(c, 12006, "连接失败: "+err.Error())
		return
	}
	conn.Close()

	now := time.Now()
	server.Status = "online"
	server.LastConnTime = &now
	h.repo.Update(server)

	response.OKWithMessage(c, "连接成功", nil)
}

func (h *ServerMgmtHandler) GetLogs(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	logType := c.Query("type")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "50"))

	logs, total, err := h.logRepo.ListByServer(uint(id), logType, page, pageSize)
	if err != nil {
		response.Fail(c, 12007, "获取日志失败")
		return
	}
	response.OKWithPage(c, logs, total, page, pageSize)
}
