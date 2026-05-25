package handler

import (
	"bufio"
	"fmt"
	"io"
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	sshclient "df-build-server/internal/ssh"
	"df-build-server/pkg/logger"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"

	"context"
)

type RemoteHandler struct {
	serverRepo *repository.ServerRepo
}

func NewRemoteHandler() *RemoteHandler {
	return &RemoteHandler{serverRepo: repository.NewServerRepo()}
}

func (h *RemoteHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/remote")
	g.Use(middleware.AuthRequired())
	{
		g.POST("/sync", h.Sync)
		g.POST("/package", h.Package)
	}
}

type syncReq struct {
	SourcePath      string `json:"sourcePath" binding:"required"`
	DestPath        string `json:"destPath" binding:"required"`
	TargetServerIDs []uint `json:"targetServerIds" binding:"required"`
	ExcludePatterns string `json:"excludePatterns"`
}

func (h *RemoteHandler) Sync(c *gin.Context) {
	var req syncReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10401, "参数错误")
		return
	}

	servers, err := h.serverRepo.FindByIDs(req.TargetServerIDs)
	if err != nil || len(servers) == 0 {
		response.Fail(c, 10401, "目标服务器不存在")
		return
	}

	// SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	c.Stream(func(w io.Writer) bool {
		for _, server := range servers {
			// Build rsync command
			excludes := "--exclude=version.json --exclude=*.sql"
			if req.ExcludePatterns != "" {
				excludes = ""
				for _, p := range splitPatterns(req.ExcludePatterns) {
					excludes += fmt.Sprintf("--exclude=%s ", p)
				}
			}
			cmd := fmt.Sprintf("rsync -avz %s %s %s", excludes, req.SourcePath, req.DestPath)

			fmt.Fprintf(w, "data: [%s] 执行: %s\n\n", server.Name, cmd)

			// Connect via SSH and execute
			client, err := sshclient.Connect(&server)
			if err != nil {
				fmt.Fprintf(w, "data: [%s] SSH 连接失败: %v\n\n", server.Name, err)
				continue
			}

			ctx := context.Background()
			reader, err := client.ExecStream(ctx, cmd)
			if err != nil {
				fmt.Fprintf(w, "data: [%s] 命令执行失败: %v\n\n", server.Name, err)
				client.Close()
				continue
			}

			// Stream output
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				fmt.Fprintf(w, "data: [%s] %s\n\n", server.Name, scanner.Text())
			}

			client.Close()
			fmt.Fprintf(w, "data: [%s] 同步完成 ✓\n\n", server.Name)
			logger.Log.Infof("Remote sync completed on %s", server.Name)
		}
		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		return false
	})
}

type packageReq struct {
	RemoteDir      string `json:"remoteDir" binding:"required"`
	TargetServerID uint   `json:"targetServerId" binding:"required"`
}

func (h *RemoteHandler) Package(c *gin.Context) {
	var req packageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10402, "参数错误")
		return
	}

	server, err := h.serverRepo.FindByID(req.TargetServerID)
	if err != nil {
		response.Fail(c, 10402, "目标服务器不存在")
		return
	}

	// SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	c.Stream(func(w io.Writer) bool {
		cmd := fmt.Sprintf("tar -czvf %s.tar.gz -C $(dirname %s) $(basename %s)",
			req.RemoteDir, req.RemoteDir, req.RemoteDir)

		fmt.Fprintf(w, "data: [%s] 执行: %s\n\n", server.Name, cmd)

		// Connect via SSH
		client, err := sshclient.Connect(server)
		if err != nil {
			fmt.Fprintf(w, "data: [%s] SSH 连接失败: %v\n\n", server.Name, err)
			fmt.Fprintf(w, "event: done\ndata: {}\n\n")
			return false
		}
		defer client.Close()

		ctx := context.Background()
		reader, err := client.ExecStream(ctx, cmd)
		if err != nil {
			fmt.Fprintf(w, "data: [%s] 命令执行失败: %v\n\n", server.Name, err)
			fmt.Fprintf(w, "event: done\ndata: {}\n\n")
			return false
		}

		// Stream output (tar -v outputs filenames)
		scanner := bufio.NewScanner(reader)
		lineCount := 0
		for scanner.Scan() {
			lineCount++
			// Only show every 10th line for large archives to avoid flooding
			if lineCount <= 20 || lineCount%10 == 0 {
				fmt.Fprintf(w, "data: [%s] %s\n\n", server.Name, scanner.Text())
			}
		}

		if lineCount > 20 {
			fmt.Fprintf(w, "data: [%s] ... 共 %d 个文件\n\n", server.Name, lineCount)
		}

		fmt.Fprintf(w, "data: [%s] 打包完成 ✓ → %s.tar.gz\n\n", server.Name, req.RemoteDir)
		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		logger.Log.Infof("Remote package completed on %s: %s", server.Name, req.RemoteDir)
		return false
	})
}

func splitPatterns(s string) []string {
	parts := make([]string, 0)
	for _, part := range stringsSplit(s, ",") {
		trimmed := trimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return []string{s}
	}
	return parts
}

func splitBy(s, sep string) []string {
	return stringsSplit(s, sep)
}

func stringsSplit(s, sep string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			result = append(result, s[start:i])
			start = i + len(sep)
		}
	}
	result = append(result, s[start:])
	return result
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// Ensure strconv is used (for potential future use)
var _ = strconv.Itoa
