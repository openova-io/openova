// cross_org_indistinguishable_213_test.go — UAT row 213 / #6122, the half
// the code-only assertion cannot reach.
//
// WHAT ROW 213 ACTUALLY ASSERTS. Not "the refusal says not found". It asserts
// that a caller CANNOT LEARN whether the Application exists in another
// Organization. That is a statement about TWO Sovereigns, not one: the same
// request, on a Sovereign where the name is owned by another Org and on a
// Sovereign where the name exists nowhere, must be answered identically.
//
// WHY THE EXISTING GUARD DOES NOT REACH IT.
// cross_org_denial_shape_213_test.go runs ONE world. It asserts code -32000,
// no "403" in the data, and that the other Org's slug is absent. Every one of
// those survives this handler:
//
//	if _, err := api.GetApplication(ctx, depID, in.Name, bearerOf(id)); err == nil {
//	    return nil, fmt.Errorf("application %q not found in organization %q "+
//	        "(check the owning organization)", in.Name, id.OrgID)
//	}
//
// Code still -32000. Still says "not found". Still never names the other Org.
// And it answers "does Organization X run an app called Y?" for every name an
// attacker cares to try — the exact oracle ADR-0013 refuses. A single-world
// test cannot see it, because in a single world there is nothing to compare
// the answer against. That mutant is the vacuity proof for this file.
//
// SO EVERY ASSERTION HERE IS A COMPARISON BETWEEN WORLDS. The own-Org estate
// is byte-identical in both; only the Sovereign-wide inventory differs. Any
// divergence in the answer — code, message, data, or the set of upstream
// requests made — is a channel.
//
// WHY A BEARER CARRYING deployment_id IS ONE OF THE CASES. An Org session
// normally carries none, and the mutant above needs one: without a
// deployment binding requireDeployment fails, the probe never runs, and the
// two worlds look alike for a reason that has nothing to do with the
// contract. Running the no-deployment shape ALONE would give this file no
// discriminating power against the very edit it exists to catch. Both shapes
// run; the no-deployment one is the realistic seeded bearer, the
// deployment-bearing one is the shape in which a Sovereign-wide probe can
// actually succeed.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

const (
	// foreignApp is owned by the OTHER Organization. It is never present in
	// walkoneEstate — the own-org seam that the caller can actually reach.
	foreignApp = "uatm4-agenity"
	// ghostApp exists nowhere on either Sovereign. It is the reference answer
	// the foreign name's answer must equal.
	ghostApp = "no-such-application"
	// ownApp is in the caller's own estate — the control.
	ownApp = "uat50-ahs-pg"

	testDeploymentID = "a0077ba47e3720e5"
)

// sovereignWideInventory is what the deployment-addressed seam would answer
// on the world where the foreign Application EXISTS. Nothing in the MCP is
// entitled to read it from an Org context; it is served here precisely so a
// handler that reaches for it produces a visibly different world.
const sovereignWideInventory = `{"kind":"ApplicationList","total":1,"items":[
	{"kind":"Application","name":"uatm4-agenity","namespace":"org-hw293walktwo","blueprint":"bp-agenity","phase":"Ready"}
]}`

// worldTransport wires the transport under test to a stub catalyst-api and
// records every upstream path reached.
//
// foreignExists selects the world:
//
//	true  — the Sovereign-wide seam serves uatm4-agenity (another Org owns it)
//	false — the Sovereign-wide seam has nothing; the name exists nowhere
//
// The own-org seam answers walkoneEstate in BOTH worlds, byte for byte. That
// is what makes a difference in the reply attributable to the Sovereign-wide
// inventory and nothing else.
func worldTransport(t *testing.T, foreignExists bool) (*httpTransport, *[]string) {
	t.Helper()
	var seen []string
	api := catalystapi.New("https://console.hw293walkone.omani.homes").
		WithTenantHost("console.hw293walkone.omani.homes").
		WithHTTPClient(&http.Client{Transport: stubRoundTrip(func(r *http.Request) (*http.Response, error) {
			seen = append(seen, r.Method+" "+r.URL.Path)
			switch {
			case r.URL.Path == "/api/v1/org/applications":
				return stubJSON(200, walkoneEstate), nil
			case strings.HasPrefix(r.URL.Path, "/api/v1/sovereigns/"):
				if foreignExists {
					if strings.HasSuffix(r.URL.Path, "/applications/"+foreignApp) {
						return stubJSON(200, `{"kind":"Application","metadata":{"name":"uatm4-agenity","namespace":"org-hw293walktwo"}}`), nil
					}
					if strings.HasSuffix(r.URL.Path, "/applications") {
						return stubJSON(200, sovereignWideInventory), nil
					}
				}
				return stubJSON(404, `{"error":"not-found"}`), nil
			default:
				return stubJSON(404, `{"error":"not-found"}`), nil
			}
		})})
	return &httpTransport{
		reg:      tools.NewRegistry(api),
		resolver: identity.NewInsecureResolver(""),
	}, &seen
}

func stubJSON(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// bearerShapes are the two Org-scoped identities this file runs every
// comparison under. See the file header for why the deployment-bearing one is
// not padding.
func bearerShapes(t *testing.T) []struct {
	name   string
	bearer string
} {
	t.Helper()
	return []struct {
		name   string
		bearer string
	}{
		{
			name: "seeded-org-bearer-no-deployment",
			bearer: mintUnsigned(t, jwt.MapClaims{
				"sub": "walker@hw293walkone", "org_id": "hw293walkone", "tier": "org-admin", "typ": "session",
			}),
		},
		{
			name: "org-bearer-carrying-deployment-binding",
			bearer: mintUnsigned(t, jwt.MapClaims{
				"sub": "walker@hw293walkone", "org_id": "hw293walkone", "tier": "org-admin", "typ": "session",
				"deployment_id": testDeploymentID,
			}),
		},
	}
}

// rawCallTool returns the RAW response body. The comparison below is on
// bytes, not on a decoded struct: a decoded struct drops any field the test's
// own type does not declare, so a handler that leaked an extra key would
// compare equal. What the caller sees is the bytes.
func rawCallTool(t *testing.T, tr *httpTransport, bearer, name string, args map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := postMCP(t, tr, bearer, string(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d body %s", name, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestRow213_ForeignNameAnswersExactlyLikeANameThatExistsNowhere is the
// clause, stated as the comparison it actually is.
//
// Same tool, same bearer, same own-org estate, same requested name. The only
// difference between the two calls is whether another Organization on the
// Sovereign owns that name. The two replies must be the SAME BYTES.
func TestRow213_ForeignNameAnswersExactlyLikeANameThatExistsNowhere(t *testing.T) {
	for _, shape := range bearerShapes(t) {
		t.Run(shape.name, func(t *testing.T) {
			existsElsewhere, _ := worldTransport(t, true)
			existsNowhere, _ := worldTransport(t, false)

			owned := rawCallTool(t, existsElsewhere, shape.bearer, "get_application", map[string]any{"name": foreignApp})
			absent := rawCallTool(t, existsNowhere, shape.bearer, "get_application", map[string]any{"name": foreignApp})

			// Both must be refusals — otherwise "identical" could mean the
			// handler is answering the foreign Application to everyone.
			assertNotFoundRefusal(t, "name owned by another Organization", owned)
			assertNotFoundRefusal(t, "name that exists nowhere", absent)

			if owned != absent {
				t.Fatalf("row 213: the refusal DIFFERS depending on whether another Organization owns the name — "+
					"that difference IS the existence oracle ADR-0013 refuses.\n owned-elsewhere: %s\n exists-nowhere: %s",
					owned, absent)
			}
		})
	}
}

// TestRow213_ForeignNameAnswersExactlyLikeAGhostName is the same comparison
// run the other way round: hold the WORLD fixed and vary the NAME.
//
// It catches what the test above cannot. That one compares one name across
// two worlds, so a handler that answered every miss with a constant leak-free
// string would pass it — and would still be fine. This one compares two names
// in the SAME world, which is the comparison an attacker actually gets to
// make: they cannot switch Sovereigns, they can only try names. The message
// must not encode which of the two kinds of miss it was.
func TestRow213_ForeignNameAnswersExactlyLikeAGhostName(t *testing.T) {
	for _, shape := range bearerShapes(t) {
		t.Run(shape.name, func(t *testing.T) {
			tr, _ := worldTransport(t, true)
			foreign := rawCallTool(t, tr, shape.bearer, "get_application", map[string]any{"name": foreignApp})
			ghostTr, _ := worldTransport(t, true)
			ghost := rawCallTool(t, ghostTr, shape.bearer, "get_application", map[string]any{"name": ghostApp})

			assertNotFoundRefusal(t, "foreign name", foreign)
			assertNotFoundRefusal(t, "ghost name", ghost)

			// The requested name is echoed by design (the caller supplied it),
			// so normalise it away and require everything else to match.
			normForeign := strings.ReplaceAll(foreign, foreignApp, "<NAME>")
			normGhost := strings.ReplaceAll(ghost, ghostApp, "<NAME>")
			if normForeign != normGhost {
				t.Fatalf("row 213: a caller can tell a foreign name from a nonexistent one by the answer alone.\n"+
					" foreign: %s\n ghost:   %s", foreign, ghost)
			}
		})
	}
}

// TestRow213_AMissMakesNoSovereignWideProbe closes the side channel the two
// byte comparisons above leave open.
//
// A handler could probe the Sovereign-wide seam, learn the answer, and then
// deliberately return the same bytes either way. The bytes would match; the
// LATENCY and the upstream request pattern would not, and on a live Sovereign
// the probe is itself the privilege OrgScopeGuard exists to deny. So the set
// of upstream requests must also be identical between the worlds, and must
// contain no deployment-addressed path at all.
func TestRow213_AMissMakesNoSovereignWideProbe(t *testing.T) {
	for _, shape := range bearerShapes(t) {
		t.Run(shape.name, func(t *testing.T) {
			existsElsewhere, seenA := worldTransport(t, true)
			existsNowhere, seenB := worldTransport(t, false)

			rawCallTool(t, existsElsewhere, shape.bearer, "get_application", map[string]any{"name": foreignApp})
			rawCallTool(t, existsNowhere, shape.bearer, "get_application", map[string]any{"name": foreignApp})

			for _, p := range *seenA {
				if strings.Contains(p, "/api/v1/sovereigns/") {
					t.Fatalf("an Org-context lookup reached the Sovereign-wide seam %q — that probe is the "+
						"existence oracle, whatever it then chooses to print. Reached: %v", p, *seenA)
				}
			}
			if strings.Join(*seenA, "|") != strings.Join(*seenB, "|") {
				t.Fatalf("the upstream request pattern differs between the two worlds — a timing oracle.\n"+
					" owned-elsewhere: %v\n exists-nowhere: %v", *seenA, *seenB)
			}
			if len(*seenA) != 1 || (*seenA)[0] != "GET /api/v1/org/applications" {
				t.Fatalf("an Org-context lookup must read the own-org seam exactly once, reached %v", *seenA)
			}
		})
	}
}

// TestRow213_OwnOrgGetSucceedsInBothWorlds is THE CONTROL for this file, and
// it shares every suspect property with the comparisons above: same worlds,
// same bearers, same tool, same transport. Only the requested name changes,
// to one the caller's own estate really holds.
//
// Without it, all three tests above are satisfied by a get_application that
// refuses everything — identical refusals are trivially identical.
func TestRow213_OwnOrgGetSucceedsInBothWorlds(t *testing.T) {
	for _, shape := range bearerShapes(t) {
		t.Run(shape.name, func(t *testing.T) {
			for _, foreignExists := range []bool{true, false} {
				tr, _ := worldTransport(t, foreignExists)
				body := rawCallTool(t, tr, shape.bearer, "get_application", map[string]any{"name": ownApp})
				if strings.Contains(body, `"error"`) {
					t.Fatalf("own-Org get must succeed (foreignExists=%v), got %s", foreignExists, body)
				}
				if !strings.Contains(body, ownApp) {
					t.Fatalf("own-Org get returned the wrong object (foreignExists=%v): %s", foreignExists, body)
				}
			}
		})
	}
}

// assertNotFoundRefusal checks the reply is the -32000 not-found refusal and
// not something else that merely happens to be equal across two runs — a
// crash, an unauthenticated frame, or a success.
func assertNotFoundRefusal(t *testing.T, label, body string) {
	t.Helper()
	var frame rpcFrame
	if err := json.Unmarshal([]byte(body), &frame); err != nil {
		t.Fatalf("%s: undecodable frame %s: %v", label, body, err)
	}
	if frame.Error == nil {
		t.Fatalf("%s: expected a refusal, got a result: %s", label, body)
	}
	if frame.Error.Code != -32000 {
		t.Fatalf("%s: expected -32000 tool error, got code %d: %s", label, frame.Error.Code, body)
	}
	if !strings.Contains(string(frame.Error.Data), "not found") {
		t.Fatalf("%s: expected a not-found refusal, got %s", label, body)
	}
}
