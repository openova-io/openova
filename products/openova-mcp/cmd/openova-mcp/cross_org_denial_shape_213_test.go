// cross_org_denial_shape_213_test.go — UAT row 213 / #6122.
//
// WHAT THIS PINS. The SHAPE the MCP puts on the wire when an Org-scoped
// caller reaches across the Organization boundary. Adjudicated in
// docs/adr/0013-cross-org-denial-shape.md:
//
//	deny-by-assertion (create) → -32003 forbidden / status 403
//	deny-by-lookup   (read)    → -32000 tool error / not found
//
// WHY AT THE WIRE AND NOT IN internal/tools. Row 213 is measured with a
// JSON-RPC call against a live instance, and what it reads is the numeric
// error code. internal/tools returns Go errors; the code is chosen one layer
// up, in toolError. A guard that stops at the Go error cannot see a change
// that flips the code, and the code is the whole assertion.
//
// WHY THE EXISTING GUARD WAS NOT ENOUGH.
// TestGetApplicationOrgNameOutsideOwnEstateNotFound asserts on the error
// TEXT (`strings.Contains(err, "not found")`). Wrapping the same message in
// ErrForbidden — `fmt.Errorf("%w: application %q not found …", ErrForbidden)`
// — keeps that text, keeps that test green, and silently moves the wire
// answer to -32003/403. That is exactly the change this file exists to
// catch, and it is the mutation the PR shows it going red against.
//
// THE CONTROL. Every assertion below is about a REFUSAL, and a refusal is
// also what a broken tool, a dead stub or a rejected bearer produces. So the
// own-Org get runs on the SAME transport, the SAME bearer and the SAME tool,
// and must SUCCEED. If that control ever fails, the two refusals prove
// nothing about the Org boundary.
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

// walkoneEstate is the own-org envelope GET /api/v1/org/applications returns
// for the caller's Organization. It is namespace-confined SERVER-SIDE, so the
// other Organization's `uatm4-agenity` is simply absent — there is nothing
// here to filter and nothing to leak.
const walkoneEstate = `{"apps":[
	{"id":"uat50-ahs-pg","slug":"postgres","title":"uat50-ahs-pg","status":"installed","environment":"hw293walkone-prod","blueprint":"bp-postgres","instance":true}
],"bootstrapKit":[]}`

type stubRoundTrip func(*http.Request) (*http.Response, error)

func (f stubRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// orgTransport wires the transport under test to a stub catalyst-api that
// serves ONLY the caller's own-Org estate, and records every path the MCP
// actually reached (so "no request reached the backend" is measured, not
// assumed).
func orgTransport(t *testing.T) (*httpTransport, *[]string) {
	t.Helper()
	var seen []string
	api := catalystapi.New("https://console.hw293walkone.omani.homes").
		WithTenantHost("console.hw293walkone.omani.homes").
		WithHTTPClient(&http.Client{Transport: stubRoundTrip(func(r *http.Request) (*http.Response, error) {
			seen = append(seen, r.Method+" "+r.URL.Path)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(walkoneEstate)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		})})
	return &httpTransport{
		reg:      tools.NewRegistry(api),
		resolver: identity.NewInsecureResolver(""),
	}, &seen
}

// orgBearer mints the shape the Sovereign seeds for a per-Org MCP: org_id
// set, tier=org-admin (→ TierAdmin, so create_application is reachable), and
// NO deployment_id.
func orgBearer(t *testing.T) string {
	t.Helper()
	return mintUnsigned(t, jwt.MapClaims{
		"sub": "walker@hw293walkone", "org_id": "hw293walkone", "tier": "org-admin", "typ": "session",
	})
}

type rpcFrame struct {
	Result *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func callTool(t *testing.T, tr *httpTransport, bearer, name string, args map[string]any) rpcFrame {
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
	var frame rpcFrame
	if err := json.Unmarshal(rec.Body.Bytes(), &frame); err != nil {
		t.Fatalf("%s: undecodable frame %s: %v", name, rec.Body.String(), err)
	}
	return frame
}

// TestRow213_OwnOrgGetSucceeds is THE CONTROL, and it shares the suspect
// property with both refusals below: same transport, same bearer, same tool,
// same own-org seam. It must return the object. A green control is what makes
// the two refusals evidence about the Org boundary rather than evidence that
// something upstream is simply broken.
func TestRow213_OwnOrgGetSucceeds(t *testing.T) {
	tr, seen := orgTransport(t)
	frame := callTool(t, tr, orgBearer(t), "get_application", map[string]any{"name": "uat50-ahs-pg"})

	if frame.Error != nil {
		t.Fatalf("own-Org get must succeed, got error %+v", frame.Error)
	}
	if frame.Result == nil || len(frame.Result.Content) == 0 {
		t.Fatalf("own-Org get returned no content: %+v", frame)
	}
	if !strings.Contains(frame.Result.Content[0].Text, `"uat50-ahs-pg"`) {
		t.Fatalf("own-Org get returned the wrong object: %s", frame.Result.Content[0].Text)
	}
	if len(*seen) == 0 || (*seen)[0] != "GET /api/v1/org/applications" {
		t.Fatalf("own-Org get must read the own-org seam, reached %v", *seen)
	}
}

// TestRow213_CrossOrgGetIsNotFoundNot403 — deny-by-LOOKUP.
//
// The caller names an Application that belongs to the OTHER Organization. The
// MCP cannot distinguish "exists in another Org" from "does not exist at all"
// without a Sovereign-wide probe it is not entitled to make, so the answer is
// -32000 not-found. Answering 403 here would confirm the Application's
// existence to a caller with no entitlement to that fact and turn the tool
// into a name-enumeration oracle over every Organization on the Sovereign.
func TestRow213_CrossOrgGetIsNotFoundNot403(t *testing.T) {
	tr, _ := orgTransport(t)
	frame := callTool(t, tr, orgBearer(t), "get_application", map[string]any{"name": "uatm4-agenity"})

	if frame.Error == nil {
		t.Fatalf("a cross-Org name must not resolve, got result %+v", frame.Result)
	}
	if frame.Error.Code != -32000 {
		t.Fatalf("cross-Org READ must answer -32000 tool error (deny-by-lookup), got code %d data %s — "+
			"see docs/adr/0013-cross-org-denial-shape.md before changing this",
			frame.Error.Code, string(frame.Error.Data))
	}
	// The 403 shape must be absent from the frame ENTIRELY, not merely from
	// the code: toolError puts `"status":403` in `data` for a forbidden, and
	// a walker reading the frame would call that a 403 whatever the code says.
	if strings.Contains(string(frame.Error.Data), "403") {
		t.Fatalf("cross-Org READ leaked a 403 into the error data: %s", string(frame.Error.Data))
	}
	if !strings.Contains(string(frame.Error.Data), "not found") {
		t.Fatalf("cross-Org READ must say not found, got %s", string(frame.Error.Data))
	}
	// ...and it must not name the other Organization either, or the answer
	// would leak by prose what the code refuses to leak by shape.
	if strings.Contains(string(frame.Error.Data), "hw293walktwo") {
		t.Fatalf("cross-Org READ named the other Organization: %s", string(frame.Error.Data))
	}
}

// TestRow213_CrossOrgCreateIs403 — deny-by-ASSERTION, the other half of the
// same rule.
//
// The caller NAMES the target Organization in the request, so refusing it
// discloses nothing the caller did not already assert. 403 is therefore both
// safe and the more useful answer ("you may not act on the Org you named"),
// and it is refused BEFORE any request reaches the catalyst-api.
func TestRow213_CrossOrgCreateIs403(t *testing.T) {
	tr, seen := orgTransport(t)
	frame := callTool(t, tr, orgBearer(t), "create_application", map[string]any{
		"blueprint": "bp-wordpress", "version": "1.2.3", "name": "smash",
		"organization": "hw293walktwo",
	})

	if frame.Error == nil {
		t.Fatalf("a cross-Org create must be refused, got result %+v", frame.Result)
	}
	if frame.Error.Code != -32003 {
		t.Fatalf("cross-Org WRITE must answer -32003 forbidden (deny-by-assertion), got code %d data %s",
			frame.Error.Code, string(frame.Error.Data))
	}
	if !strings.Contains(string(frame.Error.Data), `"status":403`) {
		t.Fatalf("cross-Org WRITE must carry status 403, got %s", string(frame.Error.Data))
	}
	if len(*seen) != 0 {
		t.Fatalf("a cross-Org create must be refused locally — no request may reach the backend, reached %v", *seen)
	}
}

// TestRow213_TheTwoShapesAreDifferentOnPurpose states the coherence rule as
// one assertion, so a future reader who "harmonises" the two paths trips a
// test whose name says why they diverge. Read and write are coherent under
// deny-by-lookup / deny-by-assertion — NOT under parity of shape.
func TestRow213_TheTwoShapesAreDifferentOnPurpose(t *testing.T) {
	trRead, _ := orgTransport(t)
	read := callTool(t, trRead, orgBearer(t), "get_application", map[string]any{"name": "uatm4-agenity"})
	trWrite, _ := orgTransport(t)
	write := callTool(t, trWrite, orgBearer(t), "create_application", map[string]any{
		"blueprint": "bp-wordpress", "version": "1.2.3", "name": "smash", "organization": "hw293walktwo",
	})

	if read.Error == nil || write.Error == nil {
		t.Fatalf("both cross-Org calls must be refused: read=%+v write=%+v", read, write)
	}
	if read.Error.Code == write.Error.Code {
		t.Fatalf("read and write must NOT share a denial code — the read is deny-by-lookup and "+
			"must not confirm existence, the write is deny-by-assertion and safely may. Both gave %d",
			read.Error.Code)
	}
}
