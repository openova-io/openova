package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

// End-to-end #5175: a degraded resolver is the REAL deployment shape — the MCP
// holds the mothership HANDOVER public key but callers present post-handover
// SESSION tokens signed by a deployment-local key the MCP does not have, so
// local verify fails for every real caller. resolveBearer must then fall back
// to catalyst-api's /whoami (the session-token authority) and resolve there.
func TestResolveBearer_WhoamiFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/whoami" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"email":"e@openova.io","verified":true,"mode":"sovereign","tier":"owner","deploymentId":"dep1"}`))
	}))
	defer ts.Close()

	srv := &server{
		reg:      tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewDegradedResolver("no session pubkey", ""),
	}

	id, err := srv.resolveBearer("sess.tok.session")
	if err != nil {
		t.Fatalf("resolveBearer fell through despite a valid whoami verdict: %v", err)
	}
	if id.Tier != identity.TierOwner || id.Context != identity.ContextSovereign {
		t.Fatalf("resolved id tier=%v ctx=%s", id.Tier, id.Context)
	}
	if id.RawBearer != "sess.tok.session" {
		t.Fatalf("rawBearer=%q not forwarded to the facade", id.RawBearer)
	}
}

// Fail-closed: when catalyst-api itself rejects the token (401), resolveBearer
// keeps the ORIGINAL local-verify error — a catalyst-api verdict never widens
// the surface on a token catalyst-api refuses.
func TestResolveBearer_WhoamiFallback_FailsClosed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	srv := &server{
		reg:      tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewDegradedResolver("no session pubkey", ""),
	}
	if _, err := srv.resolveBearer("bad.tok"); err == nil {
		t.Fatal("resolveBearer accepted a token catalyst-api rejected (surface widened)")
	}
}

// No catalyst-api client wired → no fallback path → the original verify error
// stands. Backend-less / stdio-only deployments are unaffected by the fix.
func TestResolveBearer_NoAPIClient_NoFallback(t *testing.T) {
	srv := &server{
		reg:      tools.NewRegistry(nil),
		resolver: identity.NewDegradedResolver("no session pubkey", ""),
	}
	if _, err := srv.resolveBearer("any.tok"); err == nil {
		t.Fatal("resolveBearer resolved with no resolver key and no catalyst-api client")
	}
}
