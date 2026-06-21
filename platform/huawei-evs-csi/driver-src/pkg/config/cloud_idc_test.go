package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/gcfg.v1"
)

// loadFromString parses an INI cloud-config the same way LoadConfig does from a
// file, then applies the package defaults — without touching the filesystem.
func loadFromString(t *testing.T, ini string) *CloudCredentials {
	t.Helper()
	cc := &CloudCredentials{}
	if err := gcfg.FatalOnly(gcfg.ReadStringInto(cc, ini)); err != nil {
		t.Fatalf("parse cloud-config: %v", err)
	}
	setDefaultConfig(cc)
	return cc
}

const idcINI = `
[Global]
access-key=AKIDC000000000000000
secret-key=SKIDC000000000000000
project-id=proj-me-east-215-abc123
region=me-east-215
cloud=kom4dc.nationalcloud.om
auth-url=https://iam.me-east-215.kom4dc.nationalcloud.om/v3
insecure=true
idc=true
`

// TestIDCBypass_NoKeystone is the regression guard for #3971: when idc=true the
// driver must build a usable cloud client WITHOUT calling Keystone
// (openstack.Authenticate → /v3/auth/catalog + /v3/auth/tokens). On HCS
// me-east-215 those endpoints return APIGW.0101 "API does not exist", so the
// stock driver CrashLoops at startup. The bypass must complete Validate() with
// zero network round-trips and set the AK/SK signing options so every later EVS
// request is signed directly.
func TestIDCBypass_NoKeystone(t *testing.T) {
	cc := loadFromString(t, idcINI)
	if !cc.Global.Idc {
		t.Fatalf("idc flag did not parse as true")
	}

	if err := cc.Validate(); err != nil {
		t.Fatalf("idc Validate() must not error (and must not call Keystone): %v", err)
	}

	pc := cc.CloudClient
	if pc == nil {
		t.Fatalf("idc Validate() left CloudClient nil")
	}
	// The golangsdk per-request signer engages purely on
	// AKSKAuthOptions.AccessKey != "" (provider_client.go doRequest). Without
	// these set the driver would issue UNSIGNED EVS calls → 401.
	if pc.AKSKAuthOptions.AccessKey != "AKIDC000000000000000" {
		t.Errorf("AK not propagated to signer: got %q", pc.AKSKAuthOptions.AccessKey)
	}
	if pc.AKSKAuthOptions.SecretKey != "SKIDC000000000000000" {
		t.Errorf("SK not propagated to signer")
	}
	if pc.AKSKAuthOptions.ProjectId != "proj-me-east-215-abc123" {
		t.Errorf("ProjectId not propagated to signer: got %q", pc.AKSKAuthOptions.ProjectId)
	}
	if pc.ProjectID != "proj-me-east-215-abc123" {
		t.Errorf("ProviderClient.ProjectID not set: got %q", pc.ProjectID)
	}
}

// TestIDCBypass_EVSEndpoint confirms the EVS service client derives the live
// kom4dc endpoint locally (https://evs.<region>.<cloud>/) — proving the bypass
// never needs the Keystone catalog for endpoint discovery.
func TestIDCBypass_EVSEndpoint(t *testing.T) {
	cc := loadFromString(t, idcINI)
	if err := cc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	evs, err := cc.EvsV2Client()
	if err != nil {
		t.Fatalf("EvsV2Client: %v", err)
	}
	wantEndpoint := "https://evs.me-east-215.kom4dc.nationalcloud.om/"
	if evs.Endpoint != wantEndpoint {
		t.Errorf("EVS endpoint = %q, want %q", evs.Endpoint, wantEndpoint)
	}
	wantBase := wantEndpoint + "v2/proj-me-east-215-abc123/"
	if evs.ResourceBase != wantBase {
		t.Errorf("EVS ResourceBase = %q, want %q", evs.ResourceBase, wantBase)
	}
}

// TestIDCBypass_RequiresProjectID asserts the guard: with no Keystone we cannot
// resolve project-name → project-id, so an empty project-id must fail fast with
// a clear error rather than emitting EVS calls to /<version>//cloudvolumes.
func TestIDCBypass_RequiresProjectID(t *testing.T) {
	ini := strings.Replace(idcINI, "project-id=proj-me-east-215-abc123\n", "", 1)
	cc := loadFromString(t, ini)
	err := cc.Validate()
	if err == nil {
		t.Fatalf("idc mode with empty project-id must error")
	}
	if !strings.Contains(err.Error(), "project-id") {
		t.Errorf("error should name project-id, got: %v", err)
	}
}

// TestIDCBypass_DoesNotDialKeystone is the strongest guard: it points auth-url
// at a local httptest server and asserts the server receives ZERO requests
// during Validate() when idc=true. Any hit means a Keystone call leaked through
// — exactly the CrashLoop cause on HCS.
func TestIDCBypass_DoesNotDialKeystone(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Mimic the HCS APIGW rejection so a leaked call is unmistakable.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error_code":"APIGW.0101","error_msg":"The API does not exist or has not been published in the environment"}`))
	}))
	defer srv.Close()

	ini := strings.Replace(idcINI,
		"auth-url=https://iam.me-east-215.kom4dc.nationalcloud.om/v3",
		"auth-url="+srv.URL+"/v3", 1)
	cc := loadFromString(t, ini)

	if err := cc.Validate(); err != nil {
		t.Fatalf("idc Validate() must not error: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("idc bypass leaked %d Keystone call(s) to auth-url — must be 0", n)
	}
}

// TestNonIDC_StillCallsKeystone proves we did NOT break public Huawei Cloud:
// with idc unset/false the stock flow runs and DOES dial auth-url (Keystone).
// We point auth-url at a local server and assert it gets hit.
func TestNonIDC_StillCallsKeystone(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Return a deliberately empty/garbage body so Authenticate fails fast
		// after the dial — we only care that the dial HAPPENED.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ini := `
[Global]
access-key=AKPUB0000000000000000
secret-key=SKPUB0000000000000000
project-id=proj-pub
region=cn-north-4
cloud=myhuaweicloud.com
auth-url=` + srv.URL + `/v3
insecure=true
`
	cc := loadFromString(t, ini)
	if cc.Global.Idc {
		t.Fatalf("idc should default false")
	}
	// We expect an error (the fake Keystone returns 401), but the POINT is that
	// the dial occurred — i.e. the non-idc path is unchanged.
	_ = cc.Validate()
	if n := atomic.LoadInt32(&hits); n == 0 {
		t.Fatalf("non-idc path must still dial Keystone (auth-url) — got 0 hits, regression")
	}
}
