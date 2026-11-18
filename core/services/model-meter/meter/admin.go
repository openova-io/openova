// The admin listener of the meter. It is served on its own address (ADMIN_ADDR), never on the
// proxy's, and offers four routes:
//
//	GET /healthz                      200 while the process runs
//	GET /readyz                       200 when the spool directory takes a new file, else 503
//	GET /metrics                      the five metric families, Prometheus text format
//	GET /internal/events/{requestId}  the ring entry of the request, or 404
//
// The metrics count requests and tokens by the identity headers. They never hold the text of a
// request or of an answer.

package meter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

// Metrics counts the requests and the tokens the meter has seen.
type Metrics struct {
	mu       sync.Mutex
	requests map[[4]string]int64 // organization, alias, backend, status
	tokens   map[[4]string]int64 // organization, alias, principal, class
}

// NewMetrics returns empty counters.
func NewMetrics() *Metrics {
	return &Metrics{requests: map[[4]string]int64{}, tokens: map[[4]string]int64{}}
}

// observe counts one request, and its tokens when it ended ok with a usage block. The token
// classes are those of the usage event: input, cache_read, cache_write and output.
func (m *Metrics) observe(rec Record, u Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[[4]string{rec.Organization, rec.Alias, rec.Backend, rec.Status}]++
	if rec.Status != StatusOK || !u.Complete {
		return
	}
	for _, ms := range MeasuresFrom(u) {
		if class, ok := strings.CutPrefix(ms.Meter, "llm.tokens."); ok {
			m.tokens[[4]string{rec.Organization, rec.Alias, rec.Principal, class}] += ms.Quantity
		}
	}
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// writeFamily writes one counter family, its series sorted by their labels.
func writeFamily(w io.Writer, name, help string, labels [4]string, series map[[4]string]int64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	keys := make([][4]string, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return strings.Join(keys[i][:], "\x00") < strings.Join(keys[j][:], "\x00") })
	for _, k := range keys {
		pairs := make([]string, 4)
		for i := range labels {
			pairs[i] = labels[i] + `="` + labelEscaper.Replace(k[i]) + `"`
		}
		_, _ = fmt.Fprintf(w, "%s{%s} %d\n", name, strings.Join(pairs, ","), series[k])
	}
}

func writeCounter(w io.Writer, name, help string, v int64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
}

// Ready reports whether the spool directory takes a new file: it creates one and removes it.
// The drain reads only files named *.json, so it never reads this one.
func (p *SpoolPublisher) Ready() error {
	if p.SpoolDir == "" {
		return errors.New("meter: no spool directory configured")
	}
	f, err := os.CreateTemp(p.SpoolDir, ".readyz-*")
	if err != nil {
		return err
	}
	closeErr := f.Close()
	if err := os.Remove(f.Name()); err != nil {
		return err
	}
	return closeErr
}

// NewAdmin returns the handler of the admin listener for the proxy h and its publisher p.
func NewAdmin(h *Handler, p *SpoolPublisher) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if err := p.Ready(); err != nil {
			h.log.Warn("meter: not ready, the spool directory does not take a file", "error", err)
			http.Error(w, "the spool directory is not writable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		h.metrics.mu.Lock()
		writeFamily(w, "model_meter_requests_total", "Requests by outcome.",
			[4]string{"organization", "alias", "backend", "status"}, h.metrics.requests)
		writeFamily(w, "model_meter_tokens_total", "Tokens of the requests that ended ok, by class.",
			[4]string{"organization", "alias", "principal", "class"}, h.metrics.tokens)
		h.metrics.mu.Unlock()
		writeCounter(w, "model_meter_events_published_total", "Usage events acknowledged by the bus, at once or from the spool.",
			p.publishedOK.Load()+p.spoolDrained.Load())
		writeCounter(w, "model_meter_events_spooled_total", "Usage events written to the spool directory.", p.spooled.Load())
		writeCounter(w, "model_meter_events_dropped_total", "Usage events lost.", p.spoolDropFatal.Load())
	})
	mux.HandleFunc("GET /internal/events/{requestId}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := h.ring.Get(r.PathValue("requestId"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no request with this identifier among the last requests")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rec)
	})
	return mux
}
