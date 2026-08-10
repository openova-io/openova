// blueprint_version_test.go — the Blueprint-version half of the agentic
// create chain (UAT rows 221/222, #5516).
//
// THE DEFECT these lock down: `blueprintRef.version` is REQUIRED by the
// catalyst-api install validator and is NOT one of the fields
// applicationInstallRequestNormalize defaults (environmentRef,
// placement.mode and placement.regions all are). The MCP's
// create_application schema marks `version` optional, and the MCP exposed no
// catalog tool at all — so an agent handed "install wordpress in my org" had
// no way to learn a version the validator accepts. Omitting it forwarded
// `"version":""` and earned an opaque 400 several hops from its cause; the
// console never hits this because InstallPage.tsx composes the install body
// from the selected CATALOG CARD.
//
// Every pre-existing create test in tools_test.go passes an explicit
// "version": "1.2.3", so none of them could ever have caught this.
package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

// catalogBody is the GET /api/v1/catalog envelope catalyst-api's
// HandleCatalogList returns (CatalogListResponseEnvelope).
const catalogBody = `{"items":[
  {"name":"bp-wordpress","version":"1.2.3","card":{"title":"WordPress","summary":"Blog + CMS"},
   "versions":[{"version":"1.2.3","chartRef":"oci://ghcr.io/openova-io/bp-wordpress:1.2.3"},
               {"version":"1.1.0","chartRef":"oci://ghcr.io/openova-io/bp-wordpress:1.1.0"}]},
  {"name":"bp-gitea","version":"0.9.0","card":{"title":"Gitea"},"versions":[{"version":"0.9.0"}]}
],"origins":["public"]}`

// TestCreateApplicationOmittedVersionResolvedFromCatalog — THE ROW-221 CASE.
// The agent is asked to "create a wordpress app" and supplies no version.
// The facade must resolve it from the catalog (exactly what the console's
// Install page does) so the POSTed body carries a version the catalyst-api
// install validator accepts.
//
// WITHOUT THE FIX this fails on `blueprintRef.version` == "" — the body that
// upstream rejects with `blueprintRef.version is required`.
func TestCreateApplicationOmittedVersionResolvedFromCatalog(t *testing.T) {
	var sawBody map[string]any
	var sawPaths []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawPaths = append(sawPaths, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/v1/catalog" {
			return jsonResp(200, catalogBody), nil
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sawBody)
		return jsonResp(201, `{"kind":"Application","name":"shop","namespace":"acme","httpStatus":"201","applied":true}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	// No "version" key at all — the shape a chat-driven create produces.
	args, _ := json.Marshal(map[string]any{"blueprint": "bp-wordpress", "name": "shop"})
	if _, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierAdmin), "create_application", args); err != nil {
		t.Fatalf("create with omitted version should succeed, got %v", err)
	}

	br, _ := sawBody["blueprintRef"].(map[string]any)
	if br == nil {
		t.Fatalf("no install body reached the backend; requests seen: %v", sawPaths)
	}
	if br["version"] != "1.2.3" {
		t.Errorf("omitted version must be resolved from the catalog headline version; got %q (blueprintRef=%v, requests=%v)",
			br["version"], br, sawPaths)
	}
	if br["name"] != "bp-wordpress" {
		t.Errorf("blueprintRef.name not forwarded: %v", br)
	}
	// The resolution must read the catalog, not invent a version.
	joined := strings.Join(sawPaths, ", ")
	if !strings.Contains(joined, "GET /api/v1/catalog") {
		t.Errorf("version must come from GET /api/v1/catalog, not a guess; requests: %s", joined)
	}
}

// TestCreateApplicationExplicitVersionIsNotOverridden — THE CONTROL that
// makes the assertion above meaningful. The same catalog publishes 1.2.3;
// an explicitly pinned 1.1.0 must survive verbatim. If the assertion in the
// test above passed by unconditionally stamping the catalog version, this
// test fails — so the pair proves the code branches on absence, not that it
// rewrites every create.
func TestCreateApplicationExplicitVersionIsNotOverridden(t *testing.T) {
	var sawBody map[string]any
	catalogHits := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/catalog" {
			catalogHits++
			return jsonResp(200, catalogBody), nil
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sawBody)
		return jsonResp(201, `{"kind":"Application","name":"shop"}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{"blueprint": "bp-wordpress", "version": "1.1.0", "name": "shop"})
	if _, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierAdmin), "create_application", args); err != nil {
		t.Fatalf("create with explicit version should succeed, got %v", err)
	}
	br, _ := sawBody["blueprintRef"].(map[string]any)
	if br["version"] != "1.1.0" {
		t.Errorf("an explicitly pinned version must be honoured verbatim, got %q", br["version"])
	}
	if catalogHits != 0 {
		t.Errorf("an explicit version must not cost a catalog round-trip; got %d", catalogHits)
	}
}

// TestCreateApplicationUnknownBlueprintFailsLoud — the fail-LOUD half. A
// Blueprint the catalog does not publish must be refused HERE, naming the
// Blueprint and the tool that answers the question, and NO install may be
// POSTed. Silently forwarding an empty version instead would surface as an
// upstream `blueprintRef.version is required` 400 that reads as "the create
// is broken" rather than "that Blueprint does not exist here" — the exact
// misattribution class this chain has already lost days to.
func TestCreateApplicationUnknownBlueprintFailsLoud(t *testing.T) {
	posted := false
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/catalog" {
			return jsonResp(200, catalogBody), nil
		}
		posted = true
		return jsonResp(201, `{"kind":"Application"}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{"blueprint": "bp-nosuchthing", "name": "shop"})
	_, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierAdmin), "create_application", args)
	if err == nil {
		t.Fatalf("an uncatalogued Blueprint must be refused, not forwarded with an empty version")
	}
	if posted {
		t.Errorf("no install may be POSTed when the version cannot be resolved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bp-nosuchthing") {
		t.Errorf("the error must name the Blueprint, got %q", msg)
	}
	if !strings.Contains(msg, "list_blueprints") {
		t.Errorf("the error must point at the tool that answers the question, got %q", msg)
	}
}

// TestCreateApplicationCatalogUnreachableIsDistinctFromUnknownBlueprint —
// a transport failure on the catalog must NOT be reported as "that Blueprint
// does not exist". Collapsing the two would make a wiring fault read to the
// agent (and to the user) as a typo.
func TestCreateApplicationCatalogUnreachableIsDistinctFromUnknownBlueprint(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/catalog" {
			return jsonResp(502, `{"error":"catalog_upstream"}`), nil
		}
		return jsonResp(201, `{"kind":"Application"}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)

	args, _ := json.Marshal(map[string]any{"blueprint": "bp-wordpress", "name": "shop"})
	_, err := reg.Call(context.Background(), orgIdentityNoDeployment("acme", identity.TierAdmin), "create_application", args)
	if err == nil {
		t.Fatalf("an unreachable catalog must fail the create, not post an empty version")
	}
	if strings.Contains(err.Error(), "list_blueprints") {
		t.Errorf("a catalog transport failure must not be reported as an unknown Blueprint: %q", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("the upstream status must survive so the real cause is visible: %q", err)
	}
}

// TestListBlueprintsIsOfferedToAnOrgCallerAndReturnsVersions — the discovery
// tool an agent needs before it can compose a create. It must be visible to
// an Org-scoped, deployment-less caller (the real seeded bearer shape) and
// must surface the version to pin.
func TestListBlueprintsIsOfferedToAnOrgCallerAndReturnsVersions(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/catalog" {
			t.Errorf("list_blueprints must read the Org-safe catalog path, got %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer org-bearer" {
			t.Errorf("caller identity must be forwarded: %q", r.Header.Get("Authorization"))
		}
		return jsonResp(200, catalogBody), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	reg := NewRegistry(api)
	id := orgIdentityNoDeployment("acme", identity.TierViewer)

	offered := false
	for _, tool := range reg.List(id) {
		if tool.Name == "list_blueprints" {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("list_blueprints must be offered to an Org-scoped caller")
	}

	out, err := reg.Call(context.Background(), id, "list_blueprints", nil)
	if err != nil {
		t.Fatalf("list_blueprints: %v", err)
	}
	m := out.(map[string]any)
	if m["total"] != 2 {
		t.Fatalf("want 2 blueprints, got %v", m["total"])
	}
	items := m["items"].([]map[string]any)
	if items[0]["name"] != "bp-wordpress" || items[0]["version"] != "1.2.3" {
		t.Errorf("catalog row not projected with its version: %v", items[0])
	}
	if items[0]["title"] != "WordPress" {
		t.Errorf("card title not projected: %v", items[0])
	}
	vs, _ := items[0]["versions"].([]string)
	if len(vs) != 2 || vs[0] != "1.2.3" {
		t.Errorf("version index not projected: %v", items[0]["versions"])
	}
}
