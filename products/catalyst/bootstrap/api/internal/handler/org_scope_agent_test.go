package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// agentClaims are the claims of an agent token of Organization a.
func agentClaims() *auth.Claims {
	return &auth.Claims{Email: "probe@a.agents.invalid", Typ: "agent", Agent: "probe", Tier: "viewer", Org: "a",
		Audience: []string{"model-gateway", "platform-tools", "catalyst-api"}}
}

// TestClaimsAreOrgScoped_AgentToken: an agent token is confined like an
// Organization session, whatever its tier; a typ that is not "agent" is not.
func TestClaimsAreOrgScoped_AgentToken(t *testing.T) {
	if !claimsAreOrgScoped(agentClaims()) {
		t.Fatal("an agent token must be Organization-scoped")
	}
	if !claimsAreOrgScoped(&auth.Claims{Typ: "AGENT", Tier: "developer"}) {
		t.Fatal("the typ match must be case-insensitive")
	}
	if claimsAreOrgScoped(&auth.Claims{Typ: "Bearer", Tier: "admin"}) {
		t.Fatal("a Keycloak token of typ Bearer is not Organization-scoped")
	}
	h := &Handler{log: quietLog()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/org/applications", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, agentClaims()))
	if org, scoped := h.orgScopeForRequest(req); !scoped || org != "a" {
		t.Fatalf("orgScopeForRequest = %q, %v; want a, true", org, scoped)
	}
}

// TestOrgScopeGuard_AgentToken: an agent token of Organization a gets 403
// org-scoped-forbidden on a deployment-addressed route, whose handler never
// runs, and passes on its own Organization's route.
func TestOrgScopeGuard_AgentToken(t *testing.T) {
	h := &Handler{log: quietLog()}
	for _, c := range []struct {
		path    string
		status  int
		reached bool
	}{
		{"/api/v1/sovereigns/29b7e14918178f7e/applications", http.StatusForbidden, false},
		{"/api/v1/deployments/29b7e14918178f7e", http.StatusForbidden, false},
		{"/api/v1/org/applications", http.StatusOK, true},
	} {
		reached := false
		guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, agentClaims()))
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != c.status || reached != c.reached {
			t.Errorf("GET %s: %d reached=%v body=%s; want %d reached=%v", c.path, rec.Code, reached, rec.Body, c.status, c.reached)
		}
		if c.status == http.StatusForbidden && !strings.Contains(rec.Body.String(), "org-scoped-forbidden") {
			t.Errorf("GET %s: body %s", c.path, rec.Body)
		}
	}
}
