package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"df-build-server/internal/deploy/engine/offline"
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

// RegisterOfflineRoutes mounts the offline-bundle endpoints. Called from the
// main handler registration.
func (h *Handler) registerOffline(g *gin.RouterGroup) {
	g.GET("/offline/status", h.OfflineStatus)
	g.POST("/offline/upload", h.OfflineUpload)
	g.POST("/offline/install", h.OfflineInstall)
}

// OfflineStatus returns the currently installed bundle metadata plus a live
// scan summary of the resource directory.
func (h *Handler) OfflineStatus(c *gin.Context) {
	cur, err := h.svc.OfflineRepo().GetCurrent(c.Request.Context())
	if err != nil {
		response.Fail(c, 5001, err.Error())
		return
	}
	summary := offline.ScanResourceDirs(h.svc.ResourceDir())
	response.OK(c, gin.H{"current": cur, "resourceDir": h.svc.ResourceDir(), "scan": summary})
}

// OfflineUpload accepts a multipart bundle upload into the staging directory.
func (h *Handler) OfflineUpload(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		response.Fail(c, 4001, "缺少上传文件 (form field: file)")
		return
	}
	if err := os.MkdirAll(h.svc.UploadDir(), 0o755); err != nil {
		response.Fail(c, 5001, "创建上传目录失败: "+err.Error())
		return
	}
	// Sanitize the filename to prevent path traversal.
	name := filepath.Base(file.Filename)
	if name == "" || name == "." || name == ".." {
		response.Fail(c, 4001, "非法文件名")
		return
	}
	dst := filepath.Join(h.svc.UploadDir(), fmt.Sprintf("%d-%s", time.Now().Unix(), name))
	if err := c.SaveUploadedFile(file, dst); err != nil {
		response.Fail(c, 5001, "保存上传文件失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"path": dst, "size": file.Size})
}

// OfflineInstall verifies and installs an offline bundle (atomic swap) from a
// previously uploaded path or an allow-listed server-local path.
func (h *Handler) OfflineInstall(c *gin.Context) {
	var body struct {
		Path          string `json:"path"`
		BundleVersion string `json:"bundleVersion"`
		Clean         bool   `json:"clean"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Fail(c, 4001, "请求体格式错误")
		return
	}
	pkgPath, err := h.resolveInstallPath(body.Path)
	if err != nil {
		response.Fail(c, 4001, err.Error())
		return
	}

	report, err := offline.Install(pkgPath, h.svc.ResourceDir(), offline.Options{
		Clean:                 body.Clean,
		ExpectedBundleVersion: body.BundleVersion,
	})
	if err != nil {
		response.FailWithStatus(c, 422, 4220, "离线包安装失败: "+err.Error())
		return
	}

	summary := offline.ScanResourceDirs(h.svc.ResourceDir())
	fileCount := 0
	for _, n := range summary {
		fileCount += n
	}
	user := middleware.GetCurrentUserID(c)
	_ = user
	bundle := &model.OfflineBundle{
		BundleVersion: report.BundleVersion,
		FileCount:     fileCount,
		InstalledBy:   currentUsername(c),
		InstalledAt:   report.ExtractedAt,
	}
	if err := h.svc.OfflineRepo().RecordInstall(c.Request.Context(), bundle); err != nil {
		// Install already succeeded; metadata failure is non-fatal.
		response.OK(c, gin.H{"bundleVersion": report.BundleVersion, "fileCount": fileCount, "warn": "metadata not recorded: " + err.Error()})
		return
	}
	response.OK(c, gin.H{"bundleVersion": report.BundleVersion, "fileCount": fileCount})
}

// resolveInstallPath validates the requested package path against the allow
// list: it must be under the upload staging dir or the configured resource
// parent. Symlinks are resolved to block escapes.
func (h *Handler) resolveInstallPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("缺少安装包路径")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("解析路径失败: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("路径不存在或不可访问: %w", err)
	}
	allowed := []string{h.svc.UploadDir()}
	for _, root := range allowed {
		rootAbs, _ := filepath.Abs(root)
		if rootResolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
			rootAbs = rootResolved
		}
		if resolved == rootAbs || strings.HasPrefix(resolved, rootAbs+string(os.PathSeparator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("路径不在允许范围内（仅允许上传暂存目录内的离线包）")
}

func currentUsername(c *gin.Context) string {
	if v, ok := c.Get("username"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
