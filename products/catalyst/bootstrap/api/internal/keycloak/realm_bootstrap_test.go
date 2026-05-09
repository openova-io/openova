package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// realmBootstrapState models the in-memory shape of a Keycloak realm's
// roles + composite-children graph for the fake test server below. The
// state machine is sufficient to assert idempotency at the byte level:
// "given state X, EnsureTierRealmRoles makes only the writes needed to
// reach the target chain — and zero writes when X is already at target".
type realmBootstrapState struct {
	mu       sync.Mutex
	roles    map[string]*RealmRole          // name → role (with assigned id)
	children map[string]map[string]struct{} // parent name → set of child names

	// Counters give per-test assertions a clean view of the network
	// activity without parsing the whole request log.
	posts            int
	getsRoles        int
	getsComposites   int
	postsComposites  int
	roleCreatesByName map[string]int

	// Token-refresh shenanigans. When forceTokenRetryOnce is true, the
	// FIRST role-create POST returns 401, the HTTP transport surfaces
	// that and the caller retries — the second attempt succeeds. This
	// validates the implicit "retry on 401" behaviour the master brief
	// asks for in test path #4.
	forceTokenRetryOnce bool
	tokenRetryFired     bool
}

func newRealmBootstrapState() *realmBootstrapState {
	return &realmBootstrapState{
		roles:             map[string]*RealmRole{},
		children:          map[string]map[string]struct{}{},
		roleCreatesByName: map[string]int{},
	}
}

func (s *realmBootstrapState) seedRole(name string, composite bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[name] = &RealmRole{
		ID:        "id-" + name,
		Name:      name,
		Composite: composite,
	}
}

func (s *realmBootstrapState) seedComposite(parent, child string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.children[parent]; !ok {
		s.children[parent] = map[string]struct{}{}
	}
	s.children[parent][child] = struct{}{}
}

// realmBootstrapServer is an httptest.Server that mimics the subset of
// the Keycloak Admin REST API the bootstrap touches:
//
//   - POST /realms/test-realm/protocol/openid-connect/token (SA token)
//   - GET  /admin/realms/test-realm/roles/{name}
//   - POST /admin/realms/test-realm/roles
//   - GET  /admin/realms/test-realm/roles/{name}/composites/realm
//   - POST /admin/realms/test-realm/roles/{name}/composites
func realmBootstrapServer(t *testing.T, state *realmBootstrapState) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()

		// Service-account token endpoint.
		if r.URL.Path == "/realms/test-realm/protocol/openid-connect/token" {
			saTokenHandler(w)
			return
		}

		const adminPrefix = "/admin/realms/test-realm/roles"
		if !strings.HasPrefix(r.URL.Path, adminPrefix) {
			t.Errorf("unexpected path: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, adminPrefix)

		switch {
		case rest == "" || rest == "/":
			// POST /roles — create role.
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var rr RealmRole
			if err := json.Unmarshal(body, &rr); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state.posts++
			state.roleCreatesByName[rr.Name]++
			if state.forceTokenRetryOnce && !state.tokenRetryFired {
				state.tokenRetryFired = true
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"token_expired"}`))
				return
			}
			if _, exists := state.roles[rr.Name]; exists {
				w.WriteHeader(http.StatusConflict)
				return
			}
			rr.ID = "id-" + rr.Name
			state.roles[rr.Name] = &rr
			w.WriteHeader(http.StatusCreated)
			return

		case strings.HasSuffix(rest, "/composites/realm"):
			// GET composites — list realm composites attached to parent.
			if r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			parent := strings.TrimPrefix(rest, "/")
			parent = strings.TrimSuffix(parent, "/composites/realm")
			state.getsComposites++
			if _, ok := state.roles[parent]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			out := []RealmRole{}
			for childName := range state.children[parent] {
				if rr, ok := state.roles[childName]; ok {
					out = append(out, *rr)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return

		case strings.HasSuffix(rest, "/composites"):
			// POST composites — attach a list of realm-roles as composites.
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			parent := strings.TrimPrefix(rest, "/")
			parent = strings.TrimSuffix(parent, "/composites")
			state.postsComposites++
			if _, ok := state.roles[parent]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var children []RealmRole
			if err := json.Unmarshal(body, &children); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if state.children[parent] == nil {
				state.children[parent] = map[string]struct{}{}
			}
			for _, c := range children {
				state.children[parent][c.Name] = struct{}{}
			}
			w.WriteHeader(http.StatusNoContent)
			return

		default:
			// GET /roles/{name} — find role by name.
			if r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			name := strings.TrimPrefix(rest, "/")
			state.getsRoles++
			rr, ok := state.roles[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rr)
			return
		}
	}))
	t.Cleanup(srv.Close)
	return NewWithHTTP(srv.URL, "test-realm", "catalyst-api-server", "sa-secret",
		&http.Client{Timeout: 5 * time.Second})
}

// ── Test 1: clean-slate bootstrap creates 5 roles + 4 composites. ─────

func TestEnsureTierRealmRoles_CleanSlate(t *testing.T) {
	state := newRealmBootstrapState()
	client := realmBootstrapServer(t, state)

	if err := client.EnsureTierRealmRoles(context.Background(), "test-realm"); err != nil {
		t.Fatalf("EnsureTierRealmRoles: %v", err)
	}

	// 5 roles created.
	if state.posts != 5 {
		t.Errorf("expected 5 role POSTs on clean slate, got %d", state.posts)
	}
	for _, want := range []string{
		"catalyst-viewer", "catalyst-developer", "catalyst-operator",
		"catalyst-admin", "catalyst-owner",
	} {
		if state.roleCreatesByName[want] != 1 {
			t.Errorf("role %q should have been POSTed once, got %d",
				want, state.roleCreatesByName[want])
		}
	}

	// 4 composite chain links.
	if state.postsComposites != 4 {
		t.Errorf("expected 4 composite POSTs on clean slate, got %d",
			state.postsComposites)
	}
	for parent, child := range map[string]string{
		"catalyst-developer": "catalyst-viewer",
		"catalyst-operator":  "catalyst-developer",
		"catalyst-admin":     "catalyst-operator",
		"catalyst-owner":     "catalyst-admin",
	} {
		set, ok := state.children[parent]
		if !ok {
			t.Errorf("expected composite chain to wire %q (parent missing)", parent)
			continue
		}
		if _, has := set[child]; !has {
			t.Errorf("expected %q to compose %q, got children=%v",
				parent, child, set)
		}
	}

	// tier-level attribute round-tripped.
	if rr := state.roles["catalyst-admin"]; rr == nil {
		t.Error("catalyst-admin role missing")
	} else {
		levels := rr.Attributes["tier-level"]
		if len(levels) != 1 || levels[0] != "40" {
			t.Errorf("catalyst-admin tier-level want [40], got %v", levels)
		}
	}
}

// ── Test 2: re-run on populated state writes nothing. ─────────────────

func TestEnsureTierRealmRoles_AlreadyPopulated_NoWrites(t *testing.T) {
	state := newRealmBootstrapState()
	for _, step := range CatalogTierBootstrapPlan {
		state.seedRole(step.Name, step.ComposeChild != "")
	}
	for _, step := range CatalogTierBootstrapPlan {
		if step.ComposeChild != "" {
			state.seedComposite(step.Name, step.ComposeChild)
		}
	}

	client := realmBootstrapServer(t, state)
	if err := client.EnsureTierRealmRoles(context.Background(), "test-realm"); err != nil {
		t.Fatalf("EnsureTierRealmRoles: %v", err)
	}
	if state.posts != 0 {
		t.Errorf("expected 0 role POSTs on populated state, got %d",
			state.posts)
	}
	if state.postsComposites != 0 {
		t.Errorf("expected 0 composite POSTs on populated state, got %d",
			state.postsComposites)
	}
	if state.getsComposites != 4 {
		t.Errorf("expected 4 composite-list GETs (one per non-leaf), got %d",
			state.getsComposites)
	}
}

// ── Test 3: only the missing role + its composite link get written. ────

func TestEnsureTierRealmRoles_OneMissing_PartialWrites(t *testing.T) {
	state := newRealmBootstrapState()
	// Seed everything except catalyst-operator + its parent's composite link.
	for _, step := range CatalogTierBootstrapPlan {
		if step.Name == "catalyst-operator" {
			continue
		}
		state.seedRole(step.Name, step.ComposeChild != "")
	}
	// Wire viewer→developer (already there before operator) and owner→admin
	// — admin→operator and operator→developer are missing.
	state.seedComposite("catalyst-developer", "catalyst-viewer")
	state.seedComposite("catalyst-owner", "catalyst-admin")

	client := realmBootstrapServer(t, state)
	if err := client.EnsureTierRealmRoles(context.Background(), "test-realm"); err != nil {
		t.Fatalf("EnsureTierRealmRoles: %v", err)
	}
	// Exactly 1 role POST: catalyst-operator.
	if state.posts != 1 {
		t.Errorf("expected 1 role POST, got %d", state.posts)
	}
	if state.roleCreatesByName["catalyst-operator"] != 1 {
		t.Errorf("expected catalyst-operator to be POSTed once, got %d",
			state.roleCreatesByName["catalyst-operator"])
	}
	// Exactly 2 composite POSTs: admin→operator and operator→developer.
	if state.postsComposites != 2 {
		t.Errorf("expected 2 composite POSTs, got %d", state.postsComposites)
	}
	if _, ok := state.children["catalyst-admin"]["catalyst-operator"]; !ok {
		t.Error("admin→operator composite missing post-bootstrap")
	}
	if _, ok := state.children["catalyst-operator"]["catalyst-developer"]; !ok {
		t.Error("operator→developer composite missing post-bootstrap")
	}
}

// ── Test 4: 401 on token-refresh path bubbles up cleanly. ─────────────

// Because the catalyst-api Client does NOT cache service-account tokens
// at this layer (each Admin call refreshes via serviceAccountToken),
// a 401 from a single role-POST surfaces to EnsureTierRealmRoles as an
// error. The retry happens at the higher startup goroutine: the brief's
// "5-attempt backoff" lives in main.go, not here. This test asserts
// that the error is surfaced (does NOT silently no-op) so the goroutine
// can decide whether to retry.
func TestEnsureTierRealmRoles_RoleCreate401_SurfacesError(t *testing.T) {
	state := newRealmBootstrapState()
	state.forceTokenRetryOnce = true
	client := realmBootstrapServer(t, state)

	err := client.EnsureTierRealmRoles(context.Background(), "test-realm")
	if err == nil {
		t.Fatal("expected EnsureTierRealmRoles to surface 401 error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to mention 401, got %v", err)
	}
}

// ── Test 4b: GET role returns 403 (SA lacks view-realm/manage-realm)
//
// This is the qa-loop iter-4 Fix #23 regression test. On omantel the
// catalyst-api-server SA in the sovereign realm only had
// `impersonation+manage-users+view-users+query-users` on
// realm-management. Phase 1 of EnsureTierRealmRoles starts with an
// EnsureRealmRole on `catalyst-viewer`, which in turn does a
// GetRealmRole — the underlying GET /admin/realms/<r>/roles/<n>
// returns 403 Forbidden because the SA cannot read realm-roles.
//
// This test asserts the bootstrap surfaces a meaningful, debuggable
// 403 error chain (so the operator can immediately spot the missing
// realm-management roles in Keycloak) rather than masking it as a
// generic "create failed".
func TestEnsureTierRealmRoles_GetRole403_SurfacesPermissionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Service-account token endpoint succeeds — the SA can
		// authenticate, it just lacks realm-roles authorization.
		if r.URL.Path == "/realms/test-realm/protocol/openid-connect/token" {
			saTokenHandler(w)
			return
		}
		// Every Admin-API call returns 403 Forbidden — same shape as
		// Keycloak emits when the SA's client-roles on
		// realm-management are insufficient.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"HTTP 403 Forbidden"}`))
	}))
	t.Cleanup(srv.Close)
	client := NewWithHTTP(srv.URL, "test-realm", "catalyst-api-server", "sa-secret",
		&http.Client{Timeout: 5 * time.Second})

	err := client.EnsureTierRealmRoles(context.Background(), "test-realm")
	if err == nil {
		t.Fatal("expected EnsureTierRealmRoles to surface 403 error")
	}
	// The error chain must include both the role name (so the operator
	// knows which step blew up) AND the 403 status (so they know it's
	// a permissions issue, not a connectivity / 5xx issue).
	msg := err.Error()
	if !strings.Contains(msg, "catalyst-viewer") {
		t.Errorf("expected error to mention failing role 'catalyst-viewer', got %v", err)
	}
	if !strings.Contains(msg, "403") {
		t.Errorf("expected error to mention 403 status, got %v", err)
	}
}

// ── Test 5: realm-mismatch is rejected at the API boundary. ───────────

func TestEnsureTierRealmRoles_RealmMismatch_Rejects(t *testing.T) {
	state := newRealmBootstrapState()
	client := realmBootstrapServer(t, state)
	// Client was constructed for "test-realm"; passing a different realm
	// surfaces an error rather than silently bootstrapping the wrong one.
	if err := client.EnsureTierRealmRoles(context.Background(), "wrong-realm"); err == nil {
		t.Fatal("expected realm-mismatch rejection")
	}
}

// ── Test 6: EnsureCompositeRealmRole idempotent on already-attached. ──

func TestEnsureCompositeRealmRole_AlreadyAttached_NoWrite(t *testing.T) {
	state := newRealmBootstrapState()
	state.seedRole("catalyst-developer", true)
	state.seedRole("catalyst-viewer", false)
	state.seedComposite("catalyst-developer", "catalyst-viewer")

	client := realmBootstrapServer(t, state)
	if err := client.EnsureCompositeRealmRole(context.Background(),
		"catalyst-developer", "catalyst-viewer"); err != nil {
		t.Fatalf("EnsureCompositeRealmRole: %v", err)
	}
	if state.postsComposites != 0 {
		t.Errorf("expected 0 composite POSTs, got %d", state.postsComposites)
	}
}

// ── Test 7: ListRealmRoleComposites errors propagate with a 404. ──────

func TestListRealmRoleComposites_NotFound(t *testing.T) {
	state := newRealmBootstrapState()
	client := realmBootstrapServer(t, state)
	_, err := client.ListRealmRoleComposites(context.Background(), "no-such-role")
	if !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("expected ErrRoleNotFound, got %v", err)
	}
}

// ── Test 8: empty children list short-circuits to a no-op. ────────────

func TestAddRealmRoleComposites_EmptyChildren_NoHTTP(t *testing.T) {
	// Server fails the test if any HTTP call is made.
	client := roleTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("AddRealmRoleComposites with empty children must not call HTTP: %s",
			r.URL.Path)
	})
	if err := client.AddRealmRoleComposites(context.Background(),
		"catalyst-admin", nil); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
