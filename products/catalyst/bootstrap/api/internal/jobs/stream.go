// stream.go — GET /api/v1/deployments/{depId}/jobs/stream.
//
// The stream pushes a hint each time the jobs store writes for the
// deployment, so a reader refetches instead of polling. Response
// text/event-stream. One frame per Change:
//
//	event: jobs
//	data: {"deploymentId":"…","jobId":"…","executionId":"…","kind":"job-upserted","seq":41}
//
// and a comment line ": ping" every 20 seconds. kind is job-upserted,
// execution-started, execution-finished, log-appended or resync. resync
// means "refetch everything for this deployment". There is no replay: a
// client that reconnects refetches once. Errors: 401 from the session
// middleware of the route group; 400 missing-depId; the responses of the
// access check (503, 404).
package jobs

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// StreamAccess is the access check of the stream, injected by the caller
// so this package does not import the handler package. It writes the error
// response itself and returns ok=false, or returns the store to stream
// from.
type StreamAccess func(w http.ResponseWriter, r *http.Request, deploymentID string) (*Store, bool)

// streamPing is the interval of the ": ping" comment line. A variable so a
// test can shorten it.
var streamPing = 20 * time.Second

// streamFrame is the data of one "jobs" event.
type streamFrame struct {
	DeploymentID string     `json:"deploymentId"`
	JobID        string     `json:"jobId"`
	ExecutionID  string     `json:"executionId"`
	Kind         ChangeKind `json:"kind"`
	Seq          uint64     `json:"seq"`
}

// StreamHandler serves GET /api/v1/deployments/{depId}/jobs/stream.
func StreamHandler(access StreamAccess) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		depID := strings.TrimSpace(chi.URLParam(r, "depId"))
		if depID == "" {
			writeStreamError(w, http.StatusBadRequest, "missing-depId")
			return
		}
		st, ok := access(w, r, depID)
		if !ok {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeStreamError(w, http.StatusInternalServerError, "streaming-unsupported")
			return
		}
		changes, cancel := st.Subscribe(depID)
		defer cancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ping := time.NewTicker(streamPing)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case c, open := <-changes:
				if !open {
					return
				}
				data, err := json.Marshal(streamFrame{
					DeploymentID: c.DeploymentID,
					JobID:        c.JobID,
					ExecutionID:  c.ExecutionID,
					Kind:         c.Kind,
					Seq:          c.Seq,
				})
				if err != nil {
					return
				}
				if _, err := w.Write([]byte("event: jobs\ndata: " + string(data) + "\n\n")); err != nil {
					return
				}
				flusher.Flush()
			case <-ping.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// writeStreamError writes the {"error": code} body the API uses.
func writeStreamError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
