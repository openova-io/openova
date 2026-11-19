package judge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTokenRequestCarriesServiceAccountToken(t *testing.T) {
	if DefaultServiceAccountFile != "/var/run/secrets/kubernetes.io/serviceaccount/token" {
		t.Fatalf("default file %s", DefaultServiceAccountFile)
	}
	var mu sync.Mutex
	var seen []string
	status, answer := http.StatusOK, `{"token":"agent-jwt","expiresAt":"2026-12-01T09:30:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body TokenRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		err := dec.Decode(&body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("Content-Type"))
		code, text := status, answer
		mu.Unlock()
		if err != nil || body != (TokenRequest{Organization: "acme", Agent: "correlator", Tier: TierViewer, TTLSeconds: 1800}) {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, text)
	}))
	defer srv.Close()
	respond := func(code int, text string) { mu.Lock(); status, answer = code, text; mu.Unlock() }
	requests := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("sa-token-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &Tokens{BaseURL: srv.URL + "/", ServiceAccountFile: file}
	req := TokenRequest{Organization: "acme", Agent: "correlator", Tier: TierViewer, TTLSeconds: 1800}
	if tok, err := src.Token(context.Background(), req); err != nil || tok != "agent-jwt" {
		t.Fatalf("token %q (%v)", tok, err)
	}
	// The kubelet rotates the projected token: every request reads the file again.
	if err := os.WriteFile(file, []byte("sa-token-two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Token(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	respond(http.StatusForbidden, `{"error":"caller-not-allowed","detail":"the entry allows tier viewer at most"}`)
	_, err := src.Token(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "caller-not-allowed") || strings.Contains(err.Error(), "sa-token") {
		t.Fatalf("refusal: %v", err)
	}
	respond(http.StatusOK, `{"expiresAt":"2026-12-01T09:30:00Z"}`)
	if _, err := src.Token(context.Background(), req); err == nil {
		t.Fatal("an answer without a token was accepted")
	}
	want := []string{
		"POST /api/v1/internal/agent-tokens Bearer sa-token-one application/json",
		"POST /api/v1/internal/agent-tokens Bearer sa-token-two application/json",
		"POST /api/v1/internal/agent-tokens Bearer sa-token-two application/json",
		"POST /api/v1/internal/agent-tokens Bearer sa-token-two application/json",
	}
	if got := requests(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(got, "\n"))
	}
	for name, content := range map[string]*string{"missing": nil, "empty": new(string)} {
		f := filepath.Join(t.TempDir(), name)
		if content != nil {
			if err := os.WriteFile(f, []byte(*content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := (&Tokens{BaseURL: srv.URL, ServiceAccountFile: f}).Token(context.Background(), req); err == nil {
			t.Fatalf("a %s ServiceAccount token file was accepted", name)
		}
	}
	if got := requests(); len(got) != len(want) {
		t.Fatalf("%d requests were sent without a ServiceAccount token", len(got)-len(want))
	}
}

// rpcServer answers JSON-RPC requests with the answers given per method, and records them.
type rpcServer struct {
	mu      sync.Mutex
	methods []string
	params  []map[string]any
	headers []http.Header
}

func (s *rpcServer) snapshot() ([]string, []map[string]any, []http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...), append([]map[string]any(nil), s.params...), append([]http.Header(nil), s.headers...)
}

func (s *rpcServer) start(t *testing.T, status int, answers map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string         `json:"jsonrpc"`
			ID      string         `json:"id"`
			Method  string         `json:"method"`
			Params  map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.JSONRPC != "2.0" || req.ID == "" {
			http.Error(w, "not JSON-RPC", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.methods, s.params, s.headers = append(s.methods, req.Method), append(s.params, req.Params), append(s.headers, r.Header)
		s.mu.Unlock()
		if req.Method == "SendMessage" && answers[req.Method] == "block" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"`+req.ID+`",`+answers[req.Method]+`}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestA2A(t *testing.T) {
	task := func(state, rest string) string {
		return `"result":{"task":{"id":"t1","contextId":"c1","status":{"state":"` + state + `"` + rest + `}`
	}
	completed := func(parts string) string {
		return task(StateCompleted, `},"artifacts":[{"artifactId":"a1","parts":`+parts+`}]}`)
	}
	cases := []struct {
		name    string
		status  int
		send    string
		want    string // the answer, or "" for an error
		noAns   bool   // the error wraps ErrNoAnswer
		methods string
	}{
		{"data part", 200, completed(`[{"text":"thinking"},{"data":{"decisions":[]}}]`), `{"decisions":[]}`, false, "SendMessage"},
		{"text part with JSON", 200, completed(`[{"text":" {\"a\":1}\n"}]`), `{"a":1}`, false, "SendMessage"},
		{"text part with prose", 200, completed(`[{"text":"attach it"}]`), `"attach it"`, false, "SendMessage"},
		{"wrapped data part", 200, completed(`[{"data":{"data":{"x":1}}}]`), `{"x":1}`, false, "SendMessage"},
		{"status message", 200, task(StateCompleted, `,"message":{"role":"ROLE_AGENT","parts":[{"text":"{\"m\":2}"}]}}}`), `{"m":2}`, false, "SendMessage"},
		{"message instead of a task", 200, `"result":{"message":{"role":"ROLE_AGENT","parts":[{"data":{"y":3}}]}}`, `{"y":3}`, false, "SendMessage"},
		{"bare task", 200, `"result":{"id":"t1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"parts":[{"data":{"z":4}}]}]}`, `{"z":4}`, false, "SendMessage"},
		{"completed without parts", 200, completed(`[]`), "", true, "SendMessage"},
		{"input required is cancelled", 200, task(StateInputRequired, `}}`), "", true, "SendMessage,CancelTask"},
		{"failed", 200, task(StateFailed, `}}`), "", false, "SendMessage"},
		{"JSON-RPC error", 200, `"error":{"code":-32001,"message":"task not found"}`, "", false, "SendMessage"},
		{"HTTP error", 500, `"result":{}`, "", false, "SendMessage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &rpcServer{}
			srv := s.start(t, c.status, map[string]string{"SendMessage": c.send, "CancelTask": task(StateCanceled, `}}`)})
			got, err := (&A2A{}).Ask(context.Background(), srv.URL+"/agents/acme/correlator", "agent-jwt", `{"task":"correlate"}`)
			switch {
			case c.want != "" && (err != nil || string(got) != c.want):
				t.Fatalf("answer %s (%v), want %s", got, err, c.want)
			case c.want == "" && err == nil:
				t.Fatalf("answer %s, want an error", got)
			case c.want == "" && errors.Is(err, ErrNoAnswer) != c.noAns:
				t.Fatalf("error %v: wraps ErrNoAnswer %v, want %v", err, errors.Is(err, ErrNoAnswer), c.noAns)
			}
			methods, params, headers := s.snapshot()
			if m := strings.Join(methods, ","); m != c.methods {
				t.Fatalf("methods %s, want %s", m, c.methods)
			}
			h := headers[0]
			if h.Get("Authorization") != "Bearer agent-jwt" || h.Get("A2A-Version") != "1.0" || h.Get("X-User-Id") != "ops-records" {
				t.Fatalf("headers %v", h)
			}
			msg, _ := params[0]["message"].(map[string]any)
			parts, _ := msg["parts"].([]any)
			conf, _ := params[0]["configuration"].(map[string]any)
			if msg["role"] != "ROLE_USER" || msg["messageId"] == nil || msg["messageId"] == "" || len(parts) != 1 ||
				conf["returnImmediately"] != false || msg["contextId"] != nil {
				t.Fatalf("SendMessage params %v", params[0])
			}
			if first, _ := parts[0].(map[string]any); first["text"] != `{"task":"correlate"}` {
				t.Fatalf("SendMessage part %v", parts[0])
			}
			if len(params) == 2 && params[1]["id"] != "t1" {
				t.Fatalf("CancelTask params %v", params[1])
			}
		})
	}
	t.Run("cancelled turn", func(t *testing.T) {
		s := &rpcServer{}
		srv := s.start(t, 200, map[string]string{"SendMessage": "block"})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := (&A2A{}).Ask(ctx, srv.URL, "agent-jwt", "{}")
			done <- err
		}()
		for end := time.Now().Add(wait); ; time.Sleep(5 * time.Millisecond) {
			s.mu.Lock()
			n := len(s.methods)
			s.mu.Unlock()
			if n == 1 {
				break
			}
			if time.Now().After(end) {
				t.Fatal("SendMessage was not sent")
			}
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error %v, want context.Canceled", err)
			}
		case <-time.After(wait):
			t.Fatal("Ask did not return when its turn was cancelled")
		}
	})
}

func TestPolicy(t *testing.T) {
	valid := `{"operationsOrganization":"operations",
	 "agentRuntime":{"urlTemplate":"http://kagent-controller.{organization}.svc.cluster.local:8083/agents/{organization}/{agent}"},
	 "judges":{"correlator":{"agent":"correlator","lane":"deciding","deadlineSeconds":150,"urgentDeadlineSeconds":30,"tokenTier":"viewer","tokenTTLSeconds":1800},
	           "investigator":{"agent":"investigator","lane":"deciding","deadlineSeconds":300,"tokenTier":"viewer","tokenTTLSeconds":1800},
	           "scribe":{"agent":"scribe","lane":"background","deadlineSeconds":600,"tokenTier":"viewer","tokenTTLSeconds":1800}},
	 "lanes":{"deciding":2,"background":1},
	 "batchSeconds":60,"urgentBatchSeconds":15,
	 "turnBudget":{"turns":120,"windowSeconds":3600}}`
	p, err := ParsePolicy([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.AgentRuntime.URL("acme", "scribe"); got != "http://kagent-controller.acme.svc.cluster.local:8083/agents/acme/scribe" {
		t.Fatalf("url %s", got)
	}
	if b := p.Batches(); b.Batch != time.Minute || b.UrgentBatch != 15*time.Second {
		t.Fatalf("batches %+v", b)
	}
	empty := p
	empty.Judges = map[string]Judge{}
	badName := p
	badName.Judges = map[string]Judge{"Correlator": p.Judges["correlator"], "scribe": p.Judges["scribe"]}
	for name, q := range map[string]Policy{"no judges": empty, "a judge name in capitals": badName} {
		if q.Validate() == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	cases := []struct{ name, from, to string }{
		{"unknown field", `"lanes"`, `"lane":1,"lanes"`},
		{"two documents", `"turnBudget":{"turns":120,"windowSeconds":3600}}`, `"turnBudget":{"turns":120,"windowSeconds":3600}} {}`},
		{"operations Organization missing", `"operations"`, `""`},
		{"template without agent", `/{agent}"`, `/x"`},
		{"template not http", `"http://kagent`, `"ftp://kagent`},
		{"bad agent name", `"agent":"scribe"`, `"agent":"Scribe"`},
		{"unknown lane", `"lane":"background"`, `"lane":"fast"`},
		{"deadline too short", `"deadlineSeconds":150`, `"deadlineSeconds":9`},
		{"deadline too long", `"deadlineSeconds":600`, `"deadlineSeconds":1801`},
		{"urgent deadline above the deadline", `"urgentDeadlineSeconds":30`, `"urgentDeadlineSeconds":151`},
		{"tier admin", `"urgentDeadlineSeconds":30,"tokenTier":"viewer"`, `"urgentDeadlineSeconds":30,"tokenTier":"admin"`},
		{"token life below the deadline", `"deadlineSeconds":600,"tokenTier":"viewer","tokenTTLSeconds":1800`, `"deadlineSeconds":600,"tokenTier":"viewer","tokenTTLSeconds":599`},
		{"lane without turns", `"background":1`, `"background":0`},
		{"lane without a judge", `"lane":"background"`, `"lane":"deciding"`},
		{"unknown lane name", `"background":1`, `"background":1,"fast":1`},
		{"urgent batch longer", `"urgentBatchSeconds":15`, `"urgentBatchSeconds":61`},
		{"no turn budget", `"turns":120`, `"turns":0`},
		{"turn window too short", `"windowSeconds":3600`, `"windowSeconds":59`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if strings.Count(valid, c.from) != 1 {
				t.Fatalf("%q occurs %d times", c.from, strings.Count(valid, c.from))
			}
			if _, err := ParsePolicy([]byte(strings.Replace(valid, c.from, c.to, 1))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
