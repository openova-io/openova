package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Change is one change of the incidents of an Organization, sent on the stream as the event
// "incident". Kind is the action of a person, "rules" for a decision of the rule path, or
// "judgement" for an outcome of a judge, which names the judgement instead of an incident.
type Change struct {
	Organization string `json:"organization"`
	IncidentID   string `json:"incidentId,omitempty"`
	JudgementID  string `json:"judgementId,omitempty"`
	Kind         string `json:"kind"`
}

// subscriberBuffer is how many changes a subscriber may fall behind before it is dropped. A
// dropped subscriber's stream ends; the console connects again and reads the incidents anew.
const subscriberBuffer = 64

// Hub hands every change to the streams that may see it. It lives in one process: the service
// runs one replica, as its dispatcher requires.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan Change]string // the Organization each stream may see; empty sees every one
	closed bool
}

// NewHub returns a hub without streams.
func NewHub() *Hub { return &Hub{subs: map[chan Change]string{}} }

// Close ends every stream, and every stream opened later at once. The program calls it when
// it stops, because a stream is never idle.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// Publish hands c to every stream of its Organization and to every stream that sees all. It
// never waits: a stream that is full is dropped.
func (h *Hub) Publish(c Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, org := range h.subs {
		if org != "" && org != c.Organization {
			continue
		}
		select {
		case ch <- c:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

func (h *Hub) subscribe(org string) chan Change {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Change, subscriberBuffer)
	if h.closed {
		close(ch)
		return ch
	}
	h.subs[ch] = org
	return ch
}

func (h *Hub) unsubscribe(ch chan Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}

// stream sends the changes the caller may see as server-sent events, each flushed when it is
// written, until the client goes away or the stream is dropped.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, c caller) {
	flusher, ok := w.(http.Flusher)
	if !ok || s.Hub == nil {
		fail(w, http.StatusInternalServerError, "stream_unavailable", "this server cannot stream")
		return
	}
	ch := s.Hub.subscribe(c.org)
	defer s.Hub.unsubscribe(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": ready\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case change, open := <-ch:
			if !open {
				return
			}
			data, err := json.Marshal(change)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: incident\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
