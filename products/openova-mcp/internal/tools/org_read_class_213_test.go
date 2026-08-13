// org_read_class_213_test.go — UAT row 213 / #6122, generalised from the
// instance to the CLASS.
//
// Row 213 names get_application, but the property it asserts is not about one
// tool: no Org-context read may put another Organization's data, or another
// Organization's EXISTENCE, in front of the caller. get_application is simply
// where it was measured. A fix that holds only there leaves the class open —
// and the class is small enough to enumerate exhaustively, so it is.
//
// EVERY Org-context-reachable tool in catalogue() appears in the table below.
// If a tool is added to the catalogue and not added here, TestOrgReadClass_
// TableCoversEveryOrgReachableTool fails: the table cannot silently fall
// behind the surface it claims to cover.
//
// HOW THE STUB IS BUILT, AND WHY IT MATTERS. The backend serves a POISONED
// Sovereign-wide inventory: the deployment-addressed seam really does hold the
// other Organization's Application, and the Organizations directory really
// does list both Orgs. A stub that served only own-Org data could not tell a
// correctly-scoped tool from a tool with no scoping at all — it would be the
// "guard tested against a surface that cannot fail" shape. Here every tool has
// something to leak, and must not.
//
// THE IDENTITY CARRIES deployment_id ON PURPOSE. A seeded Org bearer carries
// none, and with none a tool that reached for the Sovereign-wide seam would
// fail at requireDeployment and look scoped for the wrong reason. Handing the
// caller a deployment binding removes that accident: any tool that wants the
// Sovereign-wide seam can now actually have it, so declining it is a real
// choice the test can observe.
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
)

const (
	classOwnOrg     = "hw293walkone"
	classForeignApp = "uatm4-agenity"
	classOwnApp     = "uat50-ahs-pg"
	classDeployment = "a0077ba47e3720e5"
)

// classForeignNeedles identify the OTHER Organization. None may appear in any
// tool's answer. The foreign APP NAME is deliberately not in this set: when a
// caller asks for it by name the refusal echoes what the caller supplied,
// which discloses nothing they did not already know.
var classForeignNeedles = []string{
	"hw293walktwo", "org-hw293walktwo", "other@hw293walktwo", "Walk Two Ltd", "22222222-bbbb",
}

// classOwnEstate — GET /api/v1/org/applications, server-side confined to the
// caller's Organization. This is the only Application data an Org caller is
// entitled to, and the only one the tools may end up projecting.
const classOwnEstate = `{"apps":[
	{"id":"uat50-ahs-pg","slug":"postgres","title":"uat50-ahs-pg","status":"installed",
	 "environment":"hw293walkone-prod","blueprint":"bp-postgres","instance":true}
],"bootstrapKit":[]}`

// classSovereignWide — the deployment-addressed seam. Holds the OTHER
// Organization's Application. Reaching this from an Org context is the defect.
const classSovereignWide = `{"kind":"ApplicationList","total":2,"items":[
	{"kind":"Application","name":"uat50-ahs-pg","namespace":"org-hw293walkone","phase":"Ready"},
	{"kind":"Application","name":"uatm4-agenity","namespace":"org-hw293walktwo","blueprint":"bp-agenity","phase":"Ready"}
]}`

// classDirectory — the Sovereign-WIDE Organizations directory, unconfined.
// list_organizations legitimately reads this endpoint (it is the only
// directory there is) and must filter it down to the caller's own row.
const classDirectory = `{"items":[
 {"org_tenant_id":"11111111-aaaa","state":"ready","subdomain":"hw293walkone",
  "admin_email":"walker@hw293walkone","company_name":"Walk One Ltd"},
 {"org_tenant_id":"22222222-bbbb","state":"ready","subdomain":"hw293walktwo",
  "admin_email":"other@hw293walktwo","company_name":"Walk Two Ltd"}
]}`

const classCatalog = `{"items":[{"name":"bp-postgres","version":"1.0.0"}]}`

// classAPI returns a client over the poisoned backend plus the recorder of
// every path the tool under test actually reached.
func classAPI(t *testing.T) (*catalystapi.Client, *[]string) {
	t.Helper()
	var seen []string
	api := catalystapi.New("https://console.hw293walkone.omani.homes").
		WithTenantHost("console.hw293walkone.omani.homes").
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			seen = append(seen, r.URL.Path)
			switch {
			case r.URL.Path == "/api/v1/org/applications":
				return jsonResp(200, classOwnEstate), nil
			case r.URL.Path == "/api/v1/organizations":
				return jsonResp(200, classDirectory), nil
			case r.URL.Path == "/api/v1/catalog":
				return jsonResp(200, classCatalog), nil
			case strings.HasPrefix(r.URL.Path, "/api/v1/sovereigns/"):
				return jsonResp(200, classSovereignWide), nil
			default:
				return jsonResp(404, `{"error":"not-found"}`), nil
			}
		})})
	return api, &seen
}

// orgReadCase is one row of the class.
type orgReadCase struct {
	tool    string
	handler HandlerFunc
	args    string
	// wantErr — the call is expected to be refused. The refusal is still
	// checked for foreign needles: a leak in an error message is a leak.
	wantErr bool
	// mayReadDirectory — list_organizations is the one Org-context read whose
	// scoping is a client-side filter over a Sovereign-WIDE endpoint rather
	// than a seam choice, so it alone is permitted to call
	// /api/v1/organizations. It is held to the same output rule as everything
	// else. Its filter is pinned in detail by org_scope_213_test.go.
	mayReadDirectory bool
}

func orgReadCases() []orgReadCase {
	return []orgReadCase{
		{tool: "whoami", handler: handleWhoami},
		{tool: "list_organizations", handler: handleListOrganizations, mayReadDirectory: true},
		{tool: "list_environments", handler: handleListEnvironments},
		{tool: "list_applications", handler: handleListApplications},
		{tool: "list_blueprints", handler: handleListBlueprints},
		{tool: "get_application", handler: handleGetApplication, args: `{"name":"uat50-ahs-pg"}`},
		{tool: "get_application/foreign-name", handler: handleGetApplication,
			args: `{"name":"uatm4-agenity"}`, wantErr: true},
	}
}

// TestOrgReadClass_NoToolLeaksAnotherOrganization is the class assertion: run
// every Org-context read against a backend that HAS the other Organization's
// data, and require that none of it comes back.
func TestOrgReadClass_NoToolLeaksAnotherOrganization(t *testing.T) {
	for _, tc := range orgReadCases() {
		t.Run(tc.tool, func(t *testing.T) {
			api, _ := classAPI(t)
			id := orgIdentity(classOwnOrg, classDeployment, identity.TierAdmin)

			out, err := tc.handler(context.Background(), id, api, json.RawMessage(tc.args))
			answer := renderAnswer(t, out, err)

			if tc.wantErr && err == nil {
				t.Fatalf("%s: expected a refusal, got %s", tc.tool, answer)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("%s: expected success, got error %v", tc.tool, err)
			}
			for _, needle := range classForeignNeedles {
				if strings.Contains(answer, needle) {
					t.Fatalf("%s leaked %q from the other Organization: %s", tc.tool, needle, answer)
				}
			}
			if strings.Contains(answer, classForeignApp) && !tc.wantErr {
				t.Fatalf("%s returned the other Organization's Application: %s", tc.tool, answer)
			}
		})
	}
}

// TestOrgReadClass_NoToolReachesTheSovereignWideSeam is the mechanism half.
//
// The test above measures the ANSWER; a tool could reach the Sovereign-wide
// seam, read another Org's estate, and filter it out before replying — the
// answer would be clean and the privilege would still have been exercised.
// That is what #5516 removed and what OrgScopeGuard 403s in production, so a
// clean answer obtained that way is a fault that happens to be invisible.
func TestOrgReadClass_NoToolReachesTheSovereignWideSeam(t *testing.T) {
	for _, tc := range orgReadCases() {
		t.Run(tc.tool, func(t *testing.T) {
			api, seen := classAPI(t)
			id := orgIdentity(classOwnOrg, classDeployment, identity.TierAdmin)
			_, _ = tc.handler(context.Background(), id, api, json.RawMessage(tc.args))

			for _, p := range *seen {
				if strings.HasPrefix(p, "/api/v1/sovereigns/") {
					t.Fatalf("%s reached the deployment-addressed Sovereign-wide seam %q from an Org context "+
						"(OrgScopeGuard 403s it live; #5516). Reached: %v", tc.tool, p, *seen)
				}
				if p == "/api/v1/organizations" && !tc.mayReadDirectory {
					t.Fatalf("%s read the Sovereign-wide Organizations directory. Reached: %v", tc.tool, *seen)
				}
			}
		})
	}
}

// TestOrgReadClass_SovereignAdminStillReadsEverything is the CONTROL for both
// tests above, and it shares their suspect property: same tools, same backend,
// same poisoned inventory. Only the caller's context differs.
//
// Without it, a get_application that refuses everything and a
// list_applications that returns nothing satisfy every assertion in this file.
// A sovereign-admin legitimately reads across Organizations, so the same
// backend must hand them the data an Org caller was denied — proving the two
// tests above measure the Org boundary and not a dead facade.
func TestOrgReadClass_SovereignAdminStillReadsEverything(t *testing.T) {
	api, seen := classAPI(t)
	out, err := handleListApplications(context.Background(), sovereignIdentity(classDeployment), api, nil)
	if err != nil {
		t.Fatalf("sovereign-admin list_applications failed: %v", err)
	}
	answer := renderAnswer(t, out, nil)
	if !strings.Contains(answer, classForeignApp) {
		t.Fatalf("a sovereign-admin must see every Organization's Applications — the backend served them "+
			"and the facade dropped them, so the Org-context tests above prove nothing: %s", answer)
	}
	if len(*seen) == 0 || !strings.HasPrefix((*seen)[0], "/api/v1/sovereigns/") {
		t.Fatalf("a sovereign-admin must read the deployment-addressed seam, reached %v", *seen)
	}
}

// TestOrgReadClass_TableCoversEveryOrgReachableTool keeps this file honest as
// the catalogue grows. A new Org-context tool added to catalogue() without a
// row here would otherwise be covered by nothing while this file's name
// promised the class.
func TestOrgReadClass_TableCoversEveryOrgReachableTool(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range orgReadCases() {
		covered[strings.SplitN(tc.tool, "/", 2)[0]] = true
	}
	// create_application is a WRITE and its cross-Org rule is deny-by-
	// ASSERTION (403, ADR-0013) — a different contract, pinned by
	// TestCreateApplicationCrossOrgDenied and the wire-level row-213 file.
	covered["create_application"] = true

	orgCallerID := orgIdentity(classOwnOrg, classDeployment, identity.TierAdmin)
	var missing []string
	for _, tool := range NewRegistry(nil).List(orgCallerID) {
		if !covered[tool.Name] {
			missing = append(missing, tool.Name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these tools are reachable in an Org context but absent from the row-213 class table: %v — "+
			"add a case to orgReadCases() (and decide what the tool may reach) before shipping them", missing)
	}
}

// renderAnswer flattens a handler's (result, error) into one searchable
// string, so a leak is caught whether it rode out in the payload or in the
// error text.
func renderAnswer(t *testing.T, out any, err error) string {
	t.Helper()
	if err != nil {
		return err.Error()
	}
	blob, merr := json.Marshal(out)
	if merr != nil {
		t.Fatalf("marshal handler result: %v", merr)
	}
	return string(blob)
}
