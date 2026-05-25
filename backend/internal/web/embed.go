package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// StaticFS holds the embedded frontend dist files.
// This will be populated at build time when dist/ exists.
// During development (no dist/), the FS will be empty and the SPA handler won't serve anything.
//
//go:embed all:dist
var StaticFS embed.FS

// RegisterSPA registers the SPA static file handler on the gin engine.
// It serves frontend assets and falls back to index.html for Vue Router history mode.
func RegisterSPA(r *gin.Engine) {
	// Get the sub-filesystem rooted at "dist"
	distFS, err := fs.Sub(StaticFS, "dist")
	if err != nil {
		// No dist directory embedded (dev mode) — skip
		return
	}

	fileServer := http.FileServer(http.FS(distFS))

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// Skip API routes — let them return proper 404 JSON
		if strings.HasPrefix(path, "/api/") || path == "/health" {
			c.JSON(http.StatusNotFound, gin.H{"code": 40400, "message": "接口不存在"})
			return
		}

		// Try to serve the static file directly
		// Check if the file exists in the embedded FS
		filePath := strings.TrimPrefix(path, "/")
		if filePath == "" {
			filePath = "index.html"
		}

		if f, err := distFS.Open(filePath); err == nil {
			f.Close()
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		// File not found in dist — serve index.html (SPA fallback)
		// This enables Vue Router history mode
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
