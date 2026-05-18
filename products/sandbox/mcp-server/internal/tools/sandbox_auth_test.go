package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestSandboxRealmName_Format pins the deterministic per-Sandbox realm
// naming. listClients + registerClient depend on the agent not being
// able to swap the realm, so any drift here is a test regression.
func TestSandboxRealmName_Format(t *testing.T) {
	cases := map[string]struct {
		env  *Env
		want string
	}{
		"happy":   {&Env{OrgID: "acme", SandboxID: "emrah"}, "sandbox-acme-emrah"},
		"upper":   {&Env{OrgID: "ACME", SandboxID: "EMRAH"}, "sandbox-acme-emrah"},
		"trimmed": {&Env{OrgID: " acme ", SandboxID: " emrah "}, "sandbox-acme-emrah"},
		"missing": {&Env{OrgID: "acme"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := sandboxRealmName(tc.env); got != tc.want {
				t.Errorf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

// TestRequireKeycloak covers the gate every sandbox.auth.* handler runs
// before issuing any HTTP call.
func TestRequireKeycloak(t *testing.T) {
	if err := requireKeycloak(nil); err == nil || !strings.Contains(err.Error(), "env missing") {
		t.Errorf("nil env: err=%v", err)
	}
	if err := requireKeycloak(&Env{}); err == nil || !strings.Contains(err.Error(), "KEYCLOAK_ADMIN_URL") {
		t.Errorf("empty url: err=%v", err)
	}
	if err := requireKeycloak(&Env{KeycloakAdminURL: "http://kc"}); err == nil || !strings.Contains(err.Error(), "KEYCLOAK_ADMIN_TOKEN") {
		t.Errorf("empty token: err=%v", err)
	}
	if err := requireKeycloak(&Env{KeycloakAdminURL: "http://kc", KeycloakAdminToken: "t"}); err != nil {
		t.Errorf("populated: err=%v", err)
	}
}

// TestSandboxAuthProvisionRealm_Validation covers input-validation
// failures before any HTTP hop.
func TestSandboxAuthProvisionRealm_Validation(t *testing.T) {
	cases := map[string]struct {
		args    string
		wantErr string
	}{
		"missing":      {`{}`, "must match"},
		"too short":    {`{"realm_name":"ab"}`, "must match"},
		"uppercase":    {`{"realm_name":"Abcd"}`, "must match"},
		"leading dash": {`{"realm_name":"-abc"}`, "must match"},
		"valid":        {`{"realm_name":"app1"}`, "KEYCLOAK_ADMIN_URL"}, // passes validation, falls through to env gate
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := WithEnv(context.Background(), &Env{})
			_, err := sandboxAuthProvisionRealm(ctx, json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err=%v want substring %q", err, tc.wantErr)
			}
		})
	}
}

// TestSandboxAuthProvisionRealm_HTTPRoundtrip wires a real httptest
// server to assert the handler posts the expected body shape to
// /admin/realms and surfaces 201 / 409 distinctly.
func TestSandboxAuthProvisionRealm_HTTPRoundtrip(t *testing.T) {
	var gotPath, gotMethod, gotBody, gotAuth string
	respStatus := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(respStatus)
	}))
	defer srv.Close()

	env := &Env{
		OrgID:              "acme",
		SandboxID:          "emrah",
		KeycloakAdminURL:   srv.URL,
		KeycloakAdminToken: "tok-X",
	}
	ctx := WithEnv(context.Background(), env)

	// happy path
	out, err := sandboxAuthProvisionRealm(ctx, json.RawMessage(`{"realm_name":"app1"}`))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	m := out.(map[string]any)
	if m["status"] != "Created" {
		t.Errorf("status=%v", m["status"])
	}
	if gotPath != "/admin/realms" {
		t.Errorf("path=%q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method=%q", gotMethod)
	}
	if gotAuth != "Bearer tok-X" {
		t.Errorf("auth=%q", gotAuth)
	}
	if !strings.Contains(gotBody, `"realm":"app1"`) {
		t.Errorf("body=%q missing realm", gotBody)
	}
	if !strings.Contains(gotBody, `"openova-sandbox-mcp"`) {
		t.Errorf("body=%q missing managed-by attribute", gotBody)
	}

	// idempotent: 409 conflict path
	respStatus = http.StatusConflict
	out, err = sandboxAuthProvisionRealm(ctx, json.RawMessage(`{"realm_name":"app1"}`))
	if err != nil {
		t.Fatalf("409 path err=%v", err)
	}
	if out.(map[string]any)["status"] != "AlreadyExists" {
		t.Errorf("409 status=%v", out.(map[string]any)["status"])
	}
}

// TestSandboxAuthListClients_HTTPRoundtrip asserts the GET URL hits the
// Sandbox's own realm AND surfaces a friendly empty list on 404.
func TestSandboxAuthListClients_HTTPRoundtrip(t *testing.T) {
	var gotPath string
	respStatus := http.StatusOK
	respBody := `[{"id":"uuid-1","clientId":"acme-app","enabled":true,"publicClient":false,"protocol":"openid-connect","redirectUris":["https://app/*"]}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(respStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	env := &Env{
		OrgID:              "acme",
		SandboxID:          "emrah",
		KeycloakAdminURL:   srv.URL,
		KeycloakAdminToken: "tok",
	}
	ctx := WithEnv(context.Background(), env)

	// happy
	out, err := sandboxAuthListClients(ctx, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	m := out.(map[string]any)
	if m["realm"] != "sandbox-acme-emrah" {
		t.Errorf("realm=%v", m["realm"])
	}
	if m["count"] != 1 {
		t.Errorf("count=%v", m["count"])
	}
	if gotPath != "/admin/realms/sandbox-acme-emrah/clients" {
		t.Errorf("path=%q", gotPath)
	}

	// realm not found → empty list + note
	respStatus = http.StatusNotFound
	respBody = ""
	out, err = sandboxAuthListClients(ctx, nil)
	if err != nil {
		t.Fatalf("404 err=%v", err)
	}
	m = out.(map[string]any)
	if m["count"] != 0 {
		t.Errorf("404 count=%v", m["count"])
	}
	if !strings.Contains(m["note"].(string), "provisionRealm first") {
		t.Errorf("404 note=%v", m["note"])
	}
}

// TestSandboxAuthRegisterClient_Validation pins the per-argument
// validation rules.
func TestSandboxAuthRegisterClient_Validation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{OrgID: "acme", SandboxID: "emrah"})
	cases := map[string]struct {
		args    string
		wantErr string
	}{
		"missing client_id":      {`{"redirect_uris":["https://x/*"]}`, "must match"},
		"bad client_id":          {`{"client_id":"a","redirect_uris":["https://x/*"]}`, "must match"},
		"missing redirect_uris":  {`{"client_id":"acme-app"}`, "at least one entry"},
		"empty redirect_uris":    {`{"client_id":"acme-app","redirect_uris":[]}`, "at least one entry"},
		"empty entry":            {`{"client_id":"acme-app","redirect_uris":[" "]}`, "empty entry"},
		"valid but kc not wired": {`{"client_id":"acme-app","redirect_uris":["https://x/*"]}`, "KEYCLOAK_ADMIN_URL"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := sandboxAuthRegisterClient(ctx, json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err=%v want %q", err, tc.wantErr)
			}
		})
	}
}

// TestSandboxAuthRegisterClient_HTTPRoundtrip pins the POST body shape
// + the 404 → "provision realm first" guard.
func TestSandboxAuthRegisterClient_HTTPRoundtrip(t *testing.T) {
	var gotBody, gotPath string
	respStatus := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(respStatus)
	}))
	defer srv.Close()

	env := &Env{
		OrgID:              "acme",
		SandboxID:          "emrah",
		KeycloakAdminURL:   srv.URL,
		KeycloakAdminToken: "tok",
	}
	ctx := WithEnv(context.Background(), env)

	// happy
	args := `{"client_id":"acme-app","redirect_uris":["https://app/*"],"public_client":true}`
	out, err := sandboxAuthRegisterClient(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if out.(map[string]any)["status"] != "Created" {
		t.Errorf("status=%v", out.(map[string]any)["status"])
	}
	if gotPath != "/admin/realms/sandbox-acme-emrah/clients" {
		t.Errorf("path=%q", gotPath)
	}
	if !strings.Contains(gotBody, `"clientId":"acme-app"`) {
		t.Errorf("body missing clientId: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"publicClient":true`) {
		t.Errorf("body missing publicClient=true: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"redirectUris":["https://app/*"]`) {
		t.Errorf("body missing redirectUris: %s", gotBody)
	}

	// realm not found → typed error
	respStatus = http.StatusNotFound
	_, err = sandboxAuthRegisterClient(ctx, json.RawMessage(args))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("404 err=%v want 'does not exist'", err)
	}
}

// TestSandboxAuthCapabilityGate confirms the registry rejects
// sandbox.auth.* calls whose claims lack the `sandbox.auth` capability.
func TestSandboxAuthCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:     "acme",
		JWTSecret: []byte("x"),
		SandboxID: "emrah",
	})
	for _, name := range []string{
		"sandbox.auth.provisionRealm",
		"sandbox.auth.listClients",
		"sandbox.auth.registerClient",
	} {
		t.Run(name, func(t *testing.T) {
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.db"}}
			args := json.RawMessage(`{"realm_name":"app1","client_id":"a","redirect_uris":["https://x/*"]}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err=%v want forbidden", err)
			}
		})
	}
}
