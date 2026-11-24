package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// rwKVServer is a fake OpenBao KV-v2 backend that answers BOTH the GET
// (read-before-write existence check) and the PUT (seed write) the #4877
// reconciler issues. GETs resolve from a canned map keyed by KV data-path
// ("catalyst/anthropic/token", …); an absent key returns 404 so
// openbaoPathIsHealthy reports the #4877 gap. Every PUT is recorded so a test
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

// Test_openbaoPathIsHealthy_Presence covers the read-before-write existence
// check with no predicate: absent path → false, present-with-value → true,
// present-but-empty → false.
func Test_openbaoPathIsHealthy_Presence(t *testing.T) {
	srv := newRWKVServer(t)
	// #6250: an apiKey with an EMPTY credentialsJson is NOT a healthy path —
	// claude-code authenticates from the OAuth blob. A genuinely all-present
	// pass needs a credential that can authenticate.
	srv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "FAKE-not-a-real-api-key", "credentialsJson": validAnthropicCredentialsJSON()})
	srv.seedPresent("catalyst/newapi/hollow", map[string]any{"ADMIN_API_TOKEN": "  "})

	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()
	ctx := context.Background()

	// Present with a non-empty property.
	if ok, err := h.openbaoPathIsHealthy(ctx, "secret", "catalyst/anthropic/token", []string{"apiKey", "credentialsJson"}, nil); err != nil || !ok {
		t.Errorf("anthropic present: got (%v,%v), want (true,nil)", ok, err)
	}
	// Absent path.
	if ok, err := h.openbaoPathIsHealthy(ctx, "secret", "catalyst/newapi/admin-token", []string{"ADMIN_API_TOKEN"}, nil); err != nil || ok {
		t.Errorf("newapi absent: got (%v,%v), want (false,nil)", ok, err)
	}
	// Present but the only property is whitespace → treated as empty (re-seed).
	if ok, err := h.openbaoPathIsHealthy(ctx, "secret", "catalyst/newapi/hollow", []string{"ADMIN_API_TOKEN"}, nil); err != nil || ok {
		t.Errorf("hollow token: got (%v,%v), want (false,nil)", ok, err)
	}
}

// Test_runSeedReconcilePass_NoChurnWhenAllPresent is the safety guarantee: when
// every global path already holds a valid value, the pass issues ZERO PUTs — a
// healthy Sovereign is never re-written (no OpenBao version churn, no downstream
// Secret/pod churn).
func Test_runSeedReconcilePass_NoChurnWhenAllPresent(t *testing.T) {
	srv := newRWKVServer(t)
	srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "bridge-secret"})
	// #6250: an apiKey with an EMPTY credentialsJson is NOT a healthy path —
	// claude-code authenticates from the OAuth blob. A genuinely all-present
	// pass needs a credential that can authenticate.
	srv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "FAKE-not-a-real-api-key", "credentialsJson": validAnthropicCredentialsJSON()})

	h := &Handler{log: silentLogger()}
	h.openbao = srv.client()

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
	t.Setenv(anthropicSeedAPIKeyEnv, "FAKE-not-a-real-api-key")
	t.Setenv(anthropicSeedCredentialsJSONEnv, validAnthropicCredentialsJSON())

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

// Test_runSeedReconcilePass_MintsNoOrgBearer proves AIP-156: the pass seeds no
// per-Organization path. Two Organizations are listed, neither has a tool
// bearer in OpenBao, and a signer is wired, so the deleted per-Org leg would
// have minted and written one bearer for each. Both global paths hold valid
// values, so the only correct number of writes is zero.
func Test_runSeedReconcilePass_MintsNoOrgBearer(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("acme", "Acme", "omani.homes", "owner@acme.omani.homes", "Ready"),
		orgReadyCR("globex", "Globex", "omani.homes", "owner@globex.omani.homes", "Ready"),
	)
	srv := newRWKVServer(t)
	h.openbao = srv.client()
	h.handoverSigner = newTestSigner(t)
	srv.seedPresent("catalyst/newapi/admin-token", map[string]any{"ADMIN_API_TOKEN": "bridge-secret"})
	srv.seedPresent("catalyst/anthropic/token", map[string]any{"apiKey": "FAKE-not-a-real-api-key", "credentialsJson": validAnthropicCredentialsJSON()})

	// Precondition: the fixture really lists the two Organizations; without
	// it a zero write count would prove nothing.
	orgs, _, err := h.orgResponsesFromCRs(context.Background())
	if err != nil {
		t.Fatalf("precondition: list Organizations: %v", err)
	}
	slugs := make([]string, 0, len(orgs))
	for _, o := range orgs {
		slugs = append(slugs, o.Subdomain)
	}
	sort.Strings(slugs)
	if strings.Join(slugs, ",") != "acme,globex" {
		t.Fatalf("precondition: the fixture lists Organizations %v, want [acme globex]", slugs)
	}

	h.runSeedReconcilePass(context.Background())

	if got := srv.writes(); len(got) != 0 {
		t.Fatalf("the pass wrote %v; it must write nothing (no per-Organization bearer, and both global paths are healthy)", got)
	}
}

// Test_StartSovereignSeedReconciler_DormantWhenNoOpenBao verifies the
// orchestrator guard: nil OpenBao client ⇒ the reconciler stays dormant and
// runSeedReconcilePass is a no-op (no panic, no write attempt).
func Test_StartSovereignSeedReconciler_DormantWhenNoOpenBao(t *testing.T) {
	h := &Handler{log: silentLogger()}
	// h.openbao deliberately nil.
	h.StartSovereignSeedReconciler(context.Background()) // must return immediately, no panic.
	h.runSeedReconcilePass(context.Background())         // must no-op, no panic.
}
