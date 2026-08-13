// org_scope_213_test.go — the SIBLING half of UAT row 213 / #6122.
//
// Row 213's clause is about get_application, and that tool is pinned at the
// wire by cmd/openova-mcp/cross_org_denial_shape_213_test.go. The clause
// generalises, though: no Org-scoped tool may answer with another
// Organization's data. list_organizations is the one remaining Org-context
// tool whose scoping is NOT decided by seam choice — it reads the
// Sovereign-WIDE directory GET /api/v1/organizations and then filters. So it
// is the one that needs its own guard, and it is where the defect was.
//
// WHAT THE DEFECT WAS. filterOrgs keyed on "slug"/"id"/"name"/"org_id"/
// "organization". The endpoint marshals orgTenantResponse, whose Org slug is
// tagged `json:"subdomain"` and which carries NONE of those five keys. Every
// row therefore failed the match and the caller's OWN Organization was
// dropped — an empty directory returned as success, the exact #5516 failure
// mode. It was invisible because no test had ever fed this handler the JSON
// the endpoint really emits.
//
// SO THE FIXTURES BELOW ARE BUILT FROM THE REAL JSON TAGS
// (products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go,
// type orgTenantResponse). A fixture invented from the field NAMES instead of
// the TAGS would have passed against the broken filter and pinned nothing —
// that is precisely how the defect survived.
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

// directoryBody is what GET /api/v1/organizations puts on the wire. Two
// Organizations, byte-shaped like orgTenantResponse.
//
// It carries BOTH Orgs on purpose. catalyst-api confines an Org-scoped
// caller server-side, so in production the second row would normally be gone
// already; serving it here is what lets these tests measure the MCP's OWN
// filter instead of measuring the stub. A test fed a pre-confined body
// cannot tell a working filter from no filter at all.
const directoryBody = `{"items":[
 {"org_tenant_id":"11111111-aaaa","state":"ready","subdomain":"hw293walkone",
  "domain_mode":"pool","admin_email":"walker@hw293walkone","company_name":"Walk One Ltd",
  "otech_fqdn":"hw293.omani.works","tenant_namespace":"org-11111111",
  "console_host":"console.hw293walkone.omani.homes"},
 {"org_tenant_id":"22222222-bbbb","state":"ready","subdomain":"hw293walktwo",
  "domain_mode":"pool","admin_email":"other@hw293walktwo","company_name":"Walk Two Ltd",
  "otech_fqdn":"hw293.omani.works","tenant_namespace":"org-22222222",
  "console_host":"console.hw293walktwo.omani.homes"}
]}`

// directoryAPI wires a client whose backend always serves directoryBody.
func directoryAPI(t *testing.T) *catalystapi.Client {
	t.Helper()
	return catalystapi.New("https://console.hw293walkone.omani.homes").
		WithTenantHost("console.hw293walkone.omani.homes").
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, directoryBody), nil
		})})
}

// orgCaller is the identity the seed-reconciler mints per Org
// (sovereign_mcp_bearer_seed.go): context=organization, tier=admin, org_id =
// the Org slug, and NO deployment_id. Reuses the package's existing
// orgIdentityNoDeployment so this file cannot drift from the shape every
// other Org-context test asserts against.
func orgCaller(orgID string) *identity.Identity {
	return orgIdentityNoDeployment(orgID, identity.TierAdmin)
}

// callListOrganizations invokes the handler and returns the rows it produced.
func callListOrganizations(t *testing.T, id *identity.Identity) []map[string]any {
	t.Helper()
	out, err := handleListOrganizations(context.Background(), id, directoryAPI(t), nil)
	if err != nil {
		t.Fatalf("list_organizations returned an error: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("list_organizations returned %T, want map", out)
	}
	items, ok := m["items"].([]map[string]any)
	if !ok {
		t.Fatalf("list_organizations items is %T, want []map[string]any", m["items"])
	}
	return items
}

// subdomainsOf renders the rows as their Org slugs for readable failures.
func subdomainsOf(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, _ := it["subdomain"].(string)
		out = append(out, s)
	}
	return out
}

// TestListOrganizationsOrgContextReturnsOwnRow is the DEFECT test: before the
// fix this returned zero rows, because no key in the real payload was one the
// filter looked at. An empty list is the most dangerous possible failure here
// — it is indistinguishable from "you have no Organizations" and it is
// returned as SUCCESS, so nothing upstream ever raises.
func TestListOrganizationsOrgContextReturnsOwnRow(t *testing.T) {
	items := callListOrganizations(t, orgCaller("hw293walkone"))

	if len(items) != 1 {
		t.Fatalf("own-Org caller must see exactly their own row, got %d: %v",
			len(items), subdomainsOf(items))
	}
	if got, _ := items[0]["subdomain"].(string); got != "hw293walkone" {
		t.Fatalf("own-Org caller saw the wrong Organization: %q", got)
	}
}

// TestListOrganizationsOrgContextDropsTheOtherOrg is the CONTROL, and it
// shares the suspect property with the test above: same handler, same
// backend body, same tool, same code path — only the Organization differs.
//
// Without it, the test above also passes for a filter that has been deleted
// outright (both rows returned includes the own row). With it, the pair can
// only pass for a filter that keeps exactly the caller's Organization.
func TestListOrganizationsOrgContextDropsTheOtherOrg(t *testing.T) {
	items := callListOrganizations(t, orgCaller("hw293walkone"))

	for _, it := range items {
		if s, _ := it["subdomain"].(string); s == "hw293walktwo" {
			t.Fatalf("list_organizations leaked another Organization's row: %v", subdomainsOf(items))
		}
	}
	// The other Org's identifying detail must not ride along in ANY field of
	// the surviving rows either — a leak by payload is still a leak.
	blob, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal rows: %v", err)
	}
	for _, needle := range []string{"hw293walktwo", "22222222-bbbb", "other@hw293walktwo", "Walk Two Ltd"} {
		if strings.Contains(string(blob), needle) {
			t.Fatalf("list_organizations leaked %q from the other Organization: %s", needle, blob)
		}
	}
}

// TestListOrganizationsAcceptsFQDNScopedToken pins the slug-vs-FQDN
// equivalence against the SAME rule create_application enforces. A token
// scoped to "hw293walkone.omani.homes" is the same Organization as the row
// whose subdomain is "hw293walkone" — resolveCreateTargetOrg has always
// accepted that pair, so the directory must not deny what the create seam
// would honour (the #6064 shape: a picker offering nothing the writer takes).
func TestListOrganizationsAcceptsFQDNScopedToken(t *testing.T) {
	items := callListOrganizations(t, orgCaller("hw293walkone.omani.homes"))

	if len(items) != 1 {
		t.Fatalf("an FQDN-scoped token must resolve to its own Org row, got %d: %v",
			len(items), subdomainsOf(items))
	}
	if got, _ := items[0]["subdomain"].(string); got != "hw293walkone" {
		t.Fatalf("FQDN-scoped token saw the wrong Organization: %q", got)
	}
}

// TestListOrganizationsSovereignContextIsUnfiltered is the second control:
// the fix must not have turned the filter into a blanket refusal. A
// sovereign-admin legitimately reads every Organization, so the same body
// must come back whole.
func TestListOrganizationsSovereignContextIsUnfiltered(t *testing.T) {
	items := callListOrganizations(t, sovereignIdentity("dep-1"))

	if len(items) != 2 {
		t.Fatalf("a sovereign-admin must see the whole directory, got %d: %v",
			len(items), subdomainsOf(items))
	}
}

// TestOrgMatchesRejectsEmptyRefs pins the edge the old exact-fold compare got
// wrong: strings.EqualFold("", "") is TRUE, so a record carrying a blank
// identity field matched a caller carrying no Org scope. Both sides must be
// non-empty for a match.
func TestOrgMatchesRejectsEmptyRefs(t *testing.T) {
	if orgMatches(map[string]any{"subdomain": ""}, "") {
		t.Fatal("a blank record field must not match a caller with no Org scope")
	}
	if orgMatches(map[string]any{"subdomain": "hw293walkone"}, "") {
		t.Fatal("a caller with no Org scope must match nothing")
	}
}

// TestOrgMatchesIgnoresConsoleHost pins the key deliberately LEFT OUT of
// orgIdentityKeys. console_host is `console.<slug>.<zone>`, so its leading
// label is "console" for every Organization on the Sovereign; admitting it
// would make one Org's row match another's under the leading-label
// comparison. This is the guard on the fix itself.
func TestOrgMatchesIgnoresConsoleHost(t *testing.T) {
	other := map[string]any{"console_host": "console.hw293walktwo.omani.homes"}
	if orgMatches(other, "console") {
		t.Fatal("console_host must never be an identity key — every Org shares its first label")
	}
	if orgMatches(other, "hw293walkone") {
		t.Fatal("another Organization's row matched on console_host")
	}
}
