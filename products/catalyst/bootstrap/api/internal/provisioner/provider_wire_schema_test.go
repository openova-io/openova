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
//   1. An empty Provider is a *providers.FieldError on "provider": it is
//      never defaulted (CLD-103). A named provider requires the credential
//      fields its cloud profile marks required.
//   2. Provider == "huawei" requires all three Huawei credentials (access
//      key, secret key, project ID); each missing one surfaces as a distinct
//      field error naming its credential key.
//   3. An unrecognised Provider value rejects with the "unsupported
//      provider" message + the allow-list.
//   4. writeTfvars() emits the Huawei keys (huawei_access_key etc.) into
//      tofu.auto.tfvars.json when Provider == "huawei", with the region
//      defaulting to "me-east-215" when the operator omits it.
//   5. writeTfvars() does NOT emit Huawei keys for a Hetzner-provider
//      Request — credential hygiene + smaller tfvars file.
//   6. The operator's own credentials are the files
//      cloud-credential.<provider>.<key> of --cloud-credentials-dir, never
//      an environment variable (CLD-106).

package provisioner

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// huaweiValidBase mirrors validBase() with Provider="huawei" and the three
// credential keys of the Huawei profile in the credentials map instead of
// the Hetzner ones.
func huaweiValidBase() Request {
	r := validBase()
	r.Provider = "huawei"
	r.Credentials = map[string]string{
		"access_key": "TESTAK1234567890",
		"secret_key": "TESTSKABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890",
		"project_id": "0a1b2c3d4e5f6789abcd",
	}
	r.Region = "me-east-215"
	r.ControlPlaneSize = "s7n.large.4"
	return r
}

// TestRequestValidate_Provider_EmptyIsAFieldError — an empty Provider is
// refused with the field error of the port, never defaulted to a pack
// (CLD-103); a named pack then requires its profile's credential fields.
func TestRequestValidate_Provider_EmptyIsAFieldError(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.Provider = ""
	err := r.Validate()
	var fe *providers.FieldError
	if !errors.As(err, &fe) || fe.Field != "provider" {
		t.Fatalf("empty Provider: got %v, want a *providers.FieldError on provider", err)
	}

	r.Provider = "hetzner"
	if err := r.Validate(); err != nil {
		t.Fatalf("Provider hetzner + valid fields should pass: %v", err)
	}

	// The profile marks hcloud_token required: without it the request is
	// refused with a field error naming the credential key.
	r = validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.SetCredential("hcloud_token", "")
	err = r.Validate()
	if !errors.As(err, &fe) || fe.Field != "credentials.hcloud_token" {
		t.Fatalf("missing hcloud_token: got %v, want a field error on credentials.hcloud_token", err)
	}
}

// TestValidate_CredentialsMapAndObjectStorageFieldsAgree — the credentials
// map is the only place of a cloud credential (CLD-104); syncLegacyCredentials
// keeps its Object Storage keys and the Object Storage fields equal: a set
// field is written into the map, an empty field is filled from the map.
func TestValidate_CredentialsMapAndObjectStorageFieldsAgree(t *testing.T) {
	r := validBase()
	r.Region = "fsn1"
	r.ControlPlaneSize = "cx42"
	r.ObjectStorageSecretKey = ""
	r.Credentials = map[string]string{"hcloud_token": "from-the-map", "object_storage_secret_key": "secret-from-the-map"}
	if err := r.Validate(); err != nil {
		t.Fatalf("a token in the credentials map should satisfy the profile: %v", err)
	}
	if r.Credentials["hcloud_token"] != "from-the-map" {
		t.Errorf("credentials[hcloud_token] = %q, want the value of the map", r.Credentials["hcloud_token"])
	}
	if r.Credentials["object_storage_region"] != "fsn1" {
		t.Errorf("credentials[object_storage_region] = %q, want the request's Object Storage region", r.Credentials["object_storage_region"])
	}
	if r.ObjectStorageSecretKey != "secret-from-the-map" {
		t.Errorf("ObjectStorageSecretKey = %q, want it filled from the map", r.ObjectStorageSecretKey)
	}
}

// TestRequest_CredentialAccessors — Credential reads a key, and reads the
// redaction marker of a stored record as empty; WithCredential changes a copy
// of the map, never the request it is called on.
func TestRequest_CredentialAccessors(t *testing.T) {
	r := Request{Credentials: map[string]string{"access_key": "ak", "secret_key": RedactedMarker}}
	if r.Credential("access_key") != "ak" || r.Credential("secret_key") != "" || r.Credential("absent") != "" {
		t.Errorf("Credential: access_key %q, secret_key %q, absent %q", r.Credential("access_key"), r.Credential("secret_key"), r.Credential("absent"))
	}
	c := r.WithCredential("access_key", "other")
	if r.Credentials["access_key"] != "ak" || c.Credentials["access_key"] != "other" {
		t.Errorf("WithCredential changed the original: original %q, copy %q", r.Credentials["access_key"], c.Credentials["access_key"])
	}
	var empty Request
	empty.SetCredential("region", "r-1")
	if empty.Credentials["region"] != "r-1" {
		t.Errorf("SetCredential on a nil map: %v", empty.Credentials)
	}
}

// TestRequest_UnmarshalJSON_LegacyAliases — for a provider whose adapter
// names legacy JSON fields, decoding copies each into the credentials map; a
// key the map already holds keeps its value, and a provider without aliases
// decodes as before.
func TestRequest_UnmarshalJSON_LegacyAliases(t *testing.T) {
	var r Request
	if err := json.Unmarshal([]byte(`{"provider":"aliasing","legacyToken":"t-1","legacyProject":"p-1","credentials":{"project":"kept"}}`), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Credentials["token"] != "t-1" || r.Credentials["project"] != "kept" || len(r.Credentials) != 2 {
		t.Errorf("credentials = %v, want token from the alias and project kept", r.Credentials)
	}
	var plain Request
	if err := json.Unmarshal([]byte(`{"provider":"hetzner","legacyToken":"t-1","orgName":"O"}`), &plain); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if plain.Credentials != nil || plain.OrgName != "O" {
		t.Errorf("a provider without aliases: credentials %v, orgName %q", plain.Credentials, plain.OrgName)
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
				r.SetCredential("access_key", "")
			},
			wantField: "credentials.access_key",
		},
		{
			name: "missing-secret-key",
			patch: func(r *Request) {
				r.SetCredential("secret_key", "")
			},
			wantField: "credentials.secret_key",
		},
		{
			name: "missing-project-id",
			patch: func(r *Request) {
				r.SetCredential("project_id", "")
			},
			wantField: "credentials.project_id",
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

// TestRequest_HuaweiCreds_NeverFromTheWire — after PR #2144 (Wave 4.5) the
// Huawei IAM credentials are server-side-only: stamped at CreateDeployment
// from the operator's credential files (CLD-106), NEVER accepted from the
// wizard POST body. Since CLD-104 the request carries them in its credentials map,
// so the contract is pinned on the way in: the old JSON names decode into
// nothing (the Huawei adapter names no legacy alias), and a body whose
// credentials map names one of the server-only keys is refused.
//
// History: the original v1 of this test (PR #2143, Wave 4) asserted
// the opposite — that huaweiSecretKey IS in the wire. That was the
// credential-exfiltration antipattern caught + fixed in #2144.
func TestRequest_HuaweiCreds_NeverFromTheWire(t *testing.T) {
	var r Request
	body := `{"provider":"huawei","huaweiAccessKey":"TESTAK_WIRE_PROBE","huaweiSecretKey":"TESTSK_WIRE_PROBE","huaweiProjectID":"TESTPID_WIRE_PROBE","huaweiRegion":"TESTREGION_WIRE_PROBE"}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(r.Credentials) != 0 {
		t.Errorf("the old Huawei JSON names reached the credentials map: %v", r.Credentials)
	}
	for _, key := range []string{"access_key", "secret_key", "project_id", "region"} {
		named := Request{Provider: "huawei", Credentials: map[string]string{key: "from-the-body"}}
		fe := RefuseServerOnlyCredentials(&named)
		if fe == nil || fe.Field != "credentials."+key {
			t.Errorf("a body naming credentials.%s: got %v, want a field error on it", key, fe)
		}
	}
}

// TestRequest_HuaweiFields_OmitEmpty — defense-in-depth: even with the
// new json:"-" tags, double-check empty Huawei fields don't appear.
// (Redundant with the above test once json:"-" is in place; kept as a
// regression guard if anyone re-introduces json:"huaweiAccessKey,
// omitempty" by accident.)
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

// writeCSILayout writes into dir the layout of the Secrets Store CSI driver:
// the files in a data directory "..<timestamp>", the link "..data" to it, and
// per file one link of its name into "..data".
func writeCSILayout(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	data := filepath.Join(dir, "..2026_11_25_10_00_00.000000001")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(data, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Base(data), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
}

// TestReadOperatorCredentials_CSILayout_CLD106 — the files the CSI driver
// mounts, links into its data directory, are read by provider and key; a
// value is trimmed, and a blank one or a file of another name sets nothing.
func TestReadOperatorCredentials_CSILayout_CLD106(t *testing.T) {
	dir := t.TempDir()
	writeCSILayout(t, dir, map[string]string{
		"cloud-credential.huawei.access_key": "AK\n",
		"cloud-credential.huawei.secret_key": " SK ",
		"cloud-credential.huawei.region":     " \n",
		"cloud-credential.hetzner.token":     "TOKEN",
		"hf-token":                           "not a cloud credential",
	})
	got, err := ReadOperatorCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{"huawei": {"access_key": "AK", "secret_key": "SK"}, "hetzner": {"token": "TOKEN"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestReadOperatorCredentials_MissingDirectory_CLD106 — a Sovereign mounts no
// operator credential: a directory that does not exist holds none, no error.
func TestReadOperatorCredentials_MissingDirectory_CLD106(t *testing.T) {
	got, err := ReadOperatorCredentials(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want no credential and no error", got, err)
	}
}

// TestReadOperatorCredentials_MisnamedFile_CLD106 — a file with the prefix
// but without a key is an error that names it; the other files are still
// read.
func TestReadOperatorCredentials_MisnamedFile_CLD106(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{"cloud-credential.huawei": "x", "cloud-credential.huawei.access_key": "AK"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadOperatorCredentials(dir)
	if err == nil || !strings.Contains(err.Error(), "file cloud-credential.huawei is not named cloud-credential.<provider>.<key>") {
		t.Fatalf("err = %v; want the misnamed file named", err)
	}
	if want := (map[string]map[string]string{"huawei": {"access_key": "AK"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestNew_OperatorCredentialsFromFilesNotTheEnvironment_CLD106 — New() takes
// the operator's credentials from the files of CloudCredentialsDir; the
// variables CATALYST_HUAWEI_* are no source.
func TestNew_OperatorCredentialsFromFilesNotTheEnvironment_CLD106(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cloud-credential.huawei.access_key"), []byte("AK-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "AK-ENV")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "SK-ENV")
	was := CloudCredentialsDir()
	SetCloudCredentialsDir(dir)
	t.Cleanup(func() { SetCloudCredentialsDir(was) })
	if got, want := New().OperatorCredentials, (map[string]map[string]string{"huawei": {"access_key": "AK-FILE"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("OperatorCredentials = %v, want %v", got, want)
	}
}

// TestRegisterFlags_CloudCredentialsDir_CLD106 — flag --cloud-credentials-dir
// sets CloudCredentialsDir, and its default is /var/run/secrets/openova.
func TestRegisterFlags_CloudCredentialsDir_CLD106(t *testing.T) {
	was := CloudCredentialsDir()
	t.Cleanup(func() { SetCloudCredentialsDir(was) })
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "/var/run/secrets/openova"},
		{[]string{"--cloud-credentials-dir=/mnt/operator"}, "/mnt/operator"},
	} {
		SetCloudCredentialsDir("")
		set := flag.NewFlagSet("catalyst-api", flag.ContinueOnError)
		RegisterFlags(set)
		if err := set.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if got := CloudCredentialsDir(); got != c.want {
			t.Errorf("args %v: CloudCredentialsDir() = %q, want %q", c.args, got, c.want)
		}
		if got := set.Lookup("cloud-credentials-dir").DefValue; got != DefaultCloudCredentialsDir {
			t.Errorf("args %v: default = %q, want %q", c.args, got, DefaultCloudCredentialsDir)
		}
	}
}

// TestCloudCredentialsDir_SetWhileNewReads_CLD106 — a provisioning goroutine
// runs New, which reads the directory, while the flag or a test sets it; under
// go test -race the two must not race (the handler tests' cleanup did).
func TestCloudCredentialsDir_SetWhileNewReads_CLD106(t *testing.T) {
	was := CloudCredentialsDir()
	t.Cleanup(func() { SetCloudCredentialsDir(was) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = New()
	}()
	SetCloudCredentialsDir(t.TempDir())
	<-done
}
