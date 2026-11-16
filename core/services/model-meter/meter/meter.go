// Package meter is the model meter: a reverse proxy between the model gateway and the model
// back ends that publishes one usage event per answered request.
//
// The gateway verifies the caller's credential and sends the request here with the identity
// headers of identity.go. The meter routes the request by its Host header to a back end of
// BACKENDS, writes the response through to the gateway, reads the usage block of the response,
// and publishes catalyst.usage.recorded.v1 with quantities and no price
// (docs/contracts/model-contract.md, sections 3 and 4).
//
// Behaviour:
//
//  1. A request whose path does not start with /v1/ gets 404. A request whose Host is not a
//     key of BACKENDS gets 421.
//  2. A request without one of the identity headers gets 400 missing_identity and is not
//     forwarded. No usage event is published for it.
//  3. The response is written through as it arrives. Usage is read from an unstreamed JSON
//     body of at most MaxBodyBytes. A streamed response (text/event-stream) is passed through
//     and publishes no event.
//  4. At most one usage event is published per forwarded request: exactly one when the back
//     end answered with a 2xx status and a usage block was read, none otherwise. It is
//     published after the response has been written.
//  5. Every request, whatever its outcome, produces one JSON log line and one entry in the
//     ring of the last RingSize requests.
package meter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxBodyBytes is the largest unstreamed response body the meter reads usage from. A larger
// body is passed through and publishes no event.
const MaxBodyBytes = 8 << 20

// RingSize is the number of requests the ring keeps.
const RingSize = 1000

// The values of Record.Status.
const (
	StatusOK       = "ok"
	StatusError    = "error"
	StatusAborted  = "aborted"
	StatusRejected = "rejected"
)

// Backend is one entry of the BACKENDS document.
type Backend struct {
	Name string `json:"backend"`
	URL  string `json:"url"`
}

// ParseBackends reads the BACKENDS document: a JSON object from a Host header name to
// {"backend": "<name>", "url": "<base URL>"}. It needs at least one entry, and every entry
// needs a name and an http or https URL with a host.
func ParseBackends(doc string) (map[string]Backend, error) {
	var m map[string]Backend
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		return nil, fmt.Errorf("meter: BACKENDS is not a JSON object of back ends: %w", err)
	}
	if len(m) == 0 {
		return nil, errors.New("meter: BACKENDS has no entry")
	}
	for host, b := range m {
		if host == "" || b.Name == "" {
			return nil, fmt.Errorf("meter: BACKENDS entry %q needs a host and a back-end name", host)
		}
		u, err := url.Parse(b.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("meter: BACKENDS entry %q: %q is not an http or https URL", host, b.URL)
		}
	}
	return m, nil
}

// Record is what the meter keeps about one request: one entry of the ring, and the fields of
// the log line.
type Record struct {
	RequestID     string          `json:"requestId"`
	Organization  string          `json:"organization"`
	PrincipalKind string          `json:"principalKind"`
	Principal     string          `json:"principal"`
	Alias         string          `json:"alias"`
	Backend       string          `json:"backend"`
	Stream        bool            `json:"stream"`
	Status        string          `json:"status"`
	HTTPStatus    int             `json:"httpStatus"`
	Published     bool            `json:"published"`
	Event         json.RawMessage `json:"event"`
}

// Ring keeps the records of the last requests, oldest first out.
type Ring struct {
	mu      sync.Mutex
	entries []Record
	next    int
	size    int
}

// NewRing returns a ring that keeps size records.
func NewRing(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{size: size}
}

// Add keeps rec, and drops the oldest record when the ring is full.
func (r *Ring) Add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) < r.size {
		r.entries = append(r.entries, rec)
		return
	}
	r.entries[r.next] = rec
	r.next = (r.next + 1) % r.size
}

// Get returns the newest record of the request identifier.
func (r *Ring) Get(requestID string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.entries)
	for k := 1; k <= n; k++ {
		i := ((r.next-k)%n + n) % n
		if r.entries[i].RequestID == requestID {
			return r.entries[i], true
		}
	}
	return Record{}, false
}

// Config is what NewHandler needs.
type Config struct {
	Backends  map[string]Backend
	Publisher Publisher
	Source    string       // the event source, model-gateway/<sovereign-fqdn>/<region-id>
	Logger    *slog.Logger // nil: slog.Default()
	Ring      *Ring        // nil: a ring of RingSize
	Now       func() time.Time
	NewID     func() (string, error) // nil: NewEventID
}

type route struct {
	name  string
	proxy *httputil.ReverseProxy
}

// Handler is the proxy. Build it with NewHandler.
type Handler struct {
	routes map[string]route
	pub    Publisher
	source string
	log    *slog.Logger
	ring   *Ring
	now    func() time.Time
	newID  func() (string, error)
}

// exchange is what the proxy hooks learn about one forwarded request.
type exchange struct {
	httpStatus int
	firstByte  time.Time
	stream     bool
	failed     bool
	usage      Usage
}

type exchangeKey struct{}

// NewHandler returns the proxy for the back ends of c.
func NewHandler(c Config) (*Handler, error) {
	if len(c.Backends) == 0 {
		return nil, errors.New("meter: no back end")
	}
	if c.Publisher == nil {
		return nil, errors.New("meter: no publisher")
	}
	if c.Source == "" {
		return nil, errors.New("meter: no event source")
	}
	h := &Handler{routes: map[string]route{}, pub: c.Publisher, source: c.Source, log: c.Logger, ring: c.Ring, now: c.Now, newID: c.NewID}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.ring == nil {
		h.ring = NewRing(RingSize)
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.newID == nil {
		h.newID = NewEventID
	}
	hosts := make([]string, 0, len(c.Backends))
	for host := range c.Backends {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		b := c.Backends[host]
		target, err := url.Parse(b.URL)
		if err != nil {
			return nil, fmt.Errorf("meter: back end %q: %w", host, err)
		}
		h.routes[strings.ToLower(host)] = route{name: b.Name, proxy: &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				// The transport then asks for gzip itself and decompresses, so the meter
				// always reads a plain body.
				pr.Out.Header.Del("Accept-Encoding")
			},
			ModifyResponse: h.observe,
			ErrorHandler:   h.backendFailed,
		}}
	}
	return h, nil
}

// Ring returns the ring of the handler.
func (h *Handler) Ring() *Ring { return h.ring }

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := h.now()
	id, idErr := IdentityFromHeaders(r.Header)
	rec := Record{RequestID: id.RequestID, Organization: id.Organization, PrincipalKind: id.PrincipalKind,
		Principal: id.Principal, Alias: id.Alias, Status: StatusRejected}
	host := strings.ToLower(r.Host)
	if hp, _, err := net.SplitHostPort(r.Host); err == nil {
		host = strings.ToLower(hp)
	}
	rt, known := h.routes[host]
	rec.Backend = rt.name
	switch {
	case !strings.HasPrefix(r.URL.Path, "/v1/"):
		rec.Backend = ""
		rec.HTTPStatus = writeError(w, http.StatusNotFound, "not_found", "the meter serves /v1/ only")
	case !known:
		rec.HTTPStatus = writeError(w, http.StatusMisdirectedRequest, "misdirected_request", "no back end is configured for this host")
	case idErr != nil:
		rec.HTTPStatus = writeError(w, http.StatusBadRequest, "missing_identity", "an identity header is absent or empty")
	default:
		h.forward(w, r, rt, id, &rec, start)
		return
	}
	h.logLine(rec, Usage{}, start, time.Time{})
	h.ring.Add(rec)
}

// forward sends the request to the back end, then publishes the usage event when one is due.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, rt route, id Identity, rec *Record, start time.Time) {
	ex := &exchange{}
	rt.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), exchangeKey{}, ex)))
	rec.HTTPStatus = ex.httpStatus
	rec.Stream = ex.stream
	if ex.failed || ex.httpStatus < 200 || ex.httpStatus > 299 {
		rec.Status = StatusError
	} else {
		rec.Status = StatusOK
	}
	if rec.Status == StatusOK && !ex.stream && ex.usage.Complete {
		// The response is written; send it on before the publish.
		_ = http.NewResponseController(w).Flush()
		h.publish(r.Context(), id, ex.usage, rec)
	}
	h.logLine(*rec, ex.usage, start, ex.firstByte)
	h.ring.Add(*rec)
}

func (h *Handler) publish(ctx context.Context, id Identity, u Usage, rec *Record) {
	evID, err := h.newID()
	if err != nil {
		h.log.Error("meter: no event identifier, the event is dropped", "requestId", id.RequestID, "error", err)
		return
	}
	ev := UsageEvent{ID: evID, Organization: id.Organization, Alias: id.Alias, OccurredAt: h.now().UTC(), Measures: MeasuresFrom(u)}
	if body, err := ev.CloudEvent(h.source); err == nil {
		rec.Event = body
	}
	// The caller may close the connection once it has the response; the publish goes on.
	err = h.pub.Publish(context.WithoutCancel(ctx), ev)
	rec.Published = err == nil
}

func (h *Handler) logLine(rec Record, u Usage, start, firstByte time.Time) {
	end := h.now()
	firstByteMs := int64(0)
	if !firstByte.IsZero() {
		firstByteMs = firstByte.Sub(start).Milliseconds()
	}
	h.log.Info("request",
		"requestId", rec.RequestID,
		"organization", rec.Organization,
		"principalKind", rec.PrincipalKind,
		"principal", rec.Principal,
		"alias", rec.Alias,
		"backend", rec.Backend,
		"stream", rec.Stream,
		"status", rec.Status,
		"httpStatus", rec.HTTPStatus,
		"promptTokens", u.PromptTokens,
		"cachedTokens", u.CachedTokens,
		"cacheWriteTokens", u.CacheWriteTokens,
		"completionTokens", u.CompletionTokens,
		"reasoningTokens", u.ReasoningTokens,
		"toolCalls", u.ToolCalls,
		"firstByteMs", firstByteMs,
		"durationMs", end.Sub(start).Milliseconds(),
	)
}

// observe is the ModifyResponse hook: it records the status and reads the usage block of an
// unstreamed 2xx body, then gives the proxy the same bytes to write to the caller.
func (h *Handler) observe(resp *http.Response) error {
	ex, _ := resp.Request.Context().Value(exchangeKey{}).(*exchange)
	if ex == nil {
		return nil
	}
	ex.httpStatus = resp.StatusCode
	ex.firstByte = h.now()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		ex.stream = true // metered from AIP-112; passed through untouched until then
		return nil
	}
	orig := resp.Body
	body, err := io.ReadAll(io.LimitReader(orig, MaxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("meter: read the back end's body: %w", err)
	}
	if len(body) > MaxBodyBytes {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), orig), orig}
		return nil
	}
	if err := orig.Close(); err != nil {
		h.log.Warn("meter: close the back end's body", "error", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	ex.usage = ParseJSON(body)
	return nil
}

// backendFailed is the ErrorHandler of the proxy: the back end could not be reached, or its
// body could not be read.
func (h *Handler) backendFailed(w http.ResponseWriter, r *http.Request, err error) {
	if ex, _ := r.Context().Value(exchangeKey{}).(*exchange); ex != nil {
		ex.failed = true
		ex.httpStatus = http.StatusBadGateway
	}
	h.log.Warn("meter: the back end failed", "error", err)
	writeError(w, http.StatusBadGateway, "backend_error", "the back end failed")
}

// writeError writes the error body of the model contract and returns the status.
func writeError(w http.ResponseWriter, status int, code, message string) int {
	typ := "invalid_request_error"
	if status >= 500 {
		typ = "api_error"
	}
	body, _ := json.Marshal(map[string]map[string]string{"error": {"message": message, "type": typ, "code": code}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.Debug("meter: write the error body", "error", err)
	}
	return status
}
