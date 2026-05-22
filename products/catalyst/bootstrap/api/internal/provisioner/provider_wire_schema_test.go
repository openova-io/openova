// provider_wire_schema_test.go — Wave 4 (Refs #2140) coverage for the
// top-level Provider field + Huawei credential triplet on Request.
//
// Background: Wave 2 (PR #2141) introduced the CloudProvider seam, Wave 3
// (PR #2142) shipped the Huawei adapter, but the wire-facing
// provisioner.Request struct kept only `HetznerToken` / `HetznerProjectID`
// as top-level credentials. A POST /api/v1/sovereign/deployments body
// with `provider:"huawei"` therefore had no field to carry the AK/SK +
// project_id, and the handler could not construct a valid
// ProvisionSpec.Creds.Raw for the Huawei adapter.
//
// This file pins:
//
//   1. Empty Provider field auto-populates to "hetzner" + still requires
//      HetznerToken/HetznerProjectID (back-compat).
//   2. Provider == "huawei" requires all three Huawei fields (access key,
//      secret key, project ID); each missing field surfaces as a distinct
//      400 with the offending field name.
//   3. An unrecognised Provider value rejects with the "unsupported
//      provider" message + the allow-list.
//   4. writeTfvars() emits the Huawei keys (huawei_access_key etc.) into
//      tofu.auto.tfvars.json when Provider == "huawei", with the region
//      defaulting to "me-east-215" when the operator omits it.
//   5. writeTfvars() does NOT emit Huawei keys for a Hetzner-provider
//      Request — credential hygiene + smaller tfvars file.

package provisioner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// huaweiValidBase mirrors validBase() but populates the Huawei credential
// triplet AND sets Provider="huawei" so the surrounding HetznerToken-
// required check is satisfied via the switch's huawei branch.
func huaweiValidBase() Request {
	r := validBase()
	r.Provider = "huawei"
	r.HetznerToken = ""     // not relevant for huawei; switch must allow empty
	r.HetznerProjectID = "" // ditto
	r.HuaweiAccessKey = "TESTAK1234567890"
	r.HuaweiSecretKey = "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
	r.HuaweiProjectID = "0a1b2c3d4e5f6789abcd"
	// HuaweiRegion intentionally empty so the writeTfvars() default-fallback
	// path is exercised by TestWriteTfvars_Huawei_DefaultsRegion.
	r.Region = "me-east-215"
	r.ControlPlaneSize = "s7n.large.4"
	return r
}

// TestRequestValidate_Provider_DefaultsHetzner — empty Provider field
// auto-populates to "hetzner" so existing wizard payloads (every wizard
// payload as of Wave 3) keep landing as Hetzner deployments without a
// wire change. The HetznerToken-required gate must then fire on the
// hetzner branch as before.
func TestRequestValidate_Provider_DefaultsHetzner(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	// Provider intentionally empty.

	if err := r.Validate(); err != nil {
		t.Fatalf("empty Provider + valid hetzner fields should pass: %v", err)
	}
	if r.Provider != "hetzner" {
		t.Errorf("Provider was not normalised to %q: got %q", "hetzner", r.Provider)
	}

	// Removing HetznerToken must now reject with the existing
	// "hetzner token is required" message — proves the switch's
	// default branch still gates on HetznerToken.
	r.HetznerToken = ""
	err := r.Validate()
	if err == nil || !strings.Contains(err.Error(), "hetzner token is required") {
		t.Fatalf("missing HetznerToken should reject in default branch, got %v", err)
	}
}

// TestRequestValidate_Provider_HuaweiRequiresAccessKey — the three
// missing-Huawei-field cases each surface as a distinct 400 with the
// offending field name in the message. Run as a table-driven test so a
// future fourth required Huawei field (e.g. domain_id when HCS exposes
// it) lands here as one new row without restructuring the test body.
func TestRequestValidate_Provider_HuaweiRequiresAllThreeFields(t *testing.T) {
	cases := []struct {
		name      string
		patch     func(*Request)
		wantField string
	}{
		{
			name: "missing-access-key",
			patch: func(r *Request) {
				r.HuaweiAccessKey = ""
			},
			wantField: "huaweiAccessKey",
		},
		{
			name: "missing-secret-key",
			patch: func(r *Request) {
				r.HuaweiSecretKey = ""
			},
			wantField: "huaweiSecretKey",
		},
		{
			name: "missing-project-id",
			patch: func(r *Request) {
				r.HuaweiProjectID = ""
			},
			wantField: "huaweiProjectID",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := huaweiValidBase()
			tc.patch(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("missing %s should be rejected", tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("error must name the offending field %q, got %q",
					tc.wantField, err.Error())
			}
			if !strings.Contains(err.Error(), "huawei") {
				t.Errorf("error must mention provider=huawei context, got %q", err.Error())
			}
		})
	}
}

// TestRequestValidate_Provider_HuaweiHappyPath — Provider=huawei with
// all three credential fields populated must validate cleanly + the
// normalisation step (lower-case + trim) must leave the value at
// "huawei".
func TestRequestValidate_Provider_HuaweiHappyPath(t *testing.T) {
	r := huaweiValidBase()
	if err := r.Validate(); err != nil {
		t.Fatalf("populated Huawei request should validate: %v", err)
	}
	if r.Provider != "huawei" {
		t.Errorf("Provider lost normalisation: got %q, want huawei", r.Provider)
	}
}

// TestRequestValidate_Provider_HuaweiCaseInsensitive — operators
// occasionally type "Huawei" / "HUAWEI". The validator must accept
// these as identical to "huawei" + normalise the field in place so
// downstream consumers (the registry, writeTfvars, the persisted
// record) see a single canonical lower-case form.
func TestRequestValidate_Provider_HuaweiCaseInsensitive(t *testing.T) {
	for _, variant := range []string{"Huawei", "HUAWEI", " huawei ", "\thuawei\n"} {
		t.Run(variant, func(t *testing.T) {
			r := huaweiValidBase()
			r.Provider = variant
			if err := r.Validate(); err != nil {
				t.Fatalf("variant %q should validate as huawei: %v", variant, err)
			}
			if r.Provider != "huawei" {
				t.Errorf("variant %q was not normalised: got %q", variant, r.Provider)
			}
		})
	}
}

// TestRequestValidate_Provider_UnknownRejected — an unrecognised
// provider name (typo, future provider not yet implemented, or a
// malicious caller probing for default-dispatch behaviour) must reject
// with an actionable message that lists the supported names.
func TestRequestValidate_Provider_UnknownRejected(t *testing.T) {
	for _, bad := range []string{"aws", "gcp", "azure", "oci", "contabo", "foo"} {
		t.Run(bad, func(t *testing.T) {
			r := validBase()
			r.Region = "fsn1"
			r.ControlPlaneSize = "cx42"
			r.Provider = bad
			err := r.Validate()
			if err == nil {
				t.Fatalf("provider %q should be rejected", bad)
			}
			if !strings.Contains(err.Error(), "unsupported provider") {
				t.Errorf("error must mention 'unsupported provider', got %q", err.Error())
			}
			if !strings.Contains(err.Error(), "hetzner") || !strings.Contains(err.Error(), "huawei") {
				t.Errorf("error must list supported providers, got %q", err.Error())
			}
		})
	}
}

// TestRequest_HuaweiSecretKey_NotSerializedInPersistedRedaction — this
// test is the inverse of TestRequest_GHCRPullToken_NotSerialized: the
// Huawei secret key IS serialised in the wire payload (it carries a
// json:"huaweiSecretKey,omitempty" tag, NOT json:"-") because the
// wizard's POST body has to ship it. The actual credential hygiene
// happens at the internal/store.Redact boundary — that's a separate
// test in internal/store/. Here we just pin the wire shape: a
// populated HuaweiSecretKey lands in the marshaled JSON under the
// expected key.
func TestRequest_HuaweiSecretKey_SerialisedOnWire(t *testing.T) {
	const sentinel = "TESTSK_WIRE_FORMAT_PROBE_NOT_REAL"
	r := Request{HuaweiSecretKey: sentinel}

	raw, err := jsonMarshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(raw, "huaweiSecretKey") {
		t.Errorf("Request.HuaweiSecretKey JSON tag missing: %s", raw)
	}
	if !strings.Contains(raw, sentinel) {
		t.Errorf("Request.HuaweiSecretKey value not in marshaled output: %s", raw)
	}
}

// TestRequest_HuaweiFields_OmitEmpty — empty Huawei fields must NOT
// serialise. Two reasons: (1) the wire payload stays compact for the
// majority of Hetzner provisions, and (2) the persisted record never
// carries dangling empty-string credentials that a future audit might
// mistake for a leaked cred.
func TestRequest_HuaweiFields_OmitEmpty(t *testing.T) {
	r := Request{} // all Huawei fields empty

	raw, err := jsonMarshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		"huaweiAccessKey", "huaweiSecretKey", "huaweiProjectID", "huaweiRegion",
	} {
		if strings.Contains(raw, key) {
			t.Errorf("empty %s should be omitted from JSON; got: %s", key, raw)
		}
	}
}

// TestWriteTfvars_Huawei_EmitsCredsAndProvider — for a Provider=huawei
// Request, writeTfvars() MUST emit:
//
//   - `provider`: "huawei"
//   - `huawei_access_key` / `huawei_secret_key` / `huawei_project_id`
//   - `huawei_region` defaulted to "me-east-215" when omitted
//
// The OpenTofu module at infra/providers/huawei/variables.tf declares
// the matching `var.huawei_*` set; this test pins the tfvars contract.
func TestWriteTfvars_Huawei_EmitsCredsAndProvider(t *testing.T) {
	dir := t.TempDir()
	r := huaweiValidBase()
	if err := r.Validate(); err != nil {
		t.Fatalf("seed Request did not validate: %v", err)
	}
	if err := writeTfvars(dir, r); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}

	got := readTfvars(t, dir)
	if got["provider"] != "huawei" {
		t.Errorf("tfvars[provider] = %v, want huawei", got["provider"])
	}
	if got["huawei_access_key"] != r.HuaweiAccessKey {
		t.Errorf("tfvars[huawei_access_key] = %v, want %q", got["huawei_access_key"], r.HuaweiAccessKey)
	}
	if got["huawei_secret_key"] != r.HuaweiSecretKey {
		t.Errorf("tfvars[huawei_secret_key] = %v, want %q", got["huawei_secret_key"], r.HuaweiSecretKey)
	}
	if got["huawei_project_id"] != r.HuaweiProjectID {
		t.Errorf("tfvars[huawei_project_id] = %v, want %q", got["huawei_project_id"], r.HuaweiProjectID)
	}
	if got["huawei_region"] != "me-east-215" {
		t.Errorf("tfvars[huawei_region] = %v, want me-east-215 (default)", got["huawei_region"])
	}
}

// TestWriteTfvars_Huawei_RespectsExplicitRegion — when the operator
// supplies a non-default HuaweiRegion (e.g. public Huawei Cloud
// `ap-southeast-1`), writeTfvars() must honour it instead of
// stomping with the HCS default.
func TestWriteTfvars_Huawei_RespectsExplicitRegion(t *testing.T) {
	dir := t.TempDir()
	r := huaweiValidBase()
	r.HuaweiRegion = "ap-southeast-1"
	if err := r.Validate(); err != nil {
		t.Fatalf("seed Request did not validate: %v", err)
	}
	if err := writeTfvars(dir, r); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	got := readTfvars(t, dir)
	if got["huawei_region"] != "ap-southeast-1" {
		t.Errorf("explicit HuaweiRegion not honoured: got %v, want ap-southeast-1", got["huawei_region"])
	}
}

// TestWriteTfvars_Hetzner_OmitsHuaweiKeys — for a Provider=hetzner
// Request, writeTfvars() MUST NOT emit any `huawei_*` key. Credential
// hygiene: Huawei creds never land in a Hetzner provision's tfvars
// file, even if the Request struct happened to be re-used from a
// Huawei context.
func TestWriteTfvars_Hetzner_OmitsHuaweiKeys(t *testing.T) {
	dir := t.TempDir()
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	// Set Huawei fields to confirm they don't leak even when populated.
	r.HuaweiAccessKey = "STALE_FROM_PREV_REQUEST"
	r.HuaweiSecretKey = "STALE_FROM_PREV_REQUEST"
	r.HuaweiProjectID = "STALE_FROM_PREV_REQUEST"
	if err := r.Validate(); err != nil {
		t.Fatalf("seed Request did not validate: %v", err)
	}
	if err := writeTfvars(dir, r); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	got := readTfvars(t, dir)
	if got["provider"] != "hetzner" {
		t.Errorf("tfvars[provider] = %v, want hetzner", got["provider"])
	}
	for _, key := range []string{
		"huawei_access_key", "huawei_secret_key", "huawei_project_id", "huawei_region",
	} {
		if _, ok := got[key]; ok {
			t.Errorf("tfvars[%s] leaked into a Hetzner provision: %v", key, got[key])
		}
	}
	// Also confirm raw bytes never carry the sentinel value — a tighter
	// proof against a future regression that emits the key with an
	// empty string (which would round-trip through map decoding fine
	// but still leak credentials in the bytes).
	raw, err := os.ReadFile(filepath.Join(dir, "tofu.auto.tfvars.json"))
	if err != nil {
		t.Fatalf("read tfvars: %v", err)
	}
	if strings.Contains(string(raw), "STALE_FROM_PREV_REQUEST") {
		t.Errorf("Hetzner tfvars carried stale Huawei creds: %s", raw)
	}
}

// TestResolveModulePath_SwapsProviderDirectory — the per-Request
// dispatch path through Provision()/Destroy() relies on
// resolveModulePath swapping the trailing directory of the configured
// ModulePath to match req.Provider. Pin the contract end-to-end so a
// future refactor that breaks this dispatch lands here as a test
// failure rather than as a silent "wrong tfvars module ran".
func TestResolveModulePath_SwapsProviderDirectory(t *testing.T) {
	cases := []struct {
		name       string
		modulePath string
		provider   string
		want       string
	}{
		{
			name:       "canonical-hetzner-default",
			modulePath: "/infra/providers/hetzner",
			provider:   "hetzner",
			want:       "/infra/providers/hetzner",
		},
		{
			name:       "canonical-huawei-from-hetzner-default",
			modulePath: "/infra/providers/hetzner",
			provider:   "huawei",
			want:       "/infra/providers/huawei",
		},
		{
			name:       "air-gap-custom-mount-hetzner",
			modulePath: "/mnt/iac/infra/providers/hetzner",
			provider:   "huawei",
			want:       "/mnt/iac/infra/providers/huawei",
		},
		{
			name:       "trailing-slash-tolerated",
			modulePath: "/infra/providers/hetzner/",
			provider:   "huawei",
			want:       "/infra/providers/huawei",
		},
		{
			name:       "empty-provider-falls-back-to-hetzner",
			modulePath: "/infra/providers/hetzner",
			provider:   "",
			want:       "/infra/providers/hetzner",
		},
		{
			name:       "case-insensitive-provider-lower-cased",
			modulePath: "/infra/providers/hetzner",
			provider:   "HUAWEI",
			want:       "/infra/providers/huawei",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Provisioner{ModulePath: tc.modulePath}
			got := p.resolveModulePath(tc.provider)
			if got != tc.want {
				t.Errorf("resolveModulePath(%q) with ModulePath=%q = %q, want %q",
					tc.provider, tc.modulePath, got, tc.want)
			}
		})
	}
}

// readTfvars decodes the JSON tfvars file writeTfvars produced into a
// map for keyed lookups. Test-only helper.
func readTfvars(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "tofu.auto.tfvars.json"))
	if err != nil {
		t.Fatalf("read tfvars: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode tfvars: %v", err)
	}
	return got
}
