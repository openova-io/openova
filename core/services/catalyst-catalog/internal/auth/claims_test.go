package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// makeJWT builds an unsigned JWT (signature is gibberish — we don't
// verify in catalog-svc).
func makeJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	hb, _ := json.Marshal(header)
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return fmt.Sprintf("%s.%s.%s", enc(hb), enc(pb), enc([]byte("sig")))
}

func TestExtractFromRequest_BearerHeader(t *testing.T) {
	tok := makeJWT(t, map[string]interface{}{"sub": "alice"})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	got, err := ExtractFromRequest(r, "catalyst_session")
	if err != nil || got != tok {
		t.Errorf("ExtractFromRequest = (%q, %v), want (%q, nil)", got, err, tok)
	}
}

func TestExtractFromRequest_QueryParam(t *testing.T) {
	tok := makeJWT(t, map[string]interface{}{"sub": "alice"})
	r := httptest.NewRequest(http.MethodGet, "/?access_token="+tok, nil)
	got, err := ExtractFromRequest(r, "catalyst_session")
	if err != nil || got != tok {
		t.Errorf("ExtractFromRequest = (%q, %v), want (%q, nil)", got, err, tok)
	}
}

func TestExtractFromRequest_RawCookie(t *testing.T) {
	tok := makeJWT(t, map[string]interface{}{"sub": "alice"})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "catalyst_session", Value: tok})
	got, err := ExtractFromRequest(r, "catalyst_session")
	if err != nil || got != tok {
		t.Errorf("ExtractFromRequest = (%q, %v), want (%q, nil)", got, err, tok)
	}
}

func TestExtractFromRequest_HMACWrappedCookie(t *testing.T) {
	tok := makeJWT(t, map[string]interface{}{"sub": "alice"})
	wrapped := tok + ".hmac-suffix"
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "catalyst_session", Value: wrapped})
	got, err := ExtractFromRequest(r, "catalyst_session")
	if err != nil || got != tok {
		t.Errorf("ExtractFromRequest = (%q, %v), want (%q, nil)", got, err, tok)
	}
}

func TestExtractFromRequest_NoSession(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := ExtractFromRequest(r, "catalyst_session")
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("ExtractFromRequest err = %v, want ErrNoSession", err)
	}
}

func TestParseClaims_HappyPath(t *testing.T) {
	tok := makeJWT(t, map[string]interface{}{
		"sub":    "alice",
		"email":  "alice@acme.example",
		"groups": []string{"/acme/admins", "/acme/developers"},
		"org":    "acme",
		"tier":   "admin",
		"exp":    time.Now().Add(time.Hour).Unix(),
	})
	c, err := ParseClaims(tok)
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if c.Sub != "alice" || c.Email != "alice@acme.example" || c.Org != "acme" || c.Tier != "admin" {
		t.Errorf("Claims mismatch: %+v", c)
	}
	if !reflect.DeepEqual(c.Groups, []string{"/acme/admins", "/acme/developers"}) {
		t.Errorf("Groups mismatch: %v", c.Groups)
	}
}

func TestParseClaims_Expired(t *testing.T) {
	tok := makeJWT(t, map[string]interface{}{
		"sub": "alice",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	_, err := ParseClaims(tok)
	if !errors.Is(err, ErrInvalidSession) {
		t.Errorf("ParseClaims expired err = %v, want ErrInvalidSession", err)
	}
}

func TestParseClaims_Malformed(t *testing.T) {
	_, err := ParseClaims("not.a.jwt")
	if !errors.Is(err, ErrInvalidSession) {
		t.Errorf("ParseClaims malformed err = %v, want ErrInvalidSession", err)
	}
}

func TestVisibleOrgs(t *testing.T) {
	cases := []struct {
		name string
		c    *Claims
		want []string
	}{
		{"nil", nil, nil},
		{"only-org", &Claims{Org: "acme"}, []string{"acme"}},
		{
			"groups-leading-slash",
			&Claims{Groups: []string{"/acme/admins", "/contoso/devs"}},
			[]string{"acme", "contoso"},
		},
		{
			"groups-no-slash",
			&Claims{Groups: []string{"acme/admins", "fabrikam/viewers"}},
			[]string{"acme", "fabrikam"},
		},
		{
			"org-and-groups-deduped",
			&Claims{Org: "acme", Groups: []string{"/acme/admins"}},
			[]string{"acme"},
		},
		{
			"empty-paths-ignored",
			&Claims{Groups: []string{"", "/", "/acme"}},
			[]string{"acme"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VisibleOrgs(tc.c)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("VisibleOrgs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasOrg(t *testing.T) {
	c := &Claims{Org: "acme", Groups: []string{"/contoso/admins"}}
	if !HasOrg(c, "acme") {
		t.Error("HasOrg(acme) should be true via Org claim")
	}
	if !HasOrg(c, "contoso") {
		t.Error("HasOrg(contoso) should be true via Groups")
	}
	if HasOrg(c, "fabrikam") {
		t.Error("HasOrg(fabrikam) should be false")
	}
	if HasOrg(nil, "acme") {
		t.Error("HasOrg(nil, ...) should be false")
	}
	if HasOrg(c, "") {
		t.Error("HasOrg(c, \"\") should be false")
	}
}
