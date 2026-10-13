package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/funcx27/kite/modules/workflow/internal/engine"
	"github.com/funcx27/kite/modules/workflow/internal/store"
)

// heartbeatInterval paces the SSE keep-alive comment lines (design §10).
const heartbeatInterval = 15 * time.Second

type sseNodeEvent struct {
	NodeID string `json:"nodeId"`
	Status string `json:"status"`
}

type sseLogEvent struct {
	NodeID string `json:"nodeId"`
	Data   string `json:"data"`
}

type sseRunEvent struct {
	Status string `json:"status"`
}

type sseDoneEvent struct {
	Status string `json:"status"`
}

type sseErrorEvent struct {
	Error string `json:"error"`
}

// runTerminal reports whether a run status ends the event stream.
func runTerminal(status string) bool {
	switch status {
	case store.RunSucceeded, store.RunFailed, store.RunCancelled:
		return true
	}
	return false
}

// RunEvents streams one run's live progress as SSE (design §10): the
// persisted snapshot first — run and node states at connect time, so a
// reconnect aligns with the DB — then engine events with 15s ping
// keep-alives; a terminal run ends the stream after the done event.
// Authorization matches the run lifecycle actions (exec, design §8).
func (h *Handler) RunEvents(c *gin.Context) {
	runID, _, _, ok := h.runIDAndWorkflow(c)
	if !ok {
		return
	}
	if !h.authorize(c, "exec", c.GetString(clusterNameContextKey), "") {
		return
	}
	var run store.WorkflowRun
	var nodes []store.NodeRun
	flusher, ok := startSSE(c)
	if !ok {
		return
	}

	// Subscribing before the snapshot reads keeps the window between the two
	// event-free — a transition landing there would otherwise never reach the
	// client and a terminal run would strand the stream open. The cost is
	// that a queued event may replay one transition the snapshot already
	// covers; both sides carry only persisted state, so clients settle on the
	// next fetch either way.
	events, stop := h.engine.Subscribe(runID)
	defer stop()

	if err := h.store.DB().First(&run, runID).Error; err != nil {
		writeSSE(c, flusher, "error", sseErrorEvent{Error: err.Error()})
		return
	}
	if err := h.store.DB().Where("run_id = ?", runID).Order("id").Find(&nodes).Error; err != nil {
		writeSSE(c, flusher, "error", sseErrorEvent{Error: err.Error()})
		return
	}
	writeSSE(c, flusher, "run", sseRunEvent{Status: run.Status})
	for _, n := range nodes {
		writeSSE(c, flusher, "node", sseNodeEvent{NodeID: n.NodeID, Status: n.Status})
	}
	if runTerminal(run.Status) {
		writeSSE(c, flusher, "done", sseDoneEvent{Status: run.Status})
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			// Event overflow drops can swallow the terminal run event; the
			// tick rechecks the persisted status so the stream always ends.
			var status string
			if err := h.store.DB().Model(&store.WorkflowRun{}).
				Where("id = ?", runID).Select("status").Scan(&status).Error; err == nil && runTerminal(status) {
				writeSSE(c, flusher, "run", sseRunEvent{Status: status})
				writeSSE(c, flusher, "done", sseDoneEvent{Status: status})
				return
			}
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !h.writeRunEvent(c, flusher, ev) {
				return
			}
		}
	}
}

// writeRunEvent maps one engine event onto its SSE frame; a run-side terminal
// status additionally ends the stream after the done event. False means the
// client is gone.
func (h *Handler) writeRunEvent(c *gin.Context, flusher http.Flusher, ev engine.RunEvent) bool {
	switch ev.Type {
	case engine.EventLog:
		return writeSSE(c, flusher, "log", sseLogEvent{NodeID: ev.NodeID, Data: ev.Data})
	case engine.EventNode:
		return writeSSE(c, flusher, "node", sseNodeEvent{NodeID: ev.NodeID, Status: ev.Status})
	case engine.EventRun:
		if !writeSSE(c, flusher, "run", sseRunEvent{Status: ev.Status}) {
			return false
		}
		if runTerminal(ev.Status) {
			writeSSE(c, flusher, "done", sseDoneEvent{Status: ev.Status})
			return false
		}
		return true
	}
	return true
}

// startSSE sets the streaming response headers, same shape as the buildfleet
// facade, so proxies do not buffer the stream.
func startSSE(c *gin.Context) (http.Flusher, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return nil, false
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	return flusher, true
}

// writeSSE writes one event frame and flushes; false means the client is gone.
func writeSSE(c *gin.Context, flusher http.Flusher, event string, payload any) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		slog.Error("workflow: marshal sse event", "event", event, "err", err)
		return false
	}
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
