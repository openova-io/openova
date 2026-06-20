// Package auth — mint_org_test.go: round-trip + role-mapping tests
// for the Organization bridge mint helper. Sanity contract:
//
//	MintOrgAccessToken → parse with same secret via HS256 →
//	claims expose the same sub/email/role we minted.
//
// And the failure paths:
//
//	empty secret      → error (no forgeable token leak).
//	tampered token    → parse fails (caught by gateway not here, but
//	                    we assert signature is real by recomputing
//	                    with a different secret).
//
// OrgRoleFor is data-driven: every documented mapping in the helper
// godoc plus the conservative-by-default fall-through.
package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestMintOrgAccessToken_RoundTrip(t *testing.T) {
	secret := []byte("test-secret-do-not-use-in-prod-32b")
	tok, err := MintOrgAccessToken(secret, "user-uuid-1", "alice@example.com", "sovereign-admin")
	if err != nil {
		t.Fatalf("mint: unexpected error: %v", err)
	}
	if tok == "" {
		t.Fatal("mint returned empty token")
	}
	// Mirror what core/services/gateway/proxy.go validateJWT does.
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return secret, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("parse: token invalid: err=%v valid=%v", err, parsed != nil && parsed.Valid)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("parse: claims not MapClaims: %T", parsed.Claims)
	}
	if got, _ := claims["sub"].(string); got != "user-uuid-1" {
		t.Errorf("sub: got %q want user-uuid-1", got)
	}
	if got, _ := claims["email"].(string); got != "alice@example.com" {
		t.Errorf("email: got %q want alice@example.com", got)
	}
	if got, _ := claims["role"].(string); got != "sovereign-admin" {
		t.Errorf("role: got %q want sovereign-admin", got)
	}
	if got, _ := claims["typ"].(string); got != "session" {
		t.Errorf("typ: got %q want session", got)
	}
	// exp is in the future and within the OrgTokenTTL window.
	expF, _ := claims["exp"].(float64)
	exp := time.Unix(int64(expF), 0)
	if time.Until(exp) > OrgTokenTTL+time.Second {
		t.Errorf("exp too far in future: %s", exp)
	}
	if time.Until(exp) < OrgTokenTTL-time.Minute {
		t.Errorf("exp too close: %s", exp)
	}
}

func TestMintOrgAccessToken_EmptySecretRejected(t *testing.T) {
	_, err := MintOrgAccessToken(nil, "sub", "email", "role")
	if err == nil {
		t.Fatal("expected error for nil secret, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error message should mention empty secret: %v", err)
	}
	_, err = MintOrgAccessToken([]byte{}, "sub", "email", "role")
	if err == nil {
		t.Fatal("expected error for empty []byte secret, got nil")
	}
}

func TestMintOrgAccessToken_WrongSecretFailsParse(t *testing.T) {
	a := []byte("secret-A")
	b := []byte("secret-B")
	tok, err := MintOrgAccessToken(a, "sub", "email", "member")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		return b, nil
	})
	if err == nil && parsed.Valid {
		t.Fatal("token must NOT validate against a different HS256 secret")
	}
}

func TestOrgRoleFor(t *testing.T) {
	cases := []struct {
		name       string
		realmRoles []string
		tier       string
		want       string
	}{
		// Realm-role wins over tier.
		{"owner-realm-role", []string{"catalyst-owner"}, "", "superadmin"},
		{"admin-realm-role", []string{"catalyst-admin"}, "", "sovereign-admin"},
		{"legacy-sovereign-admin", []string{"sovereign-admin"}, "", "sovereign-admin"},
		{"legacy-application-admin", []string{"application-admin"}, "", "sovereign-admin"},
		// Tier fallback when no realm role hits.
		{"tier-owner", nil, "owner", "superadmin"},
		{"tier-admin", nil, "admin", "sovereign-admin"},
		// Case-insensitive on both axes.
		{"tier-Admin", nil, "Admin", "sovereign-admin"},
		{"realm-CATALYST-ADMIN", []string{"CATALYST-ADMIN"}, "", "sovereign-admin"},
		// Conservative default.
		{"empty", nil, "", "member"},
		{"viewer", []string{"catalyst-viewer"}, "viewer", "member"},
		{"developer", []string{"catalyst-developer"}, "developer", "member"},
		{"operator", []string{"catalyst-operator"}, "operator", "member"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OrgRoleFor(tc.realmRoles, tc.tier)
			if got != tc.want {
				t.Errorf("OrgRoleFor(%v, %q) = %q, want %q", tc.realmRoles, tc.tier, got, tc.want)
			}
		})
	}
}
