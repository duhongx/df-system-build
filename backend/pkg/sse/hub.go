package sse

import (
	"sync"

	"df-build-server/pkg/logger"
)

type Event struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type Hub struct {
	mu          sync.RWMutex
	channels    map[uint][]chan Event
	droppedEvts map[uint]int64
}

var DefaultHub = NewHub()

func NewHub() *Hub {
	return &Hub{
		channels:    make(map[uint][]chan Event),
		droppedEvts: make(map[uint]int64),
	}
}

func (h *Hub) Subscribe(pipelineID uint) chan Event {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan Event, 1024) // generous buffer
	h.channels[pipelineID] = append(h.channels[pipelineID], ch)
	return ch
}

func (h *Hub) Unsubscribe(pipelineID uint, ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	channels := h.channels[pipelineID]
	for i, c := range channels {
		if c == ch {
			h.channels[pipelineID] = append(channels[:i], channels[i+1:]...)
			close(ch)
			break
		}
	}
}

func (h *Hub) Publish(pipelineID uint, event Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, ch := range h.channels[pipelineID] {
		select {
		case ch <- event:
		default:
			// Channel full = slow consumer. Track and warn every 100 drops.
			h.mu.RUnlock()
			h.mu.Lock()
			h.droppedEvts[pipelineID]++
			dropped := h.droppedEvts[pipelineID]
			h.mu.Unlock()
			h.mu.RLock()
			if dropped%100 == 1 {
				logger.Log.Warnf("SSE backpressure: pipeline %d dropped %d events (slow consumer)", pipelineID, dropped)
			}
		}
	}
}

func (h *Hub) Close(pipelineID uint) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, ch := range h.channels[pipelineID] {
		close(ch)
	}
	delete(h.channels, pipelineID)
	delete(h.droppedEvts, pipelineID)
}

// DroppedCount returns the dropped event count for a pipeline (for metrics)
func (h *Hub) DroppedCount(pipelineID uint) int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.droppedEvts[pipelineID]
}
