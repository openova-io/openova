package httpapi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/auth"
	"github.com/openova-io/openova/products/cortex/internal/events"
	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/ingest"
	"github.com/openova-io/openova/products/cortex/internal/search"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// unrouted is the closed list of paths served without the token check. They are served by
// Health on the cluster-internal listener and never routed.
var unrouted = []string{"GET /healthz", "GET /readyz", "GET /metrics"}

const issuer = "https://auth.acme.example/realms/acme"

// marker is a search text that must never reach the log.
const marker = "quarterly-figures-7f3a"

type memStore struct {
	store.Store
	searched []string
	queued   []store.Job
}

func (m *memStore) ReadableSources(context.Context, []string) ([]string, error) {
	return []string{"hr"}, nil
}
func (m *memStore) Search(_ context.Context, p, s []string, v []float32, n int) ([]store.Hit, error) {
	m.searched = p
	return []store.Hit{{ChunkID: 7, DocumentID: "d1", SourceID: "hr", Page: 3, Text: "Leave accrues monthly.", Score: 0.5}}, nil
}
func (m *memStore) Documents(context.Context, []string, []string) ([]store.Document, error) {
	return []store.Document{{ID: "d1", SourceID: "hr", Title: "Leave policy", URI: "https://docs.acme.example/leave"}}, nil
}
func (m *memStore) Sources(context.Context, []string) ([]store.SourceStatus, error) {
	return []store.SourceStatus{{ID: "hr", Title: "HR policies", State: "idle", Documents: 2, Chunks: 9}}, nil
}

type fixture struct {
	srv     *Server
	store   *memStore
	key     *rsa.PrivateKey
	log     *bytes.Buffer
	gwFail  *bool
	unready error
	objects memBucket
	hub     *events.Hub
}

// newFixture builds a server with the real token check and the real gateway client in front
// of a gateway that repeats the text it was sent, in its answer and in its errors.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: &memStore{}, key: key, log: &bytes.Buffer{}, gwFail: new(bool), objects: memBucket{}, hub: events.NewHub()}
	set, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1",
		"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	checker, err := auth.New(auth.Config{Issuer: issuer, Keys: func(context.Context) ([]byte, error) { return set, nil }})
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if *f.gwFail {
			http.Error(w, "cannot embed "+strings.Join(in.Input, " "), http.StatusBadGateway)
			return
		}
		vec := make([]float32, 64)
		vec[0] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": vec}}, "echo": in.Input})
	}))
	t.Cleanup(gw.Close)
	keyFile := filepath.Join(t.TempDir(), "model-key")
	if err := os.WriteFile(keyFile, []byte("vk"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := gateway.New(gw.URL+"/v1", keyFile, gw.Client())
	if err != nil {
		t.Fatal(err)
	}
	f.srv = New(Config{Auth: checker, Store: f.store, Search: &search.Service{Store: f.store, Embedder: client.Embedder("embed-default", 64)},
		Uploads: &ingest.Intake{Objects: f.objects, Queue: f.store}, Originals: f.objects, Hub: f.hub, MaxUpload: 64,
		Heartbeat: 50 * time.Millisecond, StreamFor: 300 * time.Millisecond,
		Ready: func(context.Context) error { return f.unready },
		Log:   slog.New(slog.NewJSONHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	return f
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// token signs claims over the defaults of a valid token; a nil value removes a claim.
func (f *fixture) token(t *testing.T, more map[string]any) string {
	t.Helper()
	claims := map[string]any{"iss": issuer, "aud": "cortex", "sub": "u-1", "exp": time.Now().Add(time.Hour).Unix(), "groups": []string{"staff"}}
	for k, v := range more {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
	c, _ := json.Marshal(claims)
	signed := b64(h) + "." + b64(c)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64(sig)
}

func (f *fixture) call(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func split(pattern string) (string, string) {
	m, p, _ := strings.Cut(pattern, " ")
	return m, p
}

// TestEveryRouteAuthenticated walks every route of the router. Each refuses a call without a
// token, with an expired token, with a token of another audience and with a token that is not
// a JWS with 401 unauthenticated, and answers a valid token with something else. A path that is
// no route needs a token too. The paths served without the check are exactly the closed list
// unrouted, on the other handler.
func TestEveryRouteAuthenticated(t *testing.T) {
	f := newFixture(t)
	if got := f.srv.Unrouted(); !reflect.DeepEqual(got, unrouted) {
		t.Fatalf("the unrouted paths are %v, want exactly %v", got, unrouted)
	}
	routes := f.srv.Routes()
	if len(routes) == 0 {
		t.Fatal("the router has no route")
	}
	bad := map[string]string{"no token": "", "an expired token": f.token(t, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}),
		"another audience": f.token(t, map[string]any{"aud": "account"}), "not a JWS": "abc"}
	for _, pattern := range append(routes, "GET /v1/nothing", "POST /mcp", "GET /healthz", "GET /metrics") {
		method, path := split(pattern)
		for what, token := range bad {
			w := f.call(t, f.srv.Handler(), method, path, token, `{"query":"q"}`)
			if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"unauthenticated"`) || w.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("%s with %s: %d %s", pattern, what, w.Code, w.Body)
			}
		}
		if !slices.Contains(routes, pattern) {
			continue
		}
		if w := f.call(t, f.srv.Handler(), method, path, f.token(t, nil), `{"query":"q"}`); w.Code == http.StatusUnauthorized {
			t.Errorf("%s refused a valid token: %s", pattern, w.Body)
		}
		// A route refuses a call that reaches it past the check.
		if w := f.call(t, f.srv.mux, method, path, "", `{"query":"q"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a caller", pattern, w.Code)
		}
	}
	for _, pattern := range unrouted {
		method, path := split(pattern)
		if w := f.call(t, f.srv.Health(), method, path, "", ""); w.Code != http.StatusOK {
			t.Errorf("%s on the internal listener: %d", pattern, w.Code)
		}
	}
	if w := f.call(t, f.srv.Health(), http.MethodPost, "/v1/search", "", `{"query":"q"}`); w.Code != http.StatusNotFound {
		t.Errorf("the internal listener serves the search: %d", w.Code)
	}
}

func TestSearchRoute(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, nil)
	w := f.call(t, f.srv.Handler(), "POST", "/v1/search", tok, `{"query":"leave","sources":["hr"],"topK":3,"rerank":false}`)
	want := `{"results":[{"documentId":"d1","sourceId":"hr","title":"Leave policy","uri":"https://docs.acme.example/leave","chunk":{"id":7,"text":"Leave accrues monthly.","page":3,"score":0.5}}]}`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if !reflect.DeepEqual(f.store.searched, []string{"user:u-1", "group:staff", "org:members"}) {
		t.Fatalf("the store searched for %v", f.store.searched)
	}
	for body, status := range map[string]int{`{"query":""}`: 400, `{"query":"q","topK":99}`: 400, `{"query":"q","where":"1=1"}`: 400,
		`{"query":"q"}{}`: 400, `{"query":"q"}}`: 400, `{"query":"q"}]`: 400, `{"query":"q"} x`: 400,
		`not json`: 400, `{"query":"q","sources":["finance"]}`: 403} {
		if w := f.call(t, f.srv.Handler(), "POST", "/v1/search", tok, body); w.Code != status {
			t.Errorf("%s: %d %s, want %d", body, w.Code, w.Body, status)
		}
	}
	*f.gwFail = true
	if w := f.call(t, f.srv.Handler(), "POST", "/v1/search", tok, `{"query":"leave"}`); w.Code != 503 || !strings.Contains(w.Body.String(), "gateway-unavailable") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestSourcesRoute(t *testing.T) {
	f := newFixture(t)
	w := f.call(t, f.srv.Handler(), "GET", "/v1/sources", f.token(t, nil), "")
	want := `{"items":[{"id":"hr","title":"HR policies","state":"idle","documents":2,"chunks":9,"lastSyncAt":null,"lastError":null}]}`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	f := newFixture(t)
	f.call(t, f.srv.Handler(), "GET", "/v1/sources", f.token(t, nil), "")
	f.call(t, f.srv.Handler(), "GET", "/v1/sources", "", "")
	w := f.call(t, f.srv.Health(), "GET", "/metrics", "", "")
	for _, line := range []string{`cortex_http_requests_total{route="GET /v1/sources",code="200"} 1`, `cortex_http_requests_total{route="unauthenticated",code="401"} 1`} {
		if !strings.Contains(w.Body.String(), line+"\n") {
			t.Errorf("the metrics have no line %s:\n%s", line, w.Body)
		}
	}
	f.unready = errors.New("the database is away")
	if w := f.call(t, f.srv.Health(), "GET", "/readyz", "", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz with the database away: %d", w.Code)
	}
}

// TestNoQueryTextIsLogged runs searches that succeed, fail at the gateway (which repeats the
// text in its error), are refused and are invalid, at the debug level, and finds the text in
// no log line.
func TestNoQueryTextIsLogged(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, nil)
	bodies := []string{`{"query":"` + marker + `"}`, `{"query":"` + marker + `","sources":["finance"]}`,
		`{"query":"` + marker + `","topK":0}`, `{"query":"` + marker + `","bad":1}`}
	for _, body := range bodies {
		f.call(t, f.srv.Handler(), "POST", "/v1/search?q="+marker, tok, body)
		f.call(t, f.srv.Handler(), "POST", "/v1/search?q="+marker, "", body)
	}
	*f.gwFail = true
	if w := f.call(t, f.srv.Handler(), "POST", "/v1/search", tok, bodies[0]); w.Code != 503 {
		t.Fatalf("the failing gateway gave %d", w.Code)
	}
	if lines := strings.Count(f.log.String(), "\n"); lines < len(bodies)*2+1 || !strings.Contains(f.log.String(), "the gateway failed") {
		t.Fatalf("the log has %d lines; the test logged nothing to judge:\n%s", lines, f.log)
	}
	if strings.Contains(f.log.String(), marker) || strings.Contains(f.log.String(), "Leave accrues") {
		t.Fatalf("the log holds request or passage text:\n%s", f.log)
	}
}
