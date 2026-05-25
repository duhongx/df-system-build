package handler

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"df-build-server/internal/docker"
	"df-build-server/internal/k8s"
	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type SettingsHandler struct {
	repo *repository.SettingsRepo
}

func NewSettingsHandler() *SettingsHandler {
	return &SettingsHandler{repo: repository.NewSettingsRepo()}
}

func (h *SettingsHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/settings")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.GetAll)
		g.PUT("", h.Update)
		g.POST("/test-registry", h.TestRegistry)
		g.POST("/test-connection", h.TestConnection)
		g.GET("/k8s-namespaces", h.GetK8sNamespaces)
		g.GET("/read-kubeconfig", h.ReadKubeconfig)
	}
}

func (h *SettingsHandler) GetAll(c *gin.Context) {
	list, err := h.repo.GetAll()
	if err != nil {
		response.Fail(c, 10901, "获取设置失败")
		return
	}
	// Convert to map for easier frontend consumption
	result := make(map[string]string)
	for _, s := range list {
		result[s.Key] = s.Value
	}
	response.OK(c, result)
}

func (h *SettingsHandler) Update(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10902, "参数错误")
		return
	}
	if err := h.repo.BatchUpdate(req); err != nil {
		response.Fail(c, 10902, "保存失败")
		return
	}
	response.OKWithMessage(c, "设置已保存", nil)
}

// TestRegistry tests Docker registry connectivity and credentials
func (h *SettingsHandler) TestRegistry(c *gin.Context) {
	registryURL, _ := h.repo.GetByKey("docker_registry_url")
	registryUser, _ := h.repo.GetByKey("docker_registry_user")
	registryPass, _ := h.repo.GetByKey("docker_registry_password")

	if registryURL == "" {
		response.Fail(c, 10903, "镜像仓库地址未配置")
		return
	}

	// Test 1: Check if registry is reachable (HTTP v2 API)
	checkURL := fmt.Sprintf("http://%s/v2/", registryURL)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(checkURL)
	if err != nil {
		response.Fail(c, 10903, fmt.Sprintf("仓库地址不可访问: %v", err))
		return
	}
	resp.Body.Close()

	// Test 2: If credentials provided, try docker login via SDK
	if registryUser != "" && registryPass != "" {
		dockerCli, err := docker.NewClient("")
		if err != nil {
			response.Fail(c, 10903, fmt.Sprintf("Docker 不可用: %v", err))
			return
		}
		if err := dockerCli.Login(c.Request.Context(), registryURL, registryUser, registryPass); err != nil {
			response.Fail(c, 10903, fmt.Sprintf("凭据验证失败: %v", err))
			return
		}
	}

	response.OKWithMessage(c, "连接测试成功", nil)
}

// GetK8sNamespaces returns available K8s namespaces
func (h *SettingsHandler) GetK8sNamespaces(c *gin.Context) {
	names, err := k8s.ListNamespaces(c.Request.Context())
	if err != nil {
		response.Fail(c, 10904, fmt.Sprintf("获取命名空间失败: %v", err))
		return
	}
	response.OK(c, names)
}

// ReadKubeconfig reads a kubeconfig file from disk and returns its content
func (h *SettingsHandler) ReadKubeconfig(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		response.Fail(c, 10906, "path 参数不能为空")
		return
	}

	// Security check: path must end with "config" or contain "kube"
	if !strings.HasSuffix(path, "config") && !strings.Contains(path, "kube") {
		response.Fail(c, 10906, "不允许读取该路径，路径必须包含 'kube' 或以 'config' 结尾")
		return
	}

	content, err := os.ReadFile(path)
	if err != nil {
		response.Fail(c, 10906, fmt.Sprintf("读取文件失败: %v", err))
		return
	}

	response.OK(c, gin.H{"content": string(content)})
}

// TestConnection tests connectivity for various services
func (h *SettingsHandler) TestConnection(c *gin.Context) {
	var req struct {
		Type   string            `json:"type" binding:"required"`
		Config map[string]string `json:"config" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10905, "参数错误")
		return
	}

	var success bool
	var message string

	switch req.Type {
	case "registry":
		success, message = h.testRegistry(req.Config)
	case "k8s":
		success, message = h.testK8s(req.Config)
	case "nacos":
		success, message = h.testNacos(req.Config)
	case "skywalking":
		success, message = h.testSkyWalking(req.Config)
	case "postgresql":
		success, message = h.testPostgreSQL(req.Config)
	default:
		response.Fail(c, 10905, "不支持的测试类型: "+req.Type)
		return
	}

	if success {
		response.OK(c, gin.H{"success": true, "message": message})
	} else {
		response.Fail(c, 10905, message)
	}
}

func (h *SettingsHandler) testRegistry(config map[string]string) (bool, string) {
	registryURL := config["docker_registry_url"]
	if registryURL == "" {
		return false, "镜像仓库地址未填写"
	}

	// Strip http:// or https:// prefix if present
	registryURL = strings.TrimPrefix(registryURL, "http://")
	registryURL = strings.TrimPrefix(registryURL, "https://")
	registryURL = strings.TrimRight(registryURL, "/")

	checkURL := fmt.Sprintf("http://%s/v2/", registryURL)
	httpClient := &http.Client{Timeout: 5 * time.Second}

	req, _ := http.NewRequest("GET", checkURL, nil)
	user := config["docker_registry_user"]
	pass := config["docker_registry_password"]
	if user != "" && pass != "" {
		req.SetBasicAuth(user, pass)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("仓库地址不可访问: %v", err)
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case 200:
		return true, "连接成功，认证通过"
	case 401:
		if user != "" {
			return false, "认证失败：用户名或密码错误"
		}
		return false, "需要认证：请填写用户名和密码"
	case 404:
		return false, "该地址不是有效的 Docker Registry（/v2/ 路径不存在）"
	case 403:
		return false, "访问被拒绝：检查仓库权限配置"
	case 502, 503, 504:
		return false, fmt.Sprintf("仓库网关错误（HTTP %d）", resp.StatusCode)
	default:
		return false, fmt.Sprintf("意外的状态码 %d，请检查仓库地址", resp.StatusCode)
	}
}

func (h *SettingsHandler) testK8s(_ map[string]string) (bool, string) {
	_, err := k8s.ListNamespaces(nil)
	if err != nil {
		return false, fmt.Sprintf("K8s 连接失败: %v", err)
	}
	return true, "K8s 连接成功"
}

func (h *SettingsHandler) testNacos(config map[string]string) (bool, string) {
	nacosURL := config["nacos_url"]
	if nacosURL == "" {
		return false, "Nacos 地址未填写"
	}

	// Auto-add http:// prefix if missing
	if !strings.HasPrefix(nacosURL, "http://") && !strings.HasPrefix(nacosURL, "https://") {
		nacosURL = "http://" + nacosURL
	}

	// POST to /v1/auth/login
	loginURL := strings.TrimRight(nacosURL, "/") + "/v1/auth/login"
	user := config["nacos_user"]
	pass := config["nacos_password"]

	httpClient := &http.Client{Timeout: 5 * time.Second}
	form := url.Values{}
	form.Set("username", user)
	form.Set("password", pass)

	resp, err := httpClient.PostForm(loginURL, form)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "first path segment in URL cannot contain colon") {
			return false, "Nacos 地址格式错误：缺少 http:// 前缀"
		}
		if strings.Contains(errStr, "no such host") {
			return false, "Nacos 主机不存在，请检查地址"
		}
		if strings.Contains(errStr, "connection refused") {
			return false, "Nacos 连接被拒绝（端口未开放或服务未启动）"
		}
		if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded") {
			return false, "Nacos 连接超时（5 秒）"
		}
		return false, fmt.Sprintf("Nacos 不可访问: %v", err)
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case 200:
		return true, "Nacos 连接成功，认证通过"
	case 403:
		return false, "Nacos 认证失败：用户名或密码错误"
	case 404:
		return false, "Nacos 路径错误：该地址不是有效的 Nacos 服务"
	default:
		return false, fmt.Sprintf("Nacos 返回异常状态码 %d", resp.StatusCode)
	}
}

func (h *SettingsHandler) testSkyWalking(config map[string]string) (bool, string) {
	oapURL := config["skywalking_oap_url"]
	if oapURL == "" {
		return false, "SkyWalking OAP 地址未填写"
	}

	// Strip http:// prefix if present (TCP dial requires host:port only)
	oapURL = strings.TrimPrefix(oapURL, "http://")
	oapURL = strings.TrimPrefix(oapURL, "https://")
	oapURL = strings.TrimRight(oapURL, "/")

	// Take first address if comma-separated
	if idx := strings.Index(oapURL, ","); idx > 0 {
		oapURL = oapURL[:idx]
	}

	// TCP dial to check if port is open
	conn, err := net.DialTimeout("tcp", oapURL, 5*time.Second)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "no such host") {
			return false, "SkyWalking 主机不存在，请检查地址"
		}
		if strings.Contains(errStr, "connection refused") {
			return false, "SkyWalking 端口未开放或服务未启动"
		}
		if strings.Contains(errStr, "timeout") {
			return false, "SkyWalking 连接超时（5 秒）"
		}
		return false, fmt.Sprintf("SkyWalking OAP 不可达: %v", err)
	}
	conn.Close()
	return true, "SkyWalking OAP 端口可达"
}

func (h *SettingsHandler) testPostgreSQL(config map[string]string) (bool, string) {
	host := config["postgresql_host"]
	port := config["postgresql_port"]
	user := config["postgresql_user"]
	password := config["postgresql_password"]
	database := config["postgresql_database"]

	if host == "" {
		return false, "PostgreSQL 主机地址未填写"
	}
	if port == "" {
		port = "5432"
	}
	if database == "" {
		database = "postgres"
	}

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		host, port, user, password, database)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return false, fmt.Sprintf("PostgreSQL 连接失败: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return false, fmt.Sprintf("PostgreSQL 连接失败: %v", err)
	}
	return true, "PostgreSQL 连接成功"
}
