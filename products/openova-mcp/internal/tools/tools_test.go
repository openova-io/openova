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

// orgIdentityNoDeployment is the REAL shape of the Org-scoped bearer the
// Sovereign seeds for a per-Org agenity MCP (#5516): tier=org-admin, org_id
// set, and NO deployment_id — neither HandlePinVerify nor auth_org_handover
// stamps that claim on an Org session, so the seeded service bearer
// (sovereign_mcp_bearer_seed.go) carries none either. Every Org-context tool
// must work with exactly this identity.
func orgIdentityNoDeployment(org string, tier identity.Tier) *identity.Identity {
	return &identity.Identity{
		Claims:    &sharedauth.Claims{OrgID: org},
		Context:   identity.ContextOrganization,
		Tier:      tier,
		OrgID:     org,
		RawBearer: "org-bearer",
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

// TestListApplicationsOrgUsesOwnOrgSeamWithoutDeploymentID is the #5516
// regression test.
//
// THE DEFECT: every Org-context read tool called requireDeployment first, so
// an Org bearer (which carries no deployment_id — see orgIdentityNoDeployment)
// failed with "no deployment binding on the caller's token" before any HTTP
// request was made. Stamping a deployment_id onto the bearer would NOT have
// fixed it: the deployment-addressed seam it unlocks is not in the
// catalyst-api's orgSafePathPrefixes allowlist, so OrgScopeGuard 403s an
// org-admin session on it (proven in the catalyst-api's own
// TestOrgScopeGuard_OrgScoped_DeniesDeploymentAddressedApplicationRoutes).
//
// THE FIX: Org context reads the own-org seam GET /api/v1/org/applications —
// allowlisted, and confined to the caller's Org namespace server-side.
func TestListApplicationsOrgUsesOwnOrgSeamWithoutDeploymentID(t *testing.T) {
	var sawAuth, sawCookie, sawPath, sawMethod, sawTenantHost string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		sawCookie = r.Header.Get("Cookie")
		sawPath = r.URL.Path
		sawMethod = r.Method
		sawTenantHost = r.Header.Get("X-Tenant-Host")
		// The real HandleOrgApplications envelope: instance rows for the
		// caller's own Org, plus a NON-instance catalog row that must not be
		// reported as a running Application.
		return jsonResp(200, `{"apps":[
			{"id":"shop","slug":"wordpress","title":"shop","status":"installed","environment":"acme-prod","blueprint":"bp-wordpress","version":"1.2.3","topology":"singleton","externalURL":"https://shop.acme.omani.homes","instance":true},
			{"id":"blog","slug":"ghost","title":"blog","status":"installing","environment":"acme-dev","blueprint":"bp-ghost","instance":true},
			{"id":"bp-gitea","slug":"gitea","title":"Gitea","status":"available","instance":false}
		],"generatedAt":"2026-07-30T00:00:00Z","bootstrapKit":[]}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	// The identity deliberately carries NO deployment_id.
	out, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierViewer), "list_applications", nil)
	if err != nil {
		t.Fatalf("Org-context list must NOT require a deployment binding, got: %v", err)
	}

	// Reached the own-org seam, with the tenant host the server needs to
	// resolve the Org namespace.
	if sawMethod != "GET" {
		t.Errorf("want GET, got %q", sawMethod)
	}
	if sawPath != "/api/v1/org/applications" {
		t.Errorf("Org context must use the own-org seam, got %q", sawPath)
	}
	if sawTenantHost != "console.acme.omani.homes" {
		t.Errorf("X-Tenant-Host not forwarded (server cannot resolve the Org namespace without it): %q", sawTenantHost)
	}
	// Thin facade: the caller's bearer is forwarded verbatim on both carriers.
	if sawAuth != "Bearer org-bearer" {
		t.Errorf("Authorization not forwarded: %q", sawAuth)
	}
	if !strings.Contains(sawCookie, "catalyst_session=org-bearer") {
		t.Errorf("session cookie not forwarded: %q", sawCookie)
	}

	// Vacuity guard: the envelope MUST have decoded into rows before any
	// per-row assertion below can mean anything.
	m := out.(map[string]any)
	items := m["items"].([]catalystapi.ApplicationItem)
	if len(items) == 0 {
		t.Fatal("no Applications decoded from the own-org envelope — a per-row assertion would pass vacuously")
	}
	if len(items) != 2 {
		t.Fatalf("want the 2 instance rows (the catalog row is not a running Application), got %d: %+v", len(items), items)
	}
	if m["total"] != 2 {
		t.Errorf("total = %v, want 2", m["total"])
	}

	byName := map[string]catalystapi.ApplicationItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	shop, ok := byName["shop"]
	if !ok {
		t.Fatalf("own-org app %q missing from the projection: %+v", "shop", items)
	}
	// Card status → CR phase vocabulary, so the tool speaks one language
	// regardless of which seam served it.
	if shop.Phase != "Ready" {
		t.Errorf("shop.Phase = %q, want Ready (mapped from status=installed)", shop.Phase)
	}
	if shop.Blueprint != "bp-wordpress" || shop.Version != "1.2.3" {
		t.Errorf("shop blueprint/version not projected: %+v", shop)
	}
	if shop.Environment != "acme-prod" {
		t.Errorf("shop.Environment = %q, want acme-prod", shop.Environment)
	}
	if shop.Topology != "singleton" || shop.ExternalURL == "" {
		t.Errorf("shop topology/externalURL not projected: %+v", shop)
	}
	if blog := byName["blog"]; blog.Phase != "Installing" {
		t.Errorf("blog.Phase = %q, want Installing (mapped from status=installing)", blog.Phase)
	}
	if _, leaked := byName["bp-gitea"]; leaked {
		t.Error("a non-instance catalog row was reported as a running Application")
	}
}

// TestListApplicationsSovereignStillRequiresDeploymentBinding — the #5516 fix
// must NOT weaken the Sovereign path. A sovereign-admin session IS
// deployment-bound (the mothership handover JWT stamps deployment_id); without
// it the facade cannot address a Sovereign, so the call fails BEFORE any HTTP
// request rather than silently falling back to the own-org seam (which would
// be meaningless for a caller with no Org scope).
func TestListApplicationsSovereignStillRequiresDeploymentBinding(t *testing.T) {
	called := false
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(200, `{"kind":"ApplicationList","items":[],"total":0}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	_, err := reg.Call(context.Background(), sovereignIdentity(""), "list_applications", nil)
	if err == nil {
		t.Fatal("a sovereign-admin session with no deployment binding must error")
	}
	if !strings.Contains(err.Error(), "deployment_id") {
		t.Errorf("error should name the missing claim, got: %v", err)
	}
	if called {
		t.Error("no backend request should be made without a deployment binding")
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

// orgAppsEnvelope is the own-org estate the fake catalyst-api serves: one app
// belonging to the caller's Org. Anything NOT in this envelope is, by
// construction, outside the caller's Org — the server-side confinement the
// own-org seam applies.
const orgAppsEnvelope = `{"apps":[
	{"id":"shop","slug":"wordpress","title":"shop","status":"installed","environment":"acme-prod","blueprint":"bp-wordpress","instance":true}
],"bootstrapKit":[]}`

// TestGetApplicationOrgResolvesAgainstOwnEstate — an Org caller with NO
// deployment_id resolves a name against its own-org estate (#5516) and gets
// the row back.
func TestGetApplicationOrgResolvesAgainstOwnEstate(t *testing.T) {
	var sawPath string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawPath = r.URL.Path
		return jsonResp(200, orgAppsEnvelope), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]string{"name": "shop"})
	out, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierViewer), "get_application", args)
	if err != nil {
		t.Fatalf("own-org get must NOT require a deployment binding, got %v", err)
	}
	if sawPath != "/api/v1/org/applications" {
		t.Errorf("Org context must resolve via the own-org seam, got %q", sawPath)
	}
	item, ok := out.(catalystapi.ApplicationItem)
	if !ok {
		t.Fatalf("unexpected result type %T: %+v", out, out)
	}
	if item.Name != "shop" || item.Blueprint != "bp-wordpress" || item.Environment != "acme-prod" {
		t.Fatalf("wrong app returned: %+v", item)
	}
}

// TestGetApplicationOrgNameOutsideOwnEstateNotFound — a name that is not in
// the caller's own-org estate (another Org's app, or a typo) yields a
// not-found error and NO object. Cross-Org leakage is structurally impossible
// on this seam: the catalyst-api never puts another Org's rows in the
// response, so there is nothing to filter and nothing to leak.
func TestGetApplicationOrgNameOutsideOwnEstateNotFound(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, orgAppsEnvelope), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]string{"name": "globex-secret"})
	out, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierViewer), "get_application", args)
	if err == nil {
		t.Fatal("a name outside the caller's own-org estate must not resolve")
	}
	if out != nil {
		t.Errorf("no object may be returned for an out-of-estate name, got %+v", out)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("want a not-found error (which leaks neither existence nor scope), got: %v", err)
	}
}

// TestListEnvironmentsOrgWithoutDeploymentID — list_environments was equally
// blocked by requireDeployment (#5516). It now derives the partitions from the
// own-org estate, grouping on the endpoint's `environment` projection
// (spec.environmentRef) rather than the namespace the own-org seam omits.
func TestListEnvironmentsOrgWithoutDeploymentID(t *testing.T) {
	var sawPath string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawPath = r.URL.Path
		return jsonResp(200, `{"apps":[
			{"id":"shop","status":"installed","environment":"acme-prod","instance":true},
			{"id":"api","status":"installed","environment":"acme-prod","instance":true},
			{"id":"blog","status":"installing","environment":"acme-dev","instance":true}
		],"bootstrapKit":[]}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	out, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierViewer), "list_environments", nil)
	if err != nil {
		t.Fatalf("Org-context list_environments must NOT require a deployment binding, got %v", err)
	}
	if sawPath != "/api/v1/org/applications" {
		t.Errorf("Org context must use the own-org seam, got %q", sawPath)
	}
	items := out.(map[string]any)["items"].([]map[string]any)
	if len(items) == 0 {
		t.Fatal("no Environments derived — the per-row assertions below would pass vacuously")
	}
	counts := map[string]any{}
	for _, it := range items {
		counts[it["name"].(string)] = it["applications"]
	}
	if counts["acme-prod"] != 2 {
		t.Errorf("acme-prod app count = %v, want 2 (grouped on environmentRef)", counts["acme-prod"])
	}
	if counts["acme-dev"] != 1 {
		t.Errorf("acme-dev app count = %v, want 1", counts["acme-dev"])
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
// an Application in acme via the DEDICATED Org route /api/v1/org/applications
// (#4116). An org session is 403'd at the Sovereign seam
// /api/v1/sovereigns/{id}/applications by OrgScopeGuard (#4110/#4112), so the
// facade routes org context to the own-org route instead, passing
// X-Tenant-Host so the catalyst-api resolves the caller's own Org namespace.
// No deployment_id is needed on the org path. The 201 envelope is surfaced.
func TestCreateApplicationOrgScopedSucceeds(t *testing.T) {
	var sawAuth, sawCookie, sawPath, sawMethod, sawTenantHost string
	var sawBody map[string]any
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		sawCookie = r.Header.Get("Cookie")
		sawPath = r.URL.Path
		sawMethod = r.Method
		sawTenantHost = r.Header.Get("X-Tenant-Host")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sawBody)
		return jsonResp(201, `{"kind":"Application","name":"shop","namespace":"acme","uid":"u-1","httpStatus":"201","applied":true}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{
		"blueprint": "bp-wordpress", "version": "1.2.3", "name": "shop",
	})
	out, err := reg.Call(context.Background(), orgIdentity("acme", "7bb723da8da06047", identity.TierAdmin), "create_application", args)
	if err != nil {
		t.Fatalf("own-org create should succeed, got %v", err)
	}
	// Thin-facade: identity forwarded, correct verb + dedicated org path.
	if sawMethod != "POST" {
		t.Errorf("want POST, got %q", sawMethod)
	}
	if sawAuth != "Bearer org-bearer" {
		t.Errorf("Authorization not forwarded: %q", sawAuth)
	}
	if !strings.Contains(sawCookie, "catalyst_session=org-bearer") {
		t.Errorf("session cookie not forwarded: %q", sawCookie)
	}
	if sawPath != "/api/v1/org/applications" {
		t.Errorf("org context must use the own-org route, got %q", sawPath)
	}
	if sawTenantHost != "console.acme.omani.homes" {
		t.Errorf("X-Tenant-Host not forwarded for own-org install: %q", sawTenantHost)
	}
	// Org defaulted to the caller's own Org (organization arg omitted). The
	// server forces the real namespace from X-Tenant-Host, but the facade
	// still stamps the caller's Org ref in the body.
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
