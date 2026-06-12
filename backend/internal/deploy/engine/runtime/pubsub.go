package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"df-build-server/internal/deploy/engine"
	"df-build-server/internal/deploy/engine/store"
)

// Hub broadcasts deployment log entries to live SSE subscribers while
// persisting them to the underlying store. Each deployment owns one
// channel; subscribers join by deploymentID.
//
// Lifecycle per deployment:
//
//   - Logger() registers an internal channel and returns a engine.Logger
//     adapter that the deploy engine writes to.
//   - Subscribe() returns a chan LogEntry. The hub replays history
//     starting at afterSeq (exclusive) before forwarding new entries.
//   - Close() broadcasts a "done" signal (the returned closed channel
//     mirrors http.ResponseWriter behaviour: a closed channel = stream
//     ended), then drops all subscribers. Any future Subscribe call
//     returns a closed channel immediately so the SSE handler can write
//     event: done and disconnect.
//
// Hub is safe for concurrent use.
type Hub struct {
	store store.Store

	mu       sync.Mutex
	channels map[int64]*depChannel
}

type depChannel struct {
	mu     sync.Mutex
	subs   map[chan LogEntry]*subState
	closed bool
	final  string // final status: "success" / "failed" / "canceled"
}

// subState tracks per-subscriber delivery health. When a publish drops a
// frame because the subscriber is too slow, lostSince records the seq of
// the first dropped entry; the next successful publish replaces the
// payload with a synthetic gap notice so the downstream SSE handler can
// reconnect with ?after=lostSince.
type subState struct {
	lostSince  int64
	lastSeq    int64
	dropCount  int
	notifyOnce bool
}

// NewHub wires the hub to a store. The store is consulted for replay; if
// nil, replay is skipped.
func NewHub(st store.Store) *Hub {
	return &Hub{store: st, channels: map[int64]*depChannel{}}
}

// Logger returns a engine.Logger adapter that persists every event and
// fans it out to live subscribers. Component is used as a default when
// the action result does not carry one.
func (h *Hub) Logger(deploymentID int64, defaultComponent string) engine.Logger {
	h.ensure(deploymentID)
	return &hubLogger{
		hub:              h,
		deploymentID:     deploymentID,
		defaultComponent: defaultComponent,
	}
}

// Publish writes a log entry verbatim, both to the store and to live
// subscribers. Used by Executor for synthetic events such as "组件开始" /
// "rollback triggered" that don't come from the engine.
func (h *Hub) Publish(ctx context.Context, deploymentID int64, entry LogEntry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if h.store != nil {
		row := &store.DeploymentLog{
			DeploymentID: deploymentID,
			Sequence:     entry.Sequence, // 0 = let store assign next
			Timestamp:    entry.Timestamp,
			Component:    entry.Component,
			Host:         entry.Host,
			Phase:        entry.Phase,
			ActionName:   entry.Action,
			ActionType:   entry.Type,
			Status:       entry.Status,
			Detail:       entry.Detail,
			IsError:      entry.IsError,
		}
		// AppendDeploymentLog mutates Sequence in-place when 0.
		if err := h.store.AppendDeploymentLog(ctx, row); err == nil {
			entry.Sequence = row.Sequence
			entry.Timestamp = row.Timestamp
			entry.Detail = row.Detail
		}
	}
	h.broadcast(deploymentID, entry)
}

// Subscribe returns a channel that receives every entry with sequence
// greater than afterSeq. History is replayed first, then live entries
// flow in until Close is called. The returned cleanup func unsubscribes
// the channel; SSE handlers should defer it.
//
// If the deployment has already finished by the time we subscribe, the
// returned channel is drained of history and then closed immediately so
// the SSE handler can emit `event: done` and exit.
func (h *Hub) Subscribe(ctx context.Context, deploymentID int64, afterSeq int64) (<-chan LogEntry, string, func()) {
	ch := make(chan LogEntry, 64)
	dc := h.ensure(deploymentID)

	// Replay history. We do this before registering the channel so that
	// any concurrent broadcast sees a registered subscriber and we don't
	// drop in-flight events. To avoid duplicates we register first,
	// snapshot lastReplayedSeq, then drop the lock and replay.
	dc.mu.Lock()
	if dc.closed {
		// already finished — replay & close
		final := dc.final
		dc.mu.Unlock()
		go func() {
			h.replay(ctx, deploymentID, afterSeq, ch)
			close(ch)
		}()
		return ch, final, func() {}
	}
	dc.subs[ch] = &subState{lastSeq: afterSeq}
	dc.mu.Unlock()

	cleanup := func() {
		dc.mu.Lock()
		delete(dc.subs, ch)
		dc.mu.Unlock()
	}

	// Replay async to avoid blocking the caller; entries published while
	// we replay will queue on the channel and be delivered after the
	// historical ones (sequence numbers preserve ordering anyway).
	go h.replay(ctx, deploymentID, afterSeq, ch)
	return ch, "", cleanup
}

// Close marks the deployment finished, broadcasts the final status, and
// closes all subscriber channels. Idempotent. Always allocates a
// depChannel so a later Subscribe call can read the closed/final state
// even when no Logger or Publish ran beforehand (common in tests).
func (h *Hub) Close(deploymentID int64, finalStatus string) {
	dc := h.ensure(deploymentID)
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.closed {
		return
	}
	dc.closed = true
	dc.final = finalStatus
	for sub := range dc.subs {
		close(sub)
	}
	dc.subs = map[chan LogEntry]*subState{}
}

// ----- internals -----

func (h *Hub) ensure(deploymentID int64) *depChannel {
	h.mu.Lock()
	defer h.mu.Unlock()
	dc, ok := h.channels[deploymentID]
	if !ok {
		dc = &depChannel{subs: map[chan LogEntry]*subState{}}
		h.channels[deploymentID] = dc
	}
	return dc
}

func (h *Hub) broadcast(deploymentID int64, entry LogEntry) {
	h.mu.Lock()
	dc, ok := h.channels[deploymentID]
	h.mu.Unlock()
	if !ok {
		return
	}
	// Hold dc.mu for the entire fan-out. Previously we snapshotted
	// (ch, state) pairs and released the lock before sending — which
	// races with Close(): if Close runs between snapshot and send,
	// it closes the channels we still hold references to and the
	// next `p.ch <- entry` panics with "send on closed channel".
	// Holding the lock is fine because every send uses select+default
	// (non-blocking) so a slow subscriber can't stall the publisher.
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.closed {
		return
	}
	for ch, state := range dc.subs {
		// If we already dropped frames for this subscriber, swap the
		// payload for a gap marker so the downstream SSE handler can
		// surface it to the browser. The first successful send after
		// dropping carries the marker; subsequent sends resume normal
		// delivery so the consumer doesn't miss the live tail.
		if state != nil && state.lostSince > 0 && !state.notifyOnce {
			gap := LogEntry{
				Sequence:  entry.Sequence,
				Timestamp: entry.Timestamp,
				Component: entry.Component,
				Action:    "日志断流提醒",
				Type:      "gap",
				Status:    "warning",
				Detail: fmt.Sprintf("订阅速度跟不上,丢失 %d 条记录,请使用 ?after=%d 重新拉取",
					state.dropCount, state.lostSince-1),
				IsError: true,
			}
			select {
			case ch <- gap:
				state.notifyOnce = true
			default:
				// still slow — fall through, will retry next time
			}
		}
		select {
		case ch <- entry:
			if state != nil {
				state.lastSeq = entry.Sequence
				if state.notifyOnce {
					// Successfully delivered the gap marker; clear
					// state for future drops.
					state.lostSince = 0
					state.dropCount = 0
					state.notifyOnce = false
				}
			}
		default:
			// Slow consumer: record the first dropped sequence so
			// the gap marker can advise the client what to refetch.
			if state != nil {
				if state.lostSince == 0 {
					state.lostSince = entry.Sequence
				}
				state.dropCount++
			}
		}
	}
}

// replay reads stored entries with sequence > afterSeq and pushes them
// into ch. Stops on context cancellation or store errors (silent — the
// SSE handler will eventually time out).
func (h *Hub) replay(ctx context.Context, deploymentID int64, afterSeq int64, ch chan<- LogEntry) {
	if h.store == nil {
		return
	}
	const pageSize = 200
	cursor := afterSeq
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		page, err := h.store.ListDeploymentLogs(ctx, deploymentID, cursor, pageSize)
		if err != nil || len(page) == 0 {
			return
		}
		for _, l := range page {
			select {
			case <-ctx.Done():
				return
			case ch <- EntryFromStore(l):
			}
			cursor = l.Sequence
		}
		if len(page) < pageSize {
			return
		}
	}
}

// hubLogger satisfies engine.Logger by translating engine-level events
// into LogEntry rows. Each TaskStart and ActionResult is persisted +
// broadcast.
type hubLogger struct {
	hub              *Hub
	deploymentID     int64
	defaultComponent string
}

func (l *hubLogger) TaskStart(ctx engine.TaskContext) {
	component := ctx.Component
	if component == "" {
		component = l.defaultComponent
	}
	l.hub.Publish(context.Background(), l.deploymentID, LogEntry{
		Timestamp: time.Now().UTC(),
		Component: component,
		Host:      ctx.HostName,
		Phase:     ctx.Phase,
		Action:    ctx.TaskName,
		Type:      "task_start",
		Status:    "running",
		Detail:    "开始执行任务 " + nonEmpty(ctx.TaskName, ctx.TaskID),
	})
}

func (l *hubLogger) ActionResult(result engine.ActionResult) {
	component := result.Context.Component
	if component == "" {
		component = l.defaultComponent
	}
	// Prefer the structured failure detail (reason+detail+suggestion
	// from *DeployError) over the action target so the UI surfaces
	// the root cause. Falls back to Target when Detail is empty (the
	// success path or a non-DeployError failure).
	uiDetail := result.Detail
	if uiDetail == "" {
		uiDetail = result.Target
	}
	l.hub.Publish(context.Background(), l.deploymentID, LogEntry{
		Timestamp: time.Now().UTC(),
		Component: component,
		Host:      result.Context.HostName,
		Phase:     result.Context.Phase,
		Action:    result.Action,
		Type:      "action_result",
		Status:    result.Status,
		Detail:    uiDetail,
		IsError:   isErrorStatus(result.Status),
	})
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "-"
}

func isErrorStatus(status string) bool {
	switch status {
	case "失败", "failed":
		return true
	}
	return false
}
