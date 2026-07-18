package identity

import (
	"strings"
	"testing"
)

// FromWhoami maps catalyst-api's authoritative verdict to an Identity — the
// #5175 path where a session token cannot be locally verified but
// catalyst-api (its issuer) resolves it. A degraded resolver (the real
// deployment shape: MCP holds the handover key, not the session key) still
// yields a usable identity through this path.
func TestFromWhoami_SovereignOwner(t *testing.T) {
	r := NewDegradedResolver("no session pubkey", "")
	id, err := r.FromWhoami(WhoamiIdentity{
		Email:         "emrah.baysal@openova.io",
		Mode:          "sovereign",
		Tier:          "owner",
		DeploymentID:  "b85cb3b3a565893a",
		SovereignFQDN: "hw266.omani.works",
	}, "Bearer sess.tok.abc")
	if err != nil {
		t.Fatalf("FromWhoami rejected a valid sovereign-owner verdict: %v", err)
	}
	if id.Context != ContextSovereign {
		t.Fatalf("context = %q, want sovereign", id.Context)
	}
	if id.Tier != TierOwner {
		t.Fatalf("tier = %v, want TierOwner", id.Tier)
	}
	if id.DeploymentID != "b85cb3b3a565893a" {
		t.Fatalf("deploymentID = %q", id.DeploymentID)
	}
	// The facade forwards the caller's token verbatim (bearerOf reads
	// RawBearer after asserting Claims != nil), so both must be populated —
	// the "Bearer " prefix is stripped.
	if id.RawBearer != "sess.tok.abc" {
		t.Fatalf("rawBearer = %q, want stripped token", id.RawBearer)
	}
	if id.Claims == nil {
		t.Fatal("Claims is nil — bearerOf() would short-circuit to empty and break the forward")
	}
}

func TestFromWhoami_OrganizationAdmin(t *testing.T) {
	r := NewDegradedResolver("no session pubkey", "")
	id, err := r.FromWhoami(WhoamiIdentity{
		Email: "u@acme.test",
		Mode:  "organization",
		Tier:  "admin",
		Org:   "acme",
	}, "sess.tok.org")
	if err != nil {
		t.Fatalf("FromWhoami rejected a valid org-admin verdict: %v", err)
	}
	if id.Context != ContextOrganization {
		t.Fatalf("context = %q, want organization", id.Context)
	}
	if id.Tier != TierAdmin {
		t.Fatalf("tier = %v, want TierAdmin", id.Tier)
	}
	if id.OrgID != "acme" {
		t.Fatalf("orgID = %q, want acme", id.OrgID)
	}
}

// orgScoped=true forces Org context even when Mode is unset — matching the
// SPA's own reading of the whoami envelope.
func TestFromWhoami_OrgScopedFlagForcesOrgContext(t *testing.T) {
	id, err := NewDegradedResolver("x", "").FromWhoami(WhoamiIdentity{
		Email: "u@acme.test", Tier: "viewer", Org: "acme", OrgScoped: true,
	}, "t")
	if err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if id.Context != ContextOrganization {
		t.Fatalf("orgScoped verdict got context %q, want organization", id.Context)
	}
}

// The per-Org instance pin is re-applied on the whoami path: a verdict for a
// DIFFERENT Org is rejected, exactly as the signed-token path rejects a
// cross-org token (#3988 §4.3). The whoami fallback must not be a bypass.
func TestFromWhoami_OrgPinRejectsCrossOrg(t *testing.T) {
	r := NewDegradedResolver("x", ContextOrganization).WithOrgPin("demo")

	if _, err := r.FromWhoami(WhoamiIdentity{Mode: "organization", Org: "demo", Tier: "admin"}, "t"); err != nil {
		t.Fatalf("own-org whoami verdict rejected: %v", err)
	}
	_, err := r.FromWhoami(WhoamiIdentity{Mode: "organization", Org: "other", Tier: "admin"}, "t")
	if err == nil || !strings.Contains(err.Error(), "pinned org") {
		t.Fatalf("cross-org whoami verdict accepted by org-pinned instance (err=%v)", err)
	}
}

// A per-Org pin with no org in the verdict is rejected (can't widen to
// Sovereign, can't run Org-context without a scope).
func TestFromWhoami_OrgPinRequiresOrgScope(t *testing.T) {
	r := NewDegradedResolver("x", ContextOrganization)
	if _, err := r.FromWhoami(WhoamiIdentity{Mode: "sovereign", Tier: "owner"}, "t"); err == nil {
		t.Fatal("org-pinned instance accepted a verdict with no org scope")
	}
}

func TestFromWhoami_EmptyBearerRejected(t *testing.T) {
	if _, err := NewDegradedResolver("x", "").FromWhoami(WhoamiIdentity{Mode: "sovereign", Tier: "owner"}, "  "); err == nil {
		t.Fatal("empty bearer accepted")
	}
}

// #5206 — the symmetric guard on the whoami-fallback path (mirrors
// TestSovereignPinRejectsOrgScopedToken in identity_pins_test.go): a
// Sovereign-pinned instance must reject an org-scoped whoami verdict instead
// of silently relabelling it Sovereign — the exact hw270 misroute (an
// Org-admin session hit the only deployed instance, mode=sovereign, and
// list_applications 403'd org-scoped-forbidden downstream at catalyst-api).
func TestFromWhoami_SovereignPinRejectsOrgScopedVerdict(t *testing.T) {
	r := NewDegradedResolver("no session pubkey", ContextSovereign)
	_, err := r.FromWhoami(WhoamiIdentity{
		Email: "u@acme.test", Mode: "organization", Tier: "admin", Org: "acme",
	}, "sess.tok.org")
	if err == nil {
		t.Fatal("Sovereign-pinned instance accepted an org-scoped whoami verdict instead of rejecting it")
	}
	if !strings.Contains(err.Error(), "sovereign-admin") || !strings.Contains(err.Error(), "acme") {
		t.Fatalf("rejection error should name the sovereign-admin requirement + the caller's org, got: %v", err)
	}

	// A genuine sovereign verdict still resolves fine on the same pin.
	id, err := r.FromWhoami(WhoamiIdentity{Email: "e@openova.io", Mode: "sovereign", Tier: "owner"}, "sess.tok.sov")
	if err != nil {
		t.Fatalf("Sovereign-pinned instance rejected a genuine sovereign verdict: %v", err)
	}
	if id.Context != ContextSovereign {
		t.Fatalf("got context %q, want sovereign", id.Context)
	}
}
