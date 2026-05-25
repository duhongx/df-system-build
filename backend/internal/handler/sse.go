package handler

import (
	"fmt"
	"io"
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/pkg/sse"

	"github.com/gin-gonic/gin"
)

type SSEHandler struct {
	hub *sse.Hub
}

func NewSSEHandler() *SSEHandler {
	return &SSEHandler{hub: sse.DefaultHub}
}

func (h *SSEHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/pipelines")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/:id/logs/stream", h.StreamLogs)
	}
}

func (h *SSEHandler) StreamLogs(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	pipelineID := uint(id)

	// Set SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Subscribe to pipeline events
	ch := h.hub.Subscribe(pipelineID)
	defer h.hub.Unsubscribe(pipelineID, ch)

	// Stream events
	c.Stream(func(w io.Writer) bool {
		select {
		case event, ok := <-ch:
			if !ok {
				// Channel closed = pipeline done
				fmt.Fprintf(w, "event: done\ndata: {}\n\n")
				return false
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, event.Data)
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})
}
