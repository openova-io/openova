package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

// roundTripFunc lets a test stand in for the live catalyst-api.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func orgIdentity(org, dep string, tier identity.Tier) *identity.Identity {
	return &identity.Identity{
		Claims:       &sharedauth.Claims{OrgID: org, DeploymentID: dep},
		Context:      identity.ContextOrganization,
		Tier:         tier,
		OrgID:        org,
		DeploymentID: dep,
		RawBearer:    "org-bearer",
	}
}

func sovereignIdentity(dep string) *identity.Identity {
	return &identity.Identity{
		Claims:       &sharedauth.Claims{Role: "sovereign-admin", DeploymentID: dep},
		Context:      identity.ContextSovereign,
		Tier:         identity.TierSovereignAdmin,
		DeploymentID: dep,
		RawBearer:    "sov-bearer",
	}
}

// TestListFilterIsContextScoped — layer-1 RBAC: both contexts see the
// read tools (a sovereign-admin can read any Org), and an unauthenticated
// caller sees nothing. (Sovereign-only write tools are deferred, so the
// surface is identical here — the gate plumbing is what's under test.)
func TestListVisibility(t *testing.T) {
	reg := NewRegistry(nil)

	if got := reg.List(nil); len(got) != 0 {
		t.Fatalf("unauthenticated caller should see 0 tools, got %d", len(got))
	}

	orgTools := reg.List(orgIdentity("acme", "dep1", identity.TierViewer))
	if len(orgTools) == 0 {
		t.Fatal("org viewer should see the read tools")
	}
	for _, want := range []string{"whoami", "list_applications", "get_application", "list_environments", "list_organizations"} {
		if !containsTool(orgTools, want) {
			t.Errorf("org viewer missing expected tool %q", want)
		}
	}
}

// TestCallUnknownAndForbidden — layer-2 re-auth.
func TestCallGate(t *testing.T) {
	reg := NewRegistry(nil)

	// Unknown tool.
	_, err := reg.Call(context.Background(), orgIdentity("acme", "d", identity.TierOwner), "nope.nope", nil)
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}

	// Nil identity → forbidden.
	_, err = reg.Call(context.Background(), nil, "whoami", nil)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden for nil identity, got %v", err)
	}
}

// TestListApplicationsOrgScoped — the facade forwards the caller's bearer
// and filters items to the caller's Org namespace.
func TestListApplicationsOrgScoped(t *testing.T) {
	var sawAuth, sawCookie, sawPath string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		sawCookie = r.Header.Get("Cookie")
		sawPath = r.URL.Path
		return jsonResp(200, `{"kind":"ApplicationList","items":[
			{"kind":"Application","name":"shop","namespace":"acme-prod","blueprint":"bp-wordpress","phase":"Ready"},
			{"kind":"Application","name":"blog","namespace":"acme","blueprint":"bp-ghost","phase":"Ready"},
			{"kind":"Application","name":"other","namespace":"globex-prod","blueprint":"bp-gitea","phase":"Ready"}
		],"total":3}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	out, err := reg.Call(context.Background(), orgIdentity("acme", "7bb723da8da06047", identity.TierViewer), "list_applications", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// Forwarded identity assertions (thin facade).
	if sawAuth != "Bearer org-bearer" {
		t.Errorf("Authorization not forwarded: %q", sawAuth)
	}
	if !strings.Contains(sawCookie, "catalyst_session=org-bearer") {
		t.Errorf("session cookie not forwarded: %q", sawCookie)
	}
	if sawPath != "/api/v1/sovereigns/7bb723da8da06047/applications" {
		t.Errorf("wrong path: %q", sawPath)
	}
	// Org scoping: only acme-* namespaces remain.
	m := out.(map[string]any)
	items := m["items"].([]catalystapi.ApplicationItem)
	if len(items) != 2 {
		t.Fatalf("org scoping failed: want 2 acme apps, got %d: %+v", len(items), items)
	}
	for _, it := range items {
		if !strings.HasPrefix(it.Namespace, "acme") {
			t.Errorf("leaked cross-org app: %+v", it)
		}
	}
}

// TestListApplicationsSovereignUnfiltered — a sovereign-admin sees all.
func TestListApplicationsSovereignUnfiltered(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"kind":"ApplicationList","items":[
			{"kind":"Application","name":"a","namespace":"acme-prod"},
			{"kind":"Application","name":"b","namespace":"globex-prod"}
		],"total":2}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)
	out, err := reg.Call(context.Background(), sovereignIdentity("7bb723da8da06047"), "list_applications", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	items := out.(map[string]any)["items"].([]catalystapi.ApplicationItem)
	if len(items) != 2 {
		t.Fatalf("sovereign-admin should see all apps unfiltered, got %d", len(items))
	}
}

// TestParity403 — when the catalyst-api endpoint returns 403, the MCP
// surfaces the SAME upstream status (thin-facade parity, #3988 DoD §4).
func TestParity403(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(403, `{"error":"forbidden","detail":"requires tier-admin or higher"}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	_, err := reg.Call(context.Background(), orgIdentity("acme", "dep1", identity.TierViewer), "list_applications", nil)
	var apiErr *catalystapi.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *catalystapi.APIError, got %v", err)
	}
	if apiErr.Status != 403 {
		t.Fatalf("parity broken: upstream 403 not preserved, got %d", apiErr.Status)
	}
}

// TestGetApplicationCrossOrgDenied — a name resolving to another Org's
// namespace is denied by the facade's defense-in-depth scope check. The
// catalyst-api get-by-name response carries `namespace` at the top level
// (the real HandleApplicationGet wire shape, confirmed on hw173); the
// metadata.namespace fallback covers the raw-CR shape.
func TestGetApplicationCrossOrgDenied(t *testing.T) {
	for _, body := range []string{
		`{"name":"leak","namespace":"globex-prod","blueprint":"bp-gitea"}`, // top-level (real endpoint shape)
		`{"metadata":{"name":"leak","namespace":"globex-prod"},"spec":{}}`, // raw-CR shape
	} {
		rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, body), nil
		})
		api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
		reg := NewRegistry(api)

		args, _ := json.Marshal(map[string]string{"name": "leak"})
		_, err := reg.Call(context.Background(), orgIdentity("acme", "dep1", identity.TierViewer), "get_application", args)
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("cross-org get should be ErrForbidden for body %q, got %v", body, err)
		}
	}
}

// TestGetApplicationOwnOrgAllowed — the caller's own Org app is returned.
func TestGetApplicationOwnOrgAllowed(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"name":"shop","namespace":"acme-prod","blueprint":"bp-wordpress"}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)
	args, _ := json.Marshal(map[string]string{"name": "shop"})
	out, err := reg.Call(context.Background(), orgIdentity("acme", "dep1", identity.TierViewer), "get_application", args)
	if err != nil {
		t.Fatalf("own-org get should succeed, got %v", err)
	}
	if out.(map[string]any)["namespace"] != "acme-prod" {
		t.Fatalf("wrong app returned: %+v", out)
	}
}

// TestWhoamiNeedsNoBackend — whoami echoes the resolved identity.
func TestWhoami(t *testing.T) {
	reg := NewRegistry(nil)
	out, err := reg.Call(context.Background(), sovereignIdentity("7bb723da8da06047"), "whoami", nil)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	m := out.(map[string]any)
	if m["context"] != "sovereign" || m["sovereign_admin"] != true {
		t.Fatalf("whoami wrong: %+v", m)
	}
}

// ── create_application (write tool, #3988 UAT rows 221-223) ──────────────

// TestCreateApplicationOrgScopedSucceeds — an Org-scoped acme token creates
// an Application in acme. The facade forwards the caller's bearer to the
// SAME install endpoint the console posts to, with the canonical
// install-request body (blueprintRef + name + organizationRef), and
// surfaces the 201 envelope.
func TestCreateApplicationOrgScopedSucceeds(t *testing.T) {
	var sawAuth, sawCookie, sawPath, sawMethod string
	var sawBody map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		sawCookie = r.Header.Get("Cookie")
		sawPath = r.URL.Path
		sawMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sawBody)
		return jsonResp(201, `{"kind":"Application","name":"shop","namespace":"acme","uid":"u-1","httpStatus":"201","applied":true}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{
		"blueprint": "bp-wordpress", "version": "1.2.3", "name": "shop",
	})
	out, err := reg.Call(context.Background(), orgIdentity("acme", "7bb723da8da06047", identity.TierAdmin), "create_application", args)
	if err != nil {
		t.Fatalf("own-org create should succeed, got %v", err)
	}
	// Thin-facade: identity forwarded, correct verb + path.
	if sawMethod != "POST" {
		t.Errorf("want POST, got %q", sawMethod)
	}
	if sawAuth != "Bearer org-bearer" {
		t.Errorf("Authorization not forwarded: %q", sawAuth)
	}
	if !strings.Contains(sawCookie, "catalyst_session=org-bearer") {
		t.Errorf("session cookie not forwarded: %q", sawCookie)
	}
	if sawPath != "/api/v1/sovereigns/7bb723da8da06047/applications" {
		t.Errorf("wrong path: %q", sawPath)
	}
	// Org defaulted to the caller's own Org (organization arg omitted).
	if sawBody["organizationRef"] != "acme" {
		t.Errorf("organizationRef should default to caller's Org, got %v", sawBody["organizationRef"])
	}
	if br, _ := sawBody["blueprintRef"].(map[string]any); br["name"] != "bp-wordpress" || br["version"] != "1.2.3" {
		t.Errorf("blueprintRef not forwarded: %v", sawBody["blueprintRef"])
	}
	m := out.(map[string]any)
	if m["namespace"] != "acme" || m["applied"] != true {
		t.Fatalf("unexpected install envelope: %+v", m)
	}
}

// TestCreateApplicationCrossOrgDenied — an acme token naming globex as the
// target Org is ErrForbidden → MCP 403, and NO request reaches the backend
// (exact parity with the read-side cross-Org get denial).
func TestCreateApplicationCrossOrgDenied(t *testing.T) {
	called := false
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(201, `{"kind":"Application"}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{
		"blueprint": "bp-gitea", "version": "1.0.0", "name": "leak", "organization": "globex",
	})
	_, err := reg.Call(context.Background(), orgIdentity("acme", "dep1", identity.TierAdmin), "create_application", args)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-org create should be ErrForbidden, got %v", err)
	}
	if called {
		t.Fatal("cross-org create must NOT reach the backend (denied at the facade)")
	}
}

// TestCreateApplicationSovereignAnyOrg — a sovereign-admin may create in any
// Org (here globex), provided the target Org is named explicitly.
func TestCreateApplicationSovereignAnyOrg(t *testing.T) {
	var sawBody map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sawBody)
		return jsonResp(201, `{"kind":"Application","name":"shop","namespace":"globex","applied":true}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{
		"blueprint": "bp-gitea", "version": "1.0.0", "name": "shop", "organization": "globex",
	})
	out, err := reg.Call(context.Background(), sovereignIdentity("7bb723da8da06047"), "create_application", args)
	if err != nil {
		t.Fatalf("sovereign-admin create in any Org should succeed, got %v", err)
	}
	if sawBody["organizationRef"] != "globex" {
		t.Errorf("sovereign-admin target Org not forwarded: %v", sawBody["organizationRef"])
	}
	if out.(map[string]any)["namespace"] != "globex" {
		t.Fatalf("unexpected install envelope: %+v", out)
	}
}

// TestCreateApplicationSovereignNeedsExplicitOrg — a sovereign-admin has no
// implicit Org scope, so omitting the target organization is an error
// (rather than silently creating in some default Org).
func TestCreateApplicationSovereignNeedsExplicitOrg(t *testing.T) {
	called := false
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(201, `{}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{"blueprint": "bp-gitea", "version": "1.0.0", "name": "x"})
	_, err := reg.Call(context.Background(), sovereignIdentity("dep1"), "create_application", args)
	if err == nil {
		t.Fatal("sovereign-admin create without an explicit organization should error")
	}
	if called {
		t.Fatal("under-scoped sovereign create must NOT reach the backend")
	}
}

// TestCreateApplicationVisibilityTierGated — layer-1: create_application is
// an admin-or-higher tool. A viewer does NOT see it (the UI Install gate is
// admin); an admin + a sovereign-admin do.
func TestCreateApplicationVisibilityTierGated(t *testing.T) {
	reg := NewRegistry(nil)

	if containsTool(reg.List(orgIdentity("acme", "d", identity.TierViewer)), "create_application") {
		t.Error("a viewer must NOT see create_application (admin-gated)")
	}
	if !containsTool(reg.List(orgIdentity("acme", "d", identity.TierAdmin)), "create_application") {
		t.Error("an admin should see create_application")
	}
	if !containsTool(reg.List(sovereignIdentity("d")), "create_application") {
		t.Error("a sovereign-admin should see create_application")
	}
}

// TestCreateApplicationViewerCallDenied — layer-2: even a hand-crafted call
// from a viewer (who cannot see the tool) is denied with ErrForbidden,
// without reaching the backend.
func TestCreateApplicationViewerCallDenied(t *testing.T) {
	called := false
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(201, `{}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{"blueprint": "bp-wordpress", "version": "1.0.0", "name": "x"})
	_, err := reg.Call(context.Background(), orgIdentity("acme", "dep1", identity.TierViewer), "create_application", args)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer create_application call should be ErrForbidden, got %v", err)
	}
	if called {
		t.Fatal("layer-2-denied call must NOT reach the backend")
	}
}

// TestCreateApplicationParity403 — when the install endpoint itself returns
// a real HTTP 403 (e.g. tier gate on the live endpoint), the MCP surfaces
// the SAME upstream status (thin-facade parity).
func TestCreateApplicationParity403(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(403, `{"error":"forbidden","detail":"requires tier-admin or higher"}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{"blueprint": "bp-wordpress", "version": "1.0.0", "name": "x"})
	_, err := reg.Call(context.Background(), orgIdentity("acme", "dep1", identity.TierAdmin), "create_application", args)
	var apiErr *catalystapi.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *catalystapi.APIError, got %v", err)
	}
	if apiErr.Status != 403 {
		t.Fatalf("parity broken: upstream 403 not preserved, got %d", apiErr.Status)
	}
}

// TestCreateApplicationFQDNOrgMatch — an Org token scoped to the bare slug
// accepts the dotted-FQDN form of the SAME Org (they resolve to one
// namespace), so the agent can pass either.
func TestCreateApplicationFQDNOrgMatch(t *testing.T) {
	var sawBody map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sawBody)
		return jsonResp(201, `{"kind":"Application","namespace":"hw178","applied":true}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{
		"blueprint": "bp-wordpress", "version": "1.0.0", "name": "shop", "organization": "hw178.omani.works",
	})
	_, err := reg.Call(context.Background(), orgIdentity("hw178", "dep1", identity.TierAdmin), "create_application", args)
	if err != nil {
		t.Fatalf("slug-token + FQDN-arg of the same Org should succeed, got %v", err)
	}
	if sawBody["organizationRef"] != "hw178.omani.works" {
		t.Errorf("organizationRef should forward the caller's value verbatim, got %v", sawBody["organizationRef"])
	}
}

func containsTool(ts []Tool, name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}
