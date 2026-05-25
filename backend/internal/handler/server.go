package handler

import (
	"os"
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/crypto"
	"df-build-server/pkg/response"
	sshclient "df-build-server/internal/ssh"

	"github.com/gin-gonic/gin"
)

type ServerHandler struct {
	repo *repository.ServerRepo
}

func NewServerHandler() *ServerHandler {
	return &ServerHandler{repo: repository.NewServerRepo()}
}

func (h *ServerHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/servers")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.POST("/:id/test", h.TestConnection)
		g.GET("/read-key", h.ReadServerKey)
	}
}

type serverResponse struct {
	model.RemoteServer
	Credential string `json:"credential"`
}

func (h *ServerHandler) List(c *gin.Context) {
	list, err := h.repo.List()
	if err != nil {
		response.Fail(c, 10701, "获取服务器列表失败")
		return
	}
	// Mask credentials
	for i := range list {
		if list[i].CredentialEncrypted != "" {
			list[i].CredentialEncrypted = ""
		}
	}
	response.OK(c, list)
}

type serverReq struct {
	Name       string `json:"name" binding:"required"`
	Host       string `json:"host" binding:"required"`
	Port       int    `json:"port"`
	Username   string `json:"username" binding:"required"`
	AuthType   string `json:"authType" binding:"required"`
	Credential string `json:"credential"`
}

func (h *ServerHandler) Create(c *gin.Context) {
	var req serverReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10702, "参数错误")
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}

	s := &model.RemoteServer{
		Name: req.Name, Host: req.Host, Port: req.Port,
		Username: req.Username, AuthType: req.AuthType, Status: "offline",
	}

	// Encrypt credential
	if req.Credential != "" {
		encrypted, err := crypto.Encrypt(req.Credential)
		if err != nil {
			response.Fail(c, 10702, "凭据加密失败")
			return
		}
		s.CredentialEncrypted = encrypted
	}

	if err := h.repo.Create(s); err != nil {
		response.Fail(c, 10702, "创建失败: "+err.Error())
		return
	}
	s.CredentialEncrypted = "" // mask in response
	response.OKWithMessage(c, "创建成功", s)
}

func (h *ServerHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req serverReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10703, "参数错误")
		return
	}

	s, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10703, "服务器不存在")
		return
	}

	s.Name = req.Name
	s.Host = req.Host
	if req.Port > 0 {
		s.Port = req.Port
	}
	s.Username = req.Username
	s.AuthType = req.AuthType

	if req.Credential != "" {
		encrypted, _ := crypto.Encrypt(req.Credential)
		s.CredentialEncrypted = encrypted
	}

	h.repo.Update(s)
	s.CredentialEncrypted = ""
	response.OKWithMessage(c, "更新成功", s)
}

func (h *ServerHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.repo.Delete(uint(id)); err != nil {
		response.Fail(c, 10704, "删除失败")
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

func (h *ServerHandler) TestConnection(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	server, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10705, "服务器不存在")
		return
	}

	// Use the ssh package to test connection
	if err := sshclient.TestConnection(server); err != nil {
		// Update status to offline
		server.Status = "offline"
		h.repo.Update(server)
		response.Fail(c, 10705, "连接失败: "+err.Error())
		return
	}

	// Update status to online
	server.Status = "online"
	h.repo.Update(server)
	response.OKWithMessage(c, "连接测试成功", nil)
}

// ReadServerKey reads SSH private key from the server's filesystem
func (h *ServerHandler) ReadServerKey(c *gin.Context) {
	// Read from the local server's SSH key (this runs on the build server itself)
	keyPaths := []string{"/root/.ssh/id_rsa", "/root/.ssh/id_ed25519"}

	for _, path := range keyPaths {
		content, err := os.ReadFile(path)
		if err == nil && len(content) > 0 {
			response.OK(c, gin.H{
				"path":    path,
				"content": string(content),
			})
			return
		}
	}

	response.Fail(c, 10706, "未找到 SSH 私钥文件（已检查 /root/.ssh/id_rsa 和 /root/.ssh/id_ed25519）")
}
