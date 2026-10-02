package engine

// RunEvent kinds published to subscribers (design §10 SSE): node status
// transitions, node log lines and run status transitions.
const (
	EventNode = "node"
	EventLog  = "log"
	EventRun  = "run"
)

// eventBuffer bounds one subscriber's queue; a slower reader than the engine
// drops overflow — events are progress hints, never engine backpressure
// (design §10: 慢订阅者丢弃).
const eventBuffer = 16

// RunEvent is one run-progress event on a run's subscription stream. Node
// events carry NodeID + the new Status; log events carry NodeID + the raw
// Data chunk; run events carry Status only.
type RunEvent struct {
	Type   string `json:"type"`
	NodeID string `json:"nodeId,omitempty"`
	Status string `json:"status,omitempty"`
	Data   string `json:"data,omitempty"`
}

// Subscribe registers a buffered channel receiving the run's events until
// the returned cleanup runs; cleanup is idempotent and safe to defer.
func (e *Engine) Subscribe(runID uint) (<-chan RunEvent, func()) {
	ch := make(chan RunEvent, eventBuffer)
	e.subMu.Lock()
	defer e.subMu.Unlock()
	if e.subs == nil {
		e.subs = map[uint]map[chan RunEvent]struct{}{}
	}
	m, ok := e.subs[runID]
	if !ok {
		m = map[chan RunEvent]struct{}{}
		e.subs[runID] = m
	}
	m[ch] = struct{}{}
	return ch, func() { e.unsubscribe(runID, ch) }
}

// unsubscribe removes one subscriber and closes its channel; the run's
// subscriber set is deleted once empty so finished runs leave nothing behind.
func (e *Engine) unsubscribe(runID uint, ch chan RunEvent) {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	m, ok := e.subs[runID]
	if !ok {
		return
	}
	if _, ok := m[ch]; !ok {
		return
	}
	delete(m, ch)
	close(ch)
	if len(m) == 0 {
		delete(e.subs, runID)
	}
}

// publish fans one event out to the run's subscribers; full queues drop the
// event instead of blocking the engine.
func (e *Engine) publish(runID uint, ev RunEvent) {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	for ch := range e.subs[runID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// publishNode reports one node status transition to the run's subscribers.
func (e *Engine) publishNode(runID uint, nodeID, status string) {
	e.publish(runID, RunEvent{Type: EventNode, NodeID: nodeID, Status: status})
}

// publishRun reports one run status transition to the run's subscribers.
func (e *Engine) publishRun(runID uint, status string) {
	e.publish(runID, RunEvent{Type: EventRun, Status: status})
}
