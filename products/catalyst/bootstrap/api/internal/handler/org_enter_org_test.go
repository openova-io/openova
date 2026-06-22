// org_enter_org_test.go — coverage for the Enter-org support-session
// helpers (issue #3378 B2 / DoD 6): the sovereign-admin authz gate, the
// support-principal local-part sanitization, and the TTL cap invariant.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	auth "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	handoverjwt "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	store "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func TestEnterOrgCallerIsAdmin(t *testing.T) {
	tests := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{"nil claims rejected", nil, false},
		{"owner tier admitted", &auth.Claims{Tier: "owner"}, true},
		{"sovereign-admin role admitted", &auth.Claims{RealmAccess: auth.RealmAccess{Roles: []string{"sovereign-admin"}}}, true},
		{"catalyst-owner role admitted", &auth.Claims{RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}}}, true},
		{"plain viewer rejected", &auth.Claims{Tier: "viewer", RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-viewer"}}}, false},
		{"no roles, no tier rejected", &auth.Claims{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := enterOrgCallerIsAdmin(tc.claims); got != tc.want {
				t.Errorf("enterOrgCallerIsAdmin = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSanitizeEmailLocal(t *testing.T) {
	cases := map[string]string{
		"emrah.baysal@openova.io": "emrah.baysal",
		"Operator+Tag@x.com":      "operator-tag",
		"weird name!@x.com":       "weird-name-",
		"":                        "operator",
		"@nolocal.com":            "operator",
	}
	for in, want := range cases {
		if got := sanitizeEmailLocal(in); got != want {
			t.Errorf("sanitizeEmailLocal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnterOrgTTLCappedAt60Min(t *testing.T) {
	// The support session must never exceed 60 minutes (§6 B2 "TTL ≤
	// 60min"). This guards a future edit from widening the constant.
	if enterOrgMaxTTL > 60*time.Minute {
		t.Fatalf("enterOrgMaxTTL = %v, must be ≤ 60m", enterOrgMaxTTL)
	}
}

// TestEnterOrgUsesStoredParentDomain locks the #4075 fix: an Org created on
// a free-subdomain POOL domain (ParentDomain) that differs from the Sovereign
// FQDN must get a handover URL — and aud/sovereign_fqdn claims — pointed at
// its REAL console host (console.<sub>.<parentDomain>), NOT the re-synthesised
// console.<sub>.<sovereignFQDN>. On the omantel.biz Sovereign the demo Org
// lives on omani.homes, so Enter-org must target console.demo.omani.homes.
func TestEnterOrgUsesStoredParentDomain(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	// Seed the demo Org exactly as the real incident: free-subdomain on the
	// omani.homes pool, Sovereign FQDN is omantel.biz.
	if err := st.Put(store.OrganizationProvisionRecord{
		OrganizationID: "org-demo-uuid",
		Subdomain:      "demo",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		OTECHFQDN:      "omantel.biz",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	privPEM, _, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	signer, err := handoverjwt.New(privPEM, "https://console.openova.io", 8*time.Hour)
	if err != nil {
		t.Fatalf("handoverjwt.New: %v", err)
	}

	h := &Handler{
		log:                       slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner:            signer,
		authHandoverSovereignFQDN: "omantel.biz",
		orgTenantDeps:             OrganizationDeps{Store: st},
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
				Email:       "ops@omantel.biz",
				RealmAccess: auth.RealmAccess{Roles: []string{"sovereign-admin"}},
			})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Post("/api/v1/org/organizations/{slug}/enter", h.HandleEnterOrg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/organizations/demo/enter", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp EnterOrgResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	// The handover URL must point at the Org's real pool-domain console.
	if !strings.HasPrefix(resp.HandoverURL, "https://console.demo.omani.homes/auth/handover?token=") {
		t.Fatalf("regression (#4075): handoverURL targets the wrong host\n got: %s\nwant prefix: https://console.demo.omani.homes/auth/handover?token=", resp.HandoverURL)
	}
	// And must NOT fall back to the Sovereign FQDN.
	if strings.Contains(resp.HandoverURL, "omantel.biz") {
		t.Fatalf("regression (#4075): handoverURL leaked the Sovereign FQDN: %s", resp.HandoverURL)
	}
	// The support principal hangs off the pool domain too.
	if !strings.HasSuffix(resp.SupportPrincipal, "@demo.omani.homes") {
		t.Errorf("supportPrincipal not scoped to the pool domain: %s", resp.SupportPrincipal)
	}

	// The minted token's sovereign_fqdn claim must equal the pool host
	// suffix so the org console's handover redemption accepts it.
	token := strings.TrimPrefix(resp.HandoverURL, "https://console.demo.omani.homes/auth/handover?token=")
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if got := claims["sovereign_fqdn"]; got != "demo.omani.homes" {
		t.Errorf("sovereign_fqdn claim = %v, want demo.omani.homes", got)
	}
}
