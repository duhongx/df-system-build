package handler

import (
	"fmt"
	"io"

	"df-build-server/internal/middleware"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

// RunEvents streams a deployment run's logs over SSE. It replays all persisted
// log lines first (afterSeq=0), then forwards live events from the hub until
// the run reaches a terminal state or the client disconnects.
//
// Auth: the EventSource API cannot set headers, so the JWT is accepted from the
// `token` query parameter (falling back to the Authorization header for
// non-browser clients).
func (h *Handler) RunEvents(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		if hdr := c.GetHeader("Authorization"); len(hdr) > 7 && hdr[:7] == "Bearer " {
			token = hdr[7:]
		}
	}
	if token == "" {
		response.Unauthorized(c, "请先登录")
		return
	}
	if _, err := middleware.ParseToken(token); err != nil {
		response.Unauthorized(c, "登录已过期，请重新登录")
		return
	}

	id, ok := parseID(c)
	if !ok {
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	hub := h.svc.Runtime().Hub()
	ch, _, cancel := hub.Subscribe(c.Request.Context(), id, 0)
	defer cancel()

	c.Stream(func(w io.Writer) bool {
		select {
		case entry, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "event: done\ndata: {}\n\n")
				return false
			}
			data, _ := jsonMarshal(entry)
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})
}
