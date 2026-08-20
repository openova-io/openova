// keycloak_console_redirect_6509_test.go — proves the org-controller
// registers the CONCRETE per-Org console callback onto the sovereign realm's
// catalyst-ui client (#6509), NOT the inert mid-host wildcard #6504 emitted.
//
// Root cause under test: Keycloak 26.3 only honors a TRAILING `*` in a
// redirectUri; a `*` in the HOST segment (`https://console.*.<pool>/*`) is
// matched literally and never covers a real per-Org subdomain, so every
// per-Org console login 400'd `invalid_redirect_uri`. RegisterOrgConsoleRedirectURI
// appends the concrete host `https://console.<slug>.<poolTLD>/*` — the only
// shape Keycloak actually accepts.
package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// kcClientRep is the slice of the Keycloak client representation this test
// inspects on the round-trip. The LiveKeycloak impl round-trips the FULL
// representation as raw JSON, so unlisted fields here are preserved untouched.
type kcClientRep struct {
	ID           string   `json:"id"`
	ClientID     string   `json:"clientId"`
	Name         string   `json:"name"`
	RedirectUris []string `json:"redirectUris"`
	WebOrigins   []string `json:"webOrigins"`
}

// fakeKCAdmin is an in-process Keycloak Admin API stub implementing just the
// token + get-clients + put-client endpoints RegisterOrgConsoleRedirectURI /
// DeregisterOrgConsoleRedirectURI call. It holds one catalyst-ui client whose
// redirectUris/webOrigins mutate on PUT so idempotency is observable.
type fakeKCAdmin struct {
	realm    string
	cu       kcClientRep
	putCount int
	// otherFields is an extra attribute seeded on the client to prove the
	// full-representation round-trip preserves fields the impl never touches.
	otherFields string
}

func newFakeKCAdmin(realm, sovereignFQDN string) *fakeKCAdmin {
	return &fakeKCAdmin{
		realm: realm,
		cu: kcClientRep{
			ID:           "uuid-catalyst-ui",
			ClientID:     "catalyst-ui",
			Name:         "Catalyst UI (Sovereign console)",
			RedirectUris: []string{"https://console." + sovereignFQDN + "/*"},
			WebOrigins:   []string{"https://console." + sovereignFQDN},
		},
		otherFields: "preserve-me",
	}
}

func (f *fakeKCAdmin) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost &&
			r.URL.Path == "/realms/"+f.realm+"/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"test-token"}`))

		case r.Method == http.MethodGet &&
			r.URL.Path == "/admin/realms/"+f.realm+"/clients":
			if got := r.URL.Query().Get("clientId"); got != "catalyst-ui" {
				t.Errorf("unexpected clientId query: %q", got)
			}
			// Emit a representation carrying an attribute the impl never
			// touches, to prove PUT preserves it.
			out := []map[string]any{{
				"id":           f.cu.ID,
				"clientId":     f.cu.ClientID,
				"name":         f.cu.Name,
				"redirectUris": f.cu.RedirectUris,
				"webOrigins":   f.cu.WebOrigins,
				"someOtherKey": f.otherFields,
			}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case r.Method == http.MethodPut &&
			r.URL.Path == "/admin/realms/"+f.realm+"/clients/"+f.cu.ID:
			body, _ := io.ReadAll(r.Body)
			var rep map[string]json.RawMessage
			if err := json.Unmarshal(body, &rep); err != nil {
				t.Fatalf("PUT body not a JSON object: %v", err)
			}
			// The untouched attribute MUST survive the round-trip.
			if _, ok := rep["someOtherKey"]; !ok {
				t.Errorf("PUT dropped an untouched client attribute (someOtherKey): %s", body)
			}
			var updated kcClientRep
			if err := json.Unmarshal(body, &updated); err != nil {
				t.Fatalf("PUT body decode: %v", err)
			}
			f.cu.RedirectUris = updated.RedirectUris
			f.cu.WebOrigins = updated.WebOrigins
			f.putCount++
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestRegisterOrgConsoleRedirectURI_Concrete proves the CORE requirement:
// slug "acme" + pool "omani.homes" registers the concrete
// https://console.acme.omani.homes/* (a trailing `*`, no mid-host `*`), while
// preserving the pre-existing sovereign-admin host.
func TestRegisterOrgConsoleRedirectURI_Concrete(t *testing.T) {
	const (
		realm    = "sovereign"
		sovFQDN  = "hw302.omantel.biz"
		wantURI  = "https://console.acme.omani.homes/*"
		wantOrig = "https://console.acme.omani.homes"
		sovURI   = "https://console.hw302.omantel.biz/*"
	)
	adm := newFakeKCAdmin(realm, sovFQDN)
	srv := httptest.NewServer(adm.handler(t))
	defer srv.Close()

	kc := NewLiveKeycloak(srv.URL, realm, "org-controller-sa", "secret")

	if err := kc.RegisterOrgConsoleRedirectURI(context.Background(), "acme", "omani.homes"); err != nil {
		t.Fatalf("RegisterOrgConsoleRedirectURI: %v", err)
	}

	// The concrete, trailing-* per-Org callback is now on the client.
	if !containsStr(adm.cu.RedirectUris, wantURI) {
		t.Errorf("concrete per-Org redirectUri not registered.\n got: %v\nwant contains: %s",
			adm.cu.RedirectUris, wantURI)
	}
	if !containsStr(adm.cu.WebOrigins, wantOrig) {
		t.Errorf("per-Org webOrigin not registered.\n got: %v\nwant contains: %s",
			adm.cu.WebOrigins, wantOrig)
	}
	// It must NOT be a mid-host wildcard — that is the exact #6504 defect.
	if containsStr(adm.cu.RedirectUris, "https://console.*.omani.homes/*") {
		t.Errorf("registered a mid-host wildcard (inert on Keycloak 26.3) instead of a concrete host: %v",
			adm.cu.RedirectUris)
	}
	// The working sovereign-admin host must be preserved (never regress it).
	if !containsStr(adm.cu.RedirectUris, sovURI) {
		t.Errorf("sovereign-admin console redirectUri was dropped: %v", adm.cu.RedirectUris)
	}
	if adm.putCount != 1 {
		t.Errorf("expected exactly 1 PUT on first registration, got %d", adm.putCount)
	}

	// Idempotency: a second register of the same host makes ZERO writes.
	if err := kc.RegisterOrgConsoleRedirectURI(context.Background(), "acme", "omani.homes"); err != nil {
		t.Fatalf("second RegisterOrgConsoleRedirectURI: %v", err)
	}
	if adm.putCount != 1 {
		t.Errorf("steady-state re-register must not PUT again, got putCount=%d", adm.putCount)
	}
}

// TestDeregisterOrgConsoleRedirectURI removes exactly the concrete per-Org
// callback and leaves the sovereign-admin host intact; a second deregister is
// a write-free no-op (absent-as-success).
func TestDeregisterOrgConsoleRedirectURI(t *testing.T) {
	const (
		realm   = "sovereign"
		sovFQDN = "hw302.omantel.biz"
		orgURI  = "https://console.acme.omani.homes/*"
		sovURI  = "https://console.hw302.omantel.biz/*"
	)
	adm := newFakeKCAdmin(realm, sovFQDN)
	// Seed the client as if the up-path already registered the per-Org host.
	adm.cu.RedirectUris = append(adm.cu.RedirectUris, orgURI)
	adm.cu.WebOrigins = append(adm.cu.WebOrigins, "https://console.acme.omani.homes")
	srv := httptest.NewServer(adm.handler(t))
	defer srv.Close()

	kc := NewLiveKeycloak(srv.URL, realm, "org-controller-sa", "secret")

	if err := kc.DeregisterOrgConsoleRedirectURI(context.Background(), "acme", "omani.homes"); err != nil {
		t.Fatalf("DeregisterOrgConsoleRedirectURI: %v", err)
	}
	if containsStr(adm.cu.RedirectUris, orgURI) {
		t.Errorf("per-Org redirectUri not removed: %v", adm.cu.RedirectUris)
	}
	if !containsStr(adm.cu.RedirectUris, sovURI) {
		t.Errorf("deregister wrongly removed the sovereign-admin host: %v", adm.cu.RedirectUris)
	}
	if adm.putCount != 1 {
		t.Errorf("expected exactly 1 PUT on deregister, got %d", adm.putCount)
	}

	// Absent-as-success: deregistering again makes no further write.
	if err := kc.DeregisterOrgConsoleRedirectURI(context.Background(), "acme", "omani.homes"); err != nil {
		t.Fatalf("second DeregisterOrgConsoleRedirectURI: %v", err)
	}
	if adm.putCount != 1 {
		t.Errorf("no-op deregister must not PUT again, got putCount=%d", adm.putCount)
	}
}

// TestOrgConsoleRedirectURI_Builder locks the exact string shape the task
// specifies: console.<slug>.<poolTLD> with a trailing /* redirect + bare origin.
func TestOrgConsoleRedirectURI_Builder(t *testing.T) {
	redirect, origin := orgConsoleRedirectURI("acme", "omani.homes")
	if redirect != "https://console.acme.omani.homes/*" {
		t.Errorf("redirect = %q, want https://console.acme.omani.homes/*", redirect)
	}
	if origin != "https://console.acme.omani.homes" {
		t.Errorf("origin = %q, want https://console.acme.omani.homes", origin)
	}
}
