package meter

// Tests of the admin listener: the ring route, the metrics, readiness and liveness; and the test
// that no text of a request or of an answer reaches the log line, the ring or the spool.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// adminGet sends GET path to the admin handler and returns the status and the body.
func adminGet(t *testing.T, admin http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	admin.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

func TestAdminEventsRouteReturnsTheRingEntry(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	rg.do("meter-simulation", "/v1/chat/completions", identityHeaders())
	admin := NewAdmin(rg.handler, &SpoolPublisher{SpoolDir: t.TempDir()})
	status, body := adminGet(t, admin, "/internal/events/req-0000001")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"requestId": `"req-0000001"`, "organization": `"acme"`, "principalKind": `"agent"`, "principal": `"correlator"`,
		"alias": `"agent-default"`, "backend": `"simulation"`, "stream": "false", "status": `"ok"`, "httpStatus": "200", "published": "true", "sequence": "1"}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if len(got) != 12 || !strings.Contains(string(got["event"]), `"type":"io.openova.catalyst.usage.recorded.v1"`) {
		t.Fatalf("entry = %s, want the twelve keys and the published event", body)
	}
	if status, _ := adminGet(t, admin, "/internal/events/req-unknown1"); status != http.StatusNotFound {
		t.Fatalf("unknown request: status = %d, want 404", status)
	}
}

func TestHandlerRingKeepsTheLast1000Requests(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", `{"object":"list","data":[]}`)
	for i := 0; i <= RingSize; i++ {
		hdr := identityHeaders()
		hdr.Set(HeaderRequestID, fmt.Sprintf("req-%07d", i))
		rg.do("meter-simulation", "/v1/models", hdr)
	}
	if _, ok := rg.handler.Ring().Get("req-0000000"); ok {
		t.Fatal("the first of 1001 requests is still in the ring")
	}
	for _, id := range []string{"req-0000001", "req-0001000"} {
		if _, ok := rg.handler.Ring().Get(id); !ok {
			t.Fatalf("%s is not in the ring", id)
		}
	}
}

func TestMetricsCountRequestsTokensAndEvents(t *testing.T) {
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	pub := &SpoolPublisher{Bus: &fakeBus{}, Source: testSource, PublishTimeout: time.Second, SpoolDir: t.TempDir()}
	rg.handler.pub = pub
	rg.do("meter-simulation", "/v1/chat/completions", identityHeaders())
	hdr := identityHeaders()
	hdr.Del(HeaderPrincipal)
	rg.do("meter-simulation", "/v1/chat/completions", hdr)
	status, body := adminGet(t, NewAdmin(rg.handler, pub), "/metrics")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	for _, line := range []string{
		`model_meter_requests_total{organization="acme",alias="agent-default",backend="simulation",status="ok"} 1`,
		`model_meter_requests_total{organization="acme",alias="agent-default",backend="simulation",status="rejected"} 1`,
		`model_meter_tokens_total{organization="acme",alias="agent-default",principal="correlator",class="input"} 304`,
		`model_meter_tokens_total{organization="acme",alias="agent-default",principal="correlator",class="cache_read"} 1536`,
		`model_meter_tokens_total{organization="acme",alias="agent-default",principal="correlator",class="output"} 212`,
		"model_meter_events_published_total 1",
		"model_meter_events_spooled_total 0",
		"model_meter_events_dropped_total 0",
	} {
		if !strings.Contains(body, line+"\n") {
			t.Errorf("metrics have no line %s", line)
		}
	}
	if strings.Count(body, "# TYPE ") != 5 || strings.Contains(body, `class="cache_write"`) {
		t.Fatalf("metrics = %s, want five families and no zero token class", body)
	}
}

func TestReadyzFollowsTheSpoolDirectory(t *testing.T) {
	dir := t.TempDir()
	rg := newRig(t, http.StatusOK, "application/json", completion(usageWithCache))
	admin := NewAdmin(rg.handler, &SpoolPublisher{SpoolDir: dir})
	if status, _ := adminGet(t, admin, "/healthz"); status != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", status)
	}
	if status, _ := adminGet(t, admin, "/readyz"); status != http.StatusOK {
		t.Fatalf("readyz with a writable spool directory = %d, want 200", status)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("readyz left %d files in the spool directory", len(entries))
	}
	missing := NewAdmin(rg.handler, &SpoolPublisher{SpoolDir: filepath.Join(dir, "missing")})
	if status, _ := adminGet(t, missing, "/readyz"); status != http.StatusServiceUnavailable {
		t.Fatalf("readyz without a spool directory = %d, want 503", status)
	}
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	if err := os.Chmod(dir, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o600) })
	if status, _ := adminGet(t, admin, "/readyz"); status != http.StatusServiceUnavailable {
		t.Fatalf("readyz with a read-only spool directory = %d, want 503", status)
	}
	if status, _ := adminGet(t, admin, "/healthz"); status != http.StatusOK {
		t.Fatalf("healthz with a read-only spool directory = %d, want 200", status)
	}
}

// echoBackend answers with the request body in the answer text, streamed when asked.
func echoBackend(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	text, _ := json.Marshal("echo:" + string(b))
	usage := `"usage":{"prompt_tokens":7,"completion_tokens":3}`
	if !strings.Contains(string(b), `"stream":true`) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":%s}}],%s}`, text, usage)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%s}}]}\n\ndata: {\"choices\":[],%s}\n\ndata: [DONE]\n\n", text, usage)
}

func TestNoRequestOrAnswerTextIsRecorded(t *testing.T) {
	const words = "Zq7-private-words"
	backend := httptest.NewServer(http.HandlerFunc(echoBackend))
	t.Cleanup(backend.Close)
	logs, spool := &bytes.Buffer{}, t.TempDir()
	pub := &SpoolPublisher{Source: testSource, PublishTimeout: time.Second, SpoolDir: spool} // no bus: every event is spooled
	h, err := NewHandler(Config{Backends: map[string]Backend{"meter-simulation": {Name: "simulation", URL: backend.URL}}, Publisher: pub,
		Source: testSource, Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"req-unstream1", "req-stream001"}
	for i, body := range []string{
		`{"model":"agent-default","messages":[{"role":"user","content":"` + words + `"}]}`,
		`{"model":"agent-default","stream":true,"messages":[{"role":"user","content":"` + words + `"}]}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "http://meter-simulation/v1/chat/completions", strings.NewReader(body))
		for k, vs := range identityHeaders() {
			req.Header[k] = vs
		}
		req.Header.Set(HeaderRequestID, ids[i])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), "echo:") || !strings.Contains(w.Body.String(), words) {
			t.Fatalf("%s: the answer %q does not carry the text, so the test proves nothing", ids[i], w.Body.String())
		}
	}
	if strings.Count(logs.String(), `"msg":"request"`) != 2 || strings.Contains(logs.String(), words) {
		t.Fatalf("log = %s, want two request lines without the text", logs.String())
	}
	admin := NewAdmin(h, pub)
	for _, id := range ids {
		if status, body := adminGet(t, admin, "/internal/events/"+id); status != http.StatusOK || !strings.Contains(body, `"event":{`) || strings.Contains(body, words) {
			t.Fatalf("ring entry of %s = %d %s, want one with its event and without the text", id, status, body)
		}
	}
	if _, body := adminGet(t, admin, "/metrics"); strings.Contains(body, words) {
		t.Fatal("the metrics hold the text")
	}
	files, _ := filepath.Glob(filepath.Join(spool, "*.json"))
	if len(files) != 2 {
		t.Fatalf("%d spool files, want 2", len(files))
	}
	for _, f := range files {
		if b, err := os.ReadFile(filepath.Clean(f)); err != nil || bytes.Contains(b, []byte(words)) {
			t.Fatalf("spool file %s holds the text or cannot be read: %v", f, err)
		}
	}
}
