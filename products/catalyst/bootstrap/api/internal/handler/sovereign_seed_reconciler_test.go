package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// rwKVServer is a fake OpenBao KV-v2 backend that answers BOTH the GET
// (read-before-write existence check) and the PUT (seed write) the #4877
// reconciler issues. GETs resolve from a canned map keyed by KV data-path
// ("catalyst/anthropic/token", …); an absent key returns 404 so
// openbaoPathHasProperty reports the #4877 gap. Every PUT is recorded so a test
// can assert exactly which paths the reconciler (re)seeded — and, crucially,
// that a healthy path is NEVER re-written (no churn).
type rwKVServer struct {
	mu       sync.Mutex
	present  map[string]map[string]any // data-path → KV data (what a GET returns)
	putPaths []string                  // KV data-paths that received a PUT
	srv      *httptest.Server
}

func newRWKVServer(t *testing.T) *rwKVServer {
	t.Helper()
	s := &rwKVServer{present: map[string]map[string]any{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// KV-v2 data URL: /v1/<mount>/data/<secretPath>.
		const marker = "/data/"
		idx := strings.Index(r.URL.Path, marker)
		dataPath := ""
		if idx >= 0 {
			dataPath = r.URL.Path[idx+len(marker):]
		}
		switch r.Method {
		case http.MethodGet:
			s.mu.Lock()
			data, ok := s.present[dataPath]
			s.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[]}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data}})
		case http.MethodPut, http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var parsed struct {
				Data map[string]any `json:"data"`
			}
			_ = json.Unmarshal(body, &parsed)
			s.mu.Lock()
			s.putPaths = append(s.putPaths, dataPath)
			// A written path becomes present for any subsequent GET.
			s.present[dataPath] = parsed.Data
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"version":1}}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *rwKVServer) seedPresent(dataPath string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.present[dataPath] = data
}

func (s *rwKVServer) writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.putPaths))
	copy(out, s.putPaths)
	return out
}

func (s *rwKVServer) client() *openbao.Client {
	return &openbao.Client{Addr: s.srv.URL, Token: "test-token", HTTP: s.srv.Client()}
}

// Test_openbaoPathHasProperty covers the read-before-write existence check:
// absent path → false, present-with-value → true, present-but-empty → false.
func Test_openbaoPathHasProperty(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "sk-ant-x", "credentialsJson": ""})
	srv.seedPresent("catalyst/agenity/hollow/mcp-bearer", map[string]any{"bearer": "  "})

	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	ctx := context.Background()

	// Present with a non-empty property.
	if ok, err := h.openbaoPathHasProperty(ctx, "secret", "catalyst/anthropic/token", "apiKey", "credentialsJson"); err != nil || !ok {
		t.Errorf("anthropic present: got (%v,%v), want (true,nil)", ok, err)
	}
	// Absent path.
	if ok, err := h.openbaoPathHasProperty(ctx, "secret", "catalyst/newapi/admin-token", "ADMIN_API_TOKEN"); err != nil || ok {
		t.Errorf("newapi absent: got (%v,%v), want (false,nil)", ok, err)
	}
	// Present but the only property is whitespace → treated as empty (re-seed).
	if ok, err := h.openbaoPathHasProperty(ctx, "secret", "catalyst/agenity/hollow/mcp-bearer", "bearer"); err != nil || ok {
		t.Errorf("hollow bearer: got (%v,%v), want (false,nil)", ok, err)
	}
}

// Test_runSeedReconcilePass_NoChurnWhenAllPresent is the safety guarantee: when
// every global path already holds a valid value, the pass issues ZERO PUTs — a
// healthy Sovereign is never re-written (no OpenBao version churn, no downstream
// Secret/pod churn). The per-Org leg is a no-op here (no dynamic client → zero
// Orgs), which is asserted implicitly by the total write count being 0.
func Test_runSeedReconcilePass_NoChurnWhenAllPresent(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "bridge-secret"})
	srv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "sk-ant-x", "credentialsJson": ""})

	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	// #5956: the anthropic leg now probes the Anthropic API for VALIDITY, so
	// the probe seam must be pinned or this unit test would dial the real
	// api.anthropic.com. A 200 makes the stored credential healthy, which is
	// what "all present" is supposed to mean here.
	stubAnthropicProbe(t, h, http.StatusOK, modelsEnvelope)

	h.runSeedReconcilePass(context.Background())

	if got := srv.writes(); len(got) != 0 {
		t.Fatalf("expected ZERO writes on an all-present pass (no churn), got %v", got)
	}
}

// Test_runSeedReconcilePass_SelfHealsAbsentGlobalSeed proves the #4877 fix: an
// absent global path IS re-seeded. Anthropic is the cleanly-unit-testable
// global seed (its value is env-sourced, no in-cluster Secret read), so we
// assert it gets written exactly once when its path is absent.
func Test_runSeedReconcilePass_SelfHealsAbsentGlobalSeed(t *testing.T) {
	srv := newRWKVServer(t)
	// newapi path present (so that seed no-ops and can't reach rest.InClusterConfig);
	// anthropic path deliberately ABSENT → must self-heal.
	srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "bridge-secret"})

	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	stubAnthropicProbe(t, h, http.StatusOK, modelsEnvelope) // #5956 — keep the pass hermetic
	t.Setenv(anthropicSeedAPIKeyEnv, "sk-ant-selfheal")
	t.Setenv(anthropicSeedCredentialsJSONEnv, "")

	h.runSeedReconcilePass(context.Background())

	writes := srv.writes()
	found := false
	for _, p := range writes {
		if p == "catalyst/anthropic/token" {
			found = true
		}
		if p == "catalyst/newapi/admin-token" {
			t.Errorf("newapi path was present but got re-written (churn): %v", writes)
		}
	}
	if !found {
		t.Fatalf("expected a self-heal write to catalyst/anthropic/token, got writes=%v", writes)
	}
}

// Test_runSeedReconcilePass_PerOrgMCPBearerSelfHeal proves the per-Org
// enumeration: the reconciler lists every Organization CR (orgResponsesFromCRs)
// and self-heals the absent per-slug mcp-bearer path for each — while leaving an
// Org whose bearer already exists untouched (no churn).
func Test_runSeedReconcilePass_PerOrgMCPBearerSelfHeal(t *testing.T) {
	// Two funnel Orgs (parentDomain set → both carry a pool console host).
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("acme", "Acme", "omani.homes", "owner@acme.omani.homes", "Ready"),
		orgReadyCR("globex", "Globex", "omani.homes", "owner@globex.omani.homes", "Ready"),
	)
	srv := newRWKVServer(t)
	h.openbao = srv.client()
	h.handoverSigner = newTestSigner(t)
	// Make the two GLOBAL paths present so this test isolates the per-Org leg.
	srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "bridge-secret"})
	srv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "sk-ant-x"})
	stubAnthropicProbe(t, h, http.StatusOK, modelsEnvelope) // #5956 — keep the pass hermetic
	// globex ALREADY has a healthy bearer → must NOT be re-written; acme is absent.
	srv.seedPresent("catalyst/agenity/globex/mcp-bearer", map[string]any{"bearer": "existing-bearer"})

	h.runSeedReconcilePass(context.Background())

	writes := srv.writes()
	wantAcme := "catalyst/agenity/acme/mcp-bearer"
	dontWantGlobex := "catalyst/agenity/globex/mcp-bearer"
	sawAcme := false
	for _, p := range writes {
		if p == wantAcme {
			sawAcme = true
		}
		if p == dontWantGlobex {
			t.Errorf("globex bearer was present but got re-written (churn): %v", writes)
		}
	}
	if !sawAcme {
		t.Fatalf("expected a self-heal write to %s, got writes=%v", wantAcme, writes)
	}
}

// Test_StartSovereignSeedReconciler_DormantWhenNoOpenBao verifies the
// orchestrator guard: nil OpenBao client ⇒ the reconciler stays dormant and
// runSeedReconcilePass is a no-op (no panic, no write attempt).
func Test_StartSovereignSeedReconciler_DormantWhenNoOpenBao(t *testing.T) {
	h := &Handler{log: silentLogger()}
	// h.openbao deliberately nil.
	h.StartSovereignSeedReconciler(context.Background()) // must return immediately, no panic.
	h.runSeedReconcilePass(context.Background())          // must no-op, no panic.
}
