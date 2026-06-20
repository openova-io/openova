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
// namespace is denied by the facade's defense-in-depth scope check.
func TestGetApplicationCrossOrgDenied(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"metadata":{"name":"leak","namespace":"globex-prod"},"spec":{}}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]string{"name": "leak"})
	_, err := reg.Call(context.Background(), orgIdentity("acme", "dep1", identity.TierViewer), "get_application", args)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-org get should be ErrForbidden, got %v", err)
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

func containsTool(ts []Tool, name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}
