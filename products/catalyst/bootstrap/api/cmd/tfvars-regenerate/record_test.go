package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// credentialFields names the fields of provisioner.Request that a deployment
// record must not hold, each with its reason; the command reads them from the
// kept file. Every other exported field of the request must come back from the
// record (TestTheRecordKeepsEveryFieldButTheCredentials).
var credentialFields = map[string]string{
	"Credentials":            "the cloud credentials: the record keeps the keys and redacts every secret value",
	"DynadotAPIKey":          "the registrar's API key",
	"DynadotAPISecret":       "the registrar's API secret",
	"GHCRPullToken":          "a registry pull token",
	"HarborRobotToken":       "a registry robot token",
	"PowerDNSAPIKey":         "the DNS server's API key",
	"PDMBasicAuthUser":       "the user of the pool-domain manager's login",
	"PDMBasicAuthPass":       "the password of that login",
	"KubeconfigBearerToken":  "the bearer the new Sovereign posts its kubeconfig with; the record holds its hash only",
	"HandoverJWTPublicKey":   "key material of the engine's hand-over signer, stamped server-side and rotated with it: the applied key is the kept file's",
	"ObjectStorageAccessKey": "the Object Storage access key",
	"ObjectStorageSecretKey": "the Object Storage secret key",
}

// notSetByRequest names the fields that request() cannot set, each with its
// reason. TestTheRecordKeepsEveryFieldButTheCredentials covers them.
var notSetByRequest = map[string]string{
	"Installation": "the command reads no installation document (provisioner.ReadInstallation is not set in it), so a request that names one cannot be rebuilt",
}

// filled sets v, and every exported field beneath it, to a value that is not
// zero and that names its path, so that no two fields share a value.
func filled(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("probe:" + path + ";")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(len(path)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(len(path)))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		filled(t, v.Elem(), path)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		filled(t, v.Index(0), path+"[0]")
	case reflect.Map:
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		filled(t, key, path+".key")
		filled(t, value, path+".value")
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, value)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Type().Field(i); f.IsExported() {
				filled(t, v.Field(i), path+"."+f.Name)
			}
		}
	default:
		t.Fatalf("%s: filled has no value for a %s; add the kind", path, v.Kind())
	}
}

// values returns the strings that v holds, map keys left out: the record
// keeps the keys of the credentials map and none of its values.
func values(v reflect.Value) []string {
	switch v.Kind() {
	case reflect.String:
		return []string{v.String()}
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return values(v.Elem())
	case reflect.Slice:
		var out []string
		for i := 0; i < v.Len(); i++ {
			out = append(out, values(v.Index(i))...)
		}
		return out
	case reflect.Map:
		var out []string
		for _, key := range v.MapKeys() {
			out = append(out, values(v.MapIndex(key))...)
		}
		return out
	case reflect.Struct:
		var out []string
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				out = append(out, values(v.Field(i))...)
			}
		}
		return out
	}
	return nil
}

// TestTheRecordKeepsEveryFieldButTheCredentials derives the fields from the
// request type: each exported field of provisioner.Request, given a value of
// its own, comes back equal from a deployment record (store.Redact, the
// record's JSON, ToProvisionerRequest and the record's id), unless
// credentialFields names it; then the record's JSON holds none of its values.
// A field added to the request fails here until the record keeps it or
// credentialFields names it with its reason.
func TestTheRecordKeepsEveryFieldButTheCredentials(t *testing.T) {
	var full provisioner.Request
	filled(t, reflect.ValueOf(&full).Elem(), "request")
	raw, err := json.Marshal(store.Record{ID: full.DeploymentID, Request: store.Redact(full)})
	if err != nil {
		t.Fatalf("encode the record: %v", err)
	}
	var rec store.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode the record: %v", err)
	}
	want, back := reflect.ValueOf(full), reflect.ValueOf(fromRecord(rec))
	rt := want.Type()
	for name := range credentialFields {
		if _, ok := rt.FieldByName(name); !ok {
			t.Errorf("credentialFields names %s, which provisioner.Request does not have", name)
		}
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, secret := credentialFields[f.Name]; secret {
			for _, s := range values(want.Field(i)) {
				if bytes.Contains(raw, []byte(s)) {
					t.Errorf("the record holds %q, a value of the credential %s", s, f.Name)
				}
			}
			continue
		}
		if got := back.Field(i).Interface(); !reflect.DeepEqual(got, want.Field(i).Interface()) {
			t.Errorf("the record does not keep %s (got %#v): hold it in store.RedactedRequest, in Redact and in ToProvisionerRequest, or name it in credentialFields with the reason", f.Name, got)
		}
	}
}

// TestRebuildGivesBackTheRequest: request() sets every exported field of the
// request (derived from the type, so that the helper cannot fall behind it),
// and the request that the command rebuilds from the record and the kept file
// is, field by field, the request the engine wrote the kept file for.
func TestRebuildGivesBackTheRequest(t *testing.T) {
	pinEnv(t)
	for _, provider := range []string{"huawei", "hetzner"} {
		t.Run(provider, func(t *testing.T) {
			want := request(provider)
			if err := want.Validate(); err != nil {
				t.Fatalf("the test request is not valid: %v", err)
			}
			recordPath, work := deployment(t, want)
			var rec store.Record
			var file map[string]any
			var k kept
			path := filepath.Join(work, tfvarsName)
			for into, from := range map[any]string{&rec: recordPath, &file: path, &k: path} {
				if err := readJSON(from, into); err != nil {
					t.Fatal(err)
				}
			}
			got, err := rebuild(rec, k, file)
			if err != nil {
				t.Fatalf("rebuild: %v", err)
			}
			wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
			for i := 0; i < wv.NumField(); i++ {
				f := wv.Type().Field(i)
				if _, skip := notSetByRequest[f.Name]; !f.IsExported() || skip {
					continue
				}
				if wv.Field(i).IsZero() {
					t.Errorf("request() leaves %s unset: set it, so that the round trip covers it", f.Name)
					continue
				}
				if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
					t.Errorf("%s does not come back from the record and the kept file: got %#v, want %#v", f.Name, gv.Field(i).Interface(), wv.Field(i).Interface())
				}
			}
		})
	}
}

// TestRoundTrip_RecordOfAnOlderEngine: a record written before the record
// kept the settings that are not credentials holds none of them; the command
// reads them from the kept file and writes the same file again.
func TestRoundTrip_RecordOfAnOlderEngine(t *testing.T) {
	pinEnv(t)
	for _, provider := range []string{"huawei", "hetzner"} {
		t.Run(provider, func(t *testing.T) {
			recordPath, work := deployment(t, request(provider))
			olderRecord(t, recordPath)
			want := readFile(t, filepath.Join(work, tfvarsName))
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-record", recordPath, "-dir", work}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
			}
			if got := readFile(t, filepath.Join(work, tfvarsName)); !bytes.Equal(got, want) {
				t.Errorf("the file written again differs from the kept file; reported: %q", stdout.String())
			}
		})
	}
}

// olderRecord rewrites the record at path the way an engine wrote it before
// the record kept the settings that are not credentials: without them.
func olderRecord(t *testing.T, path string) {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal(readFile(t, path), &rec); err != nil {
		t.Fatalf("decode the record: %v", err)
	}
	req := rec["request"].(map[string]any)
	for _, key := range []string{"sovereignTimeZone", "bcpTopology", "adminCIDRs", "consoleIsolationEnabled", "enableSharedPostgres", "qaTestEnabled",
		"fireCutoverOnHandover", "mailRelay", "qaFixturesNamespace", "qaOrganization", "clusterMeshName", "clusterMeshId", "gitopsBranch"} {
		if _, ok := req[key]; !ok {
			t.Fatalf("the record does not hold %s, so a test of an older record proves nothing for it", key)
		}
		delete(req, key)
	}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write the record: %v", err)
	}
}

// TestTheRecordsTimeZoneWins: the zone is the record's, not the kept file's.
func TestTheRecordsTimeZoneWins(t *testing.T) {
	pinEnv(t)
	req := request("huawei")
	_, work := deployment(t, req)
	req.SovereignTimeZone = "Europe/Berlin"
	recordPath, _ := deployment(t, req)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-record", recordPath, "-dir", work}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	if got := stdout.String(); got != "changed variables\n" {
		t.Errorf("want the one line \"changed variables\", got %q", got)
	}
	var file kept
	if err := readJSON(filepath.Join(work, tfvarsName), &file); err != nil {
		t.Fatal(err)
	}
	if got := file.Variables["SOVEREIGN_TIME_ZONE"]; got != "Europe/Berlin" {
		t.Errorf("SOVEREIGN_TIME_ZONE = %v, want the record's Europe/Berlin", got)
	}
}
