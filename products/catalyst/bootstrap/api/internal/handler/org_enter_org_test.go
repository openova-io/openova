// org_enter_org_test.go — coverage for the Enter-org support-session
// helpers (issue #3378 B2 / DoD 6): the sovereign-admin authz gate, the
// support-principal local-part sanitization, and the TTL cap invariant.
package handler

import (
	"testing"
	"time"

	auth "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
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
