package meter

// Tests of the proxy. A test back end stands in for a model back end; a fake publisher keeps
// the events. No bus and no disk.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSource = "model-gateway/sovereign.example.org/region-a"

var ulidPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// fakePublisher keeps every event it is given.
type fakePublisher struct {
	mu   sync.Mutex
	evs  []UsageEvent
	fail error
}

// Publish keeps ev and returns the number of events kept as the sequence, or 0 and fail.
func (f *fakePublisher) Publish(_ context.Context, ev UsageEvent) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evs = append(f.evs, ev)
	if f.fail != nil {
		return 0, f.fail
	}
	return uint64(len(f.evs)), nil
}

func (f *fakePublisher) events() []UsageEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]UsageEvent(nil), f.evs...)
}

// rig is one back end, the meter in front of it, the publisher, the ring and the log. The
// meter is called directly, so a request's log line, ring entry and event exist when do
// returns.
type rig struct {
	t        *testing.T
	pub      *fakePublisher
	handler  *Handler
	log      *bytes.Buffer
	upstream int // requests the back end received
	mu       sync.Mutex
}

// newRig starts a back end that answers every request with status, content type and body.
func newRig(t *testing.T, status int, contentType, body string) *rig {
	t.Helper()
	rg := &rig{t: t, pub: &fakePublisher{}, log: &bytes.Buffer{}}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rg.mu.Lock()
		rg.upstream++
		rg.mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(backend.Close)
	h, err := NewHandler(Config{
		Backends:  map[string]Backend{"meter-simulation": {Name: "simulation", URL: backend.URL}},
		Publisher: rg.pub,
		Source:    testSource,
		Logger:    slog.New(slog.NewJSONHandler(rg.log, nil)),
		Ring:      NewRing(RingSize),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rg.handler = h
	return rg
}

func identityHeaders() http.Header {
	h := http.Header{}
	h.Set(HeaderOrganization, "acme")
	h.Set(HeaderPrincipalKind, "agent")
	h.Set(HeaderPrincipal, "correlator")
	h.Set(HeaderAlias, "agent-default")
	h.Set(HeaderRequestID, "req-0000001")
	return h
}

// do sends one request through the meter with the given host, path and headers.
func (rg *rig) do(host, path string, hdr http.Header) (int, []byte) {
	rg.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(`{"model":"agent-default","messages":[]}`))
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	w := httptest.NewRecorder()
	rg.handler.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (rg *rig) calls() int {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.upstream
}

// logLines returns the JSON log lines whose msg is "request".
func (rg *rig) logLines() []map[string]any {
	rg.t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(rg.log.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "request" {
			out = append(out, m)
		}
	}
	return out
}

func completion(usage string) string {
	return `{"id":"chatcmpl-1","object":"chat.completion","model":"sim-default","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":` + usage + `}`
}

const usageWithCache = `{"prompt_tokens":1840,"completion_tokens":212,"total_tokens":2052,"prompt_tokens_details":{"cached_tokens":1536}}`

func TestMissingIdentityGets400AndNoEvent(t *testing.T) {
	for _, missing := range []string{HeaderOrganization, HeaderPrincipalKind, HeaderPrincipal, HeaderAlias, HeaderRequestID} {
		t.Run(missing, func(t *testing.T) {
			rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
			hdr := identityHeaders()
			hdr.Del(missing)
			status, body := rg.do("meter-simulation", "/v1/chat/completions", hdr)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			var e struct {
				Error struct{ Code string } `json:"error"`
			}
			if err := json.Unmarshal(body, &e); err != nil || e.Error.Code != "missing_identity" {
				t.Fatalf("body = %s, want code missing_identity", body)
			}
			if rg.calls() != 0 {
				t.Fatalf("the back end got %d requests, want 0", rg.calls())
			}
			if n := len(rg.pub.events()); n != 0 {
				t.Fatalf("%d events, want 0", n)
			}
			lines := rg.logLines()
			if len(lines) != 1 || lines[0]["status"] != StatusRejected || lines[0]["httpStatus"] != float64(400) {
				t.Fatalf("log lines = %v, want one rejected 400", lines)
			}
		})
	}
}

func TestEmptyIdentityHeaderGets400(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	hdr := identityHeaders()
	hdr.Set(HeaderOrganization, " ")
	if status, _ := rg.do("meter-simulation", "/v1/chat/completions", hdr); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if rg.calls() != 0 || len(rg.pub.events()) != 0 {
		t.Fatal("a request with an empty identity header was forwarded or metered")
	}
}

func TestPathOutsideV1Gets404(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	if status, _ := rg.do("meter-simulation", "/api/status", identityHeaders()); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if rg.calls() != 0 || len(rg.pub.events()) != 0 {
		t.Fatal("a request outside /v1/ was forwarded or metered")
	}
}

func TestUnknownHostGets421(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	if status, _ := rg.do("meter-elsewhere", "/v1/chat/completions", identityHeaders()); status != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want 421", status)
	}
	if rg.calls() != 0 || len(rg.pub.events()) != 0 {
		t.Fatal("a request for an unknown host was forwarded or metered")
	}
	// A port on the Host header does not change the route.
	if status, _ := rg.do("meter-simulation:8080", "/v1/chat/completions", identityHeaders()); status != http.StatusOK {
		t.Fatalf("status with a port = %d, want 200", status)
	}
}

func TestLogLineCarriesTheIdentityHeaders(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	rg.do("meter-simulation", "/v1/chat/completions", identityHeaders())
	lines := rg.logLines()
	if len(lines) != 1 {
		t.Fatalf("%d log lines, want 1", len(lines))
	}
	l := lines[0]
	want := map[string]any{"requestId": "req-0000001", "organization": "acme", "principalKind": "agent",
		"principal": "correlator", "alias": "agent-default", "backend": "simulation", "stream": false,
		"status": StatusOK, "httpStatus": float64(200), "promptTokens": float64(1840), "cachedTokens": float64(1536),
		"cacheWriteTokens": float64(0), "completionTokens": float64(212), "reasoningTokens": float64(0), "toolCalls": float64(0)}
	for k, v := range want {
		if l[k] != v {
			t.Errorf("log %s = %v, want %v", k, l[k], v)
		}
	}
	for _, k := range []string{"firstByteMs", "durationMs"} {
		if _, ok := l[k]; !ok {
			t.Errorf("log line has no %s", k)
		}
	}
}

func TestCallerHeadersHaveNoEffectOnTheEvent(t *testing.T) {
	plain := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	plain.do("meter-simulation", "/v1/chat/completions", identityHeaders())
	forged := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	hdr := identityHeaders()
	// The two caller headers the old sidecar trusted, written in two pieces.
	hdr.Set("X-"+"Customer-Id", "someone-else")
	hdr.Set("X-"+"Tenant-Id", "other-org")
	forged.do("meter-simulation", "/v1/chat/completions", hdr)
	a, b := plain.pub.events(), forged.pub.events()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("events = %d and %d, want 1 and 1", len(a), len(b))
	}
	if a[0].Organization != b[0].Organization || a[0].Alias != b[0].Alias || !reflect.DeepEqual(a[0].Measures, b[0].Measures) {
		t.Fatalf("the caller headers changed the event: %+v against %+v", b[0], a[0])
	}
	if b[0].Organization != "acme" {
		t.Fatalf("organization = %q, want acme", b[0].Organization)
	}
}

func TestBackendErrorGivesNoEventAndAnErrorEntry(t *testing.T) {
	rg := newRig(t, http.StatusInternalServerError, "application/json", `{"error":{"message":"boom","type":"api_error","code":"backend_error"}}`)
	status, _ := rg.do("meter-simulation", "/v1/chat/completions", identityHeaders())
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the back end's 500", status)
	}
	if n := len(rg.pub.events()); n != 0 {
		t.Fatalf("%d events, want 0", n)
	}
	rec, ok := rg.handler.Ring().Get("req-0000001")
	if !ok || rec.Status != StatusError || rec.HTTPStatus != 500 || rec.Published || rec.Sequence != 0 || rec.Event != nil {
		t.Fatalf("ring entry = %+v (found %v), want status error, httpStatus 500, nothing published", rec, ok)
	}
}

func TestUnreachableBackendGets502AndNoEvent(t *testing.T) {
	pub := &fakePublisher{}
	h, err := NewHandler(Config{Backends: map[string]Backend{"meter-simulation": {Name: "simulation", URL: "http://127.0.0.1:1"}},
		Publisher: pub, Source: testSource, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://meter-simulation/v1/chat/completions", strings.NewReader("{}"))
	for k, vs := range identityHeaders() {
		req.Header[k] = vs
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "backend_error") {
		t.Fatalf("response = %d %s, want 502 backend_error", w.Code, w.Body.String())
	}
	if len(pub.events()) != 0 {
		t.Fatal("an unreachable back end was metered")
	}
	if rec, ok := h.Ring().Get("req-0000001"); !ok || rec.Status != StatusError {
		t.Fatalf("ring entry = %+v, want status error", rec)
	}
}

func TestUnstreamedResponseGivesTheMeasures(t *testing.T) {
	cases := []struct {
		name  string
		usage string
		want  []Measure
	}{
		{"with cached tokens", usageWithCache, []Measure{{MeterInput, 304}, {MeterCacheRead, 1536}, {MeterOutput, 212}, {MeterRequests, 1}}},
		{"with cache-write tokens", `{"prompt_tokens":100,"completion_tokens":7,"total_tokens":107,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":40}}`,
			[]Measure{{MeterInput, 60}, {MeterCacheWrite, 40}, {MeterOutput, 7}, {MeterRequests, 1}}},
		{"only output tokens", `{"prompt_tokens":0,"completion_tokens":5,"total_tokens":5}`, []Measure{{MeterOutput, 5}, {MeterRequests, 1}}},
		{"reasoning tokens are part of the output", `{"prompt_tokens":10,"completion_tokens":30,"total_tokens":40,"completion_tokens_details":{"reasoning_tokens":20}}`,
			[]Measure{{MeterInput, 10}, {MeterOutput, 30}, {MeterRequests, 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := completion(tc.usage)
			rg := newRig(t, http.StatusOK, "application/json", body)
			status, got := rg.do("meter-simulation", "/v1/chat/completions", identityHeaders())
			if status != http.StatusOK || string(got) != body {
				t.Fatalf("the caller got %d %q, want the back end's 200 and its body", status, got)
			}
			evs := rg.pub.events()
			if len(evs) != 1 {
				t.Fatalf("%d events, want 1", len(evs))
			}
			if !reflect.DeepEqual(evs[0].Measures, tc.want) {
				t.Fatalf("measures = %v, want %v", evs[0].Measures, tc.want)
			}
			if evs[0].Organization != "acme" || evs[0].Alias != "agent-default" || !ulidPattern.MatchString(evs[0].ID) {
				t.Fatalf("event = %+v, want organization acme, alias agent-default and a ULID", evs[0])
			}
			rec, ok := rg.handler.Ring().Get("req-0000001")
			if !ok || rec.Status != StatusOK || !rec.Published || rec.Sequence != 1 || rec.Event == nil {
				t.Fatalf("ring entry = %+v, want status ok, published, with the event", rec)
			}
		})
	}
}

func TestNoUsageBlockGivesNoEvent(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", `{"object":"list","data":[{"id":"agent-default","object":"model"}]}`)
	if status, _ := rg.do("meter-simulation", "/v1/models", identityHeaders()); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if n := len(rg.pub.events()); n != 0 {
		t.Fatalf("%d events, want 0", n)
	}
}

func TestSpooledEventIsNotPublished(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	rg.pub.fail = ErrSpooled
	rg.do("meter-simulation", "/v1/chat/completions", identityHeaders())
	if rec, _ := rg.handler.Ring().Get("req-0000001"); rec.Published || rec.Sequence != 0 || rec.Event == nil {
		t.Fatalf("ring entry = %+v, want published false with the event", rec)
	}
}

func TestEnvelopeHasTheEightAttributes(t *testing.T) {
	at := time.Date(2026, 12, 1, 9, 0, 6, 0, time.UTC)
	ev := UsageEvent{ID: "01JCF0R1K2M3N4P5Q6R7S8T9V0", Organization: "acme", Alias: "agent-default", OccurredAt: at,
		Measures: MeasuresFrom(Usage{PromptTokens: 1840, CachedTokens: 1536, CompletionTokens: 212, Complete: true})}
	body, err := ev.CloudEvent(testSource)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"specversion":"1.0","id":"01JCF0R1K2M3N4P5Q6R7S8T9V0","type":"io.openova.catalyst.usage.recorded.v1",` +
		`"source":"model-gateway/sovereign.example.org/region-a","subject":"acme","time":"2026-12-01T09:00:06Z",` +
		`"datacontenttype":"application/json","data":{"organization":"acme","resource":{"kind":"llm-model","id":"agent-default"},` +
		`"occurredAt":"2026-12-01T09:00:06Z","measures":[{"meter":"llm.tokens.input","quantity":304},` +
		`{"meter":"llm.tokens.cache_read","quantity":1536},{"meter":"llm.tokens.output","quantity":212},{"meter":"llm.requests","quantity":1}]}}`
	if string(body) != want {
		t.Fatalf("envelope\n got %s\nwant %s", body, want)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "data,datacontenttype,id,source,specversion,subject,time,type" {
		t.Fatalf("attributes = %v, want exactly the eight", keys)
	}
}

func TestEnvelopeRefusesAnIncompleteEvent(t *testing.T) {
	good := UsageEvent{ID: "01JCF0R1K2M3N4P5Q6R7S8T9V0", Organization: "acme", Alias: "agent-default", Measures: []Measure{{MeterRequests, 1}}}
	for name, ev := range map[string]UsageEvent{
		"no id":           {Organization: "acme", Alias: "agent-default", Measures: good.Measures},
		"no organization": {ID: good.ID, Alias: "agent-default", Measures: good.Measures},
		"no alias":        {ID: good.ID, Organization: "acme", Measures: good.Measures},
		"no measures":     {ID: good.ID, Organization: "acme", Alias: "agent-default"},
	} {
		if _, err := ev.CloudEvent(testSource); err == nil {
			t.Errorf("%s: CloudEvent gave no error", name)
		}
	}
	if _, err := good.CloudEvent(""); err == nil {
		t.Error("no source: CloudEvent gave no error")
	}
}

func TestMeasuresFromNeverGoesBelowZero(t *testing.T) {
	got := MeasuresFrom(Usage{PromptTokens: 10, CachedTokens: 8, CacheWriteTokens: 5, CompletionTokens: 1})
	want := []Measure{{MeterCacheRead, 8}, {MeterCacheWrite, 5}, {MeterOutput, 1}, {MeterRequests, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("measures = %v, want %v", got, want)
	}
}

func TestParseJSON(t *testing.T) {
	u := ParseJSON([]byte(`{"model":"sim-default","choices":[{"message":{"tool_calls":[{"id":"a"},{"id":"b"}]}}],` +
		`"usage":{"prompt_tokens":9,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"completion_tokens_details":{"reasoning_tokens":3}}}`))
	want := Usage{PromptTokens: 9, CachedTokens: 2, CacheWriteTokens: 1, CompletionTokens: 4, ReasoningTokens: 3, ToolCalls: 2, ResponseModel: "sim-default", Complete: true}
	if u != want {
		t.Fatalf("usage = %+v, want %+v", u, want)
	}
	for _, body := range []string{`not json`, `{"model":"x"}`, `{"usage":null}`} {
		if ParseJSON([]byte(body)).Complete {
			t.Errorf("%s: Complete is true", body)
		}
	}
}

func TestNewEventIDIsAULID(t *testing.T) {
	at := time.Date(2026, 12, 1, 9, 0, 6, 0, time.UTC)
	id, err := newULID(at, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if !ulidPattern.MatchString(id) || id[10:] != "0000000000000000" {
		t.Fatalf("id = %q", id)
	}
	// The first ten characters are the milliseconds in base 32.
	ms := int64(0)
	for _, c := range id[:10] {
		ms = ms*32 + int64(strings.IndexRune(crockford, c))
	}
	if ms != at.UnixMilli() {
		t.Fatalf("time part = %d, want %d", ms, at.UnixMilli())
	}
	if _, err := newULID(at, bytes.NewReader(nil)); err == nil {
		t.Fatal("no random bits gave no error")
	}
	a, _ := NewEventID()
	b, _ := NewEventID()
	if !ulidPattern.MatchString(a) || a == b {
		t.Fatalf("NewEventID gave %q and %q", a, b)
	}
}

func TestParseBackends(t *testing.T) {
	m, err := ParseBackends(`{"meter-simulation": {"backend": "simulation", "url": "http://axon.axon.svc.cluster.local:3000"}}`)
	if err != nil || m["meter-simulation"].Name != "simulation" {
		t.Fatalf("ParseBackends = %v, %v", m, err)
	}
	for _, doc := range []string{``, `{}`, `[]`, `{"h": {"backend": "", "url": "http://x"}}`, `{"h": {"backend": "b", "url": "ftp://x"}}`, `{"h": {"backend": "b", "url": "http://"}}`} {
		if _, err := ParseBackends(doc); err == nil {
			t.Errorf("%q: no error", doc)
		}
	}
}

func TestRingKeepsTheLastEntries(t *testing.T) {
	r := NewRing(2)
	r.Add(Record{RequestID: "a", Status: StatusOK})
	r.Add(Record{RequestID: "b", Status: StatusOK})
	r.Add(Record{RequestID: "c", Status: StatusError})
	if _, ok := r.Get("a"); ok {
		t.Fatal("the oldest entry was kept")
	}
	if rec, ok := r.Get("c"); !ok || rec.Status != StatusError {
		t.Fatalf("Get(c) = %+v, %v", rec, ok)
	}
	r.Add(Record{RequestID: "c", Status: StatusOK})
	if rec, _ := r.Get("c"); rec.Status != StatusOK {
		t.Fatal("Get does not return the newest entry")
	}
}
