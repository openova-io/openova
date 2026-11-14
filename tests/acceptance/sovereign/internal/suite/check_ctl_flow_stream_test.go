package suite

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
)

// flowStreamAPI answers GET /api/v1/flows/dep-1/stream as the API does: the
// status, the content type and the body given. A 200 answer then holds the
// stream open until the client goes away. It keeps the session cookie it was
// sent.
type flowStreamAPI struct {
	status      int
	contentType string
	body        string
	cookie      string
	path        string
}

func (f *flowStreamAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.path = r.URL.EscapedPath()
	if c, err := r.Cookie("catalyst_session"); err == nil {
		f.cookie = c.Value
	}
	w.Header().Set("Content-Type", f.contentType)
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
	if f.status != http.StatusOK {
		return
	}
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

const flowStreamNodes = `{"type":"snapshot","flow":{"id":"dep-1","status":"running","startedAt":0},` +
	`"nodes":[{"id":"dep-1:install-cilium","flowId":"dep-1","label":"install-cilium","status":"running"}],` +
	`"relationships":[{"fromId":"dep-1:bootstrap-kit","toId":"dep-1:install-cilium","type":"contains"}]}`

func flowStreamRun(t *testing.T, f *flowStreamAPI, cc *CheckContext) *runner {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	if cc.API == nil {
		cc.API = api.New(srv.URL, "session-value")
	}
	return runCheck(t, "none", flowStreamCheck{timeout: 2 * time.Second}, cc)
}

func TestFlowStreamCheck(t *testing.T) {
	if _, ok := registry[flowStreamID]; !ok || flowStreamID != "CTL-G12" {
		t.Fatalf("check %s is not registered", flowStreamID)
	}
	sse := "text/event-stream"
	for _, c := range []struct {
		name, contentType, body, fail string
		status                        int
	}{
		{"a snapshot with nodes passes", sse, "event: snapshot\ndata: " + flowStreamNodes + "\n\n", "", 200},
		{"an empty snapshot passes", sse, "event: snapshot\ndata: {\"type\":\"snapshot\"}\n\n", "", 200},
		{"a comment before the first event is skipped", sse + "; charset=utf-8", ": hello\n\nevent: snapshot\ndata: {\"type\":\"snapshot\"}\n\n", "", 200},
		{"an error status fails", "text/plain", "deployment not found\n", "HTTP 404 deployment not found; want 200", 404},
		{"the flow server gone through the proxy fails", "text/plain", "openova-flow-server unreachable\n", "HTTP 502", 502},
		{"another content type fails", "application/json", "{\"type\":\"snapshot\"}", `Content-Type "application/json"; want text/event-stream`, 200},
		{"a heartbeat as the first event fails", sse, "event: heartbeat\ndata: {}\n\nevent: snapshot\ndata: {\"type\":\"snapshot\"}\n\n", `the first event is "heartbeat"; want snapshot`, 200},
		{"an unnamed first event fails", sse, "data: {\"type\":\"snapshot\"}\n\n", `the first event is "message"; want snapshot`, 200},
		{"data that is not JSON fails", sse, "event: snapshot\ndata: not json\n\n", "is not a JSON object", 200},
		{"another type fails", sse, "event: snapshot\ndata: {\"type\":\"upsert-nodes\"}\n\n", `has type "upsert-nodes"; want snapshot`, 200},
		{"the flow of another deployment fails", sse, "event: snapshot\ndata: {\"type\":\"snapshot\",\"flow\":{\"id\":\"dep-2\"}}\n\n", `the snapshot is of flow "dep-2"; want the deployment "dep-1"`, 200},
		{"a node of another deployment fails", sse, "event: snapshot\ndata: " + strings.Replace(flowStreamNodes, `"flowId":"dep-1"`, `"flowId":"dep-2"`, 1) + "\n\n", `node "dep-1:install-cilium" has flowId "dep-2"`, 200},
		{"a stream that sends no event fails", sse, ": ping\n\n", "no event within 2s", 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &flowStreamAPI{status: c.status, contentType: c.contentType, body: c.body}
			r := flowStreamRun(t, f, &CheckContext{Deploy: &DeploymentInfo{ID: "dep-1"}})
			if len(r.run.Checks) != 1 {
				t.Fatalf("checks %+v", r.run.Checks)
			}
			got := r.run.Checks[0]
			if c.fail == "" {
				if got.Status != Pass {
					t.Fatalf("want PASS, got %s: %s", got.Status, got.Reason)
				}
			} else if got.Status != Fail || !strings.Contains(got.Reason, c.fail) {
				t.Fatalf("want FAIL with %q, got %s: %s", c.fail, got.Status, got.Reason)
			}
			if f.path != "/api/v1/flows/dep-1/stream" || f.cookie != "session-value" {
				t.Fatalf("request path %q, cookie %q", f.path, f.cookie)
			}
		})
	}
}

func TestFlowStreamCheckNeedsASessionAndADeployment(t *testing.T) {
	for _, c := range []struct {
		name string
		cc   *CheckContext
		fail string
	}{
		{"no console session", &CheckContext{Deploy: &DeploymentInfo{ID: "dep-1"}}, "the run has no console session"},
		{"no deployment", &CheckContext{API: api.New("http://127.0.0.1:9", "s")}, "the run did not learn the deployment under test"},
		{"an empty deployment id", &CheckContext{API: api.New("http://127.0.0.1:9", "s"), Deploy: &DeploymentInfo{}}, "the run did not learn the deployment under test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runCheck(t, "none", flowStreamCheck{timeout: time.Second}, c.cc)
			if len(r.run.Checks) != 1 || r.run.Checks[0].Status != Fail || !strings.Contains(r.run.Checks[0].Reason, c.fail) {
				t.Fatalf("want FAIL with %q, got %+v", c.fail, r.run.Checks)
			}
		})
	}
}
