package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/middleware"
	"df-build-server/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

type K8sWatchHandler struct{}

func NewK8sWatchHandler() *K8sWatchHandler {
	return &K8sWatchHandler{}
}

func (h *K8sWatchHandler) RegisterRoutes(r *gin.RouterGroup) {
	// WebSocket endpoint - auth via query param
	g := r.Group("/kubernetes")
	{
		g.GET("/watch", h.Watch)
	}
}

// Watch streams K8s resource changes via WebSocket
func (h *K8sWatchHandler) Watch(c *gin.Context) {
	// Auth via query param (WebSocket)
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

	namespace := c.DefaultQuery("namespace", k8s.GetDefaultNamespace())
	resource := c.DefaultQuery("resource", "deployments") // deployments / pods

	// Upgrade to WebSocket
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Log.Errorf("K8s Watch WebSocket upgrade failed: %v", err)
		return
	}
	defer ws.Close()

	cs, err := k8s.GetClient()
	if err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	// Start watch
	var watcher watch.Interface
	switch resource {
	case "deployments":
		watcher, err = cs.AppsV1().Deployments(namespace).Watch(c.Request.Context(), metav1.ListOptions{})
	case "pods":
		watcher, err = cs.CoreV1().Pods(namespace).Watch(c.Request.Context(), metav1.ListOptions{})
	default:
		ws.WriteMessage(websocket.TextMessage, []byte(`{"error":"不支持的资源类型"}`))
		return
	}

	if err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"error":"Watch 启动失败: %s"}`, err.Error())))
		return
	}
	defer watcher.Stop()

	// Read from WebSocket (for close detection)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, _, err := ws.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	// Stream events to WebSocket
	for {
		select {
		case <-done:
			return
		case event, ok := <-watcher.ResultChan():
			if !ok {
				// Watch channel closed, reconnect
				ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"RECONNECT"}`))
				return
			}

			msg := gin.H{
				"type":      string(event.Type),
				"resource":  resource,
				"timestamp": time.Now().Format(time.RFC3339),
			}

			// Extract key info based on resource type
			if event.Object != nil {
				objJSON, _ := json.Marshal(event.Object)
				msg["object"] = json.RawMessage(objJSON)
			}

			data, _ := json.Marshal(msg)
			if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}
