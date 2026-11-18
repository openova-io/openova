package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// trivyReport is what Trivy 0.76.1 wrote for a bill of materials with lodash 4.17.15,
// cut to one of its advisories: the fixture database of these tests.
const trivyReport = `{"SchemaVersion": 2, "Trivy": {"Version": "0.76.1"}, "ArtifactName": "sbom.cdx.json", "ArtifactType": "cyclonedx", "Results": [{"Target": "Node.js", "Class": "lang-pkgs", "Type": "node-pkg", "Vulnerabilities": [{"VulnerabilityID": "CVE-2020-8203", "PkgID": "lodash@4.17.15", "PkgName": "lodash", "InstalledVersion": "4.17.15", "FixedVersion": "4.17.19", "Status": "fixed", "Severity": "HIGH", "PublishedDate": "2020-07-15T17:15:11.797Z", "LastModifiedDate": "2026-11-02T18:18:29.9Z"}]}]}`

// fakeTrivy answers as Trivy does: image (the database download) by the given body,
// version with the update time of the database, and sbom with trivyReport for a bill of
// materials that names lodash and an empty report for any other. Each call appends its
// arguments, one line, to <path>.calls.
func fakeTrivy(t *testing.T, path, download string) {
	fakeProgram(t, path, `echo "$*" >> "$0.calls"
case "$1" in
image) `+download+` ;;
version) printf '{"Version":"0.76.1","VulnerabilityDB":{"Version":2,"UpdatedAt":"2026-11-30T06:00:16.23Z"}}\n' ;;
sbom) out=""; prev=""; for a in "$@"; do [ "$prev" = "--output" ] && out="$a"; prev="$a"; done
  if grep -q lodash "$a"; then printf '%s\n' '`+trivyReport+`' > "$out"; else printf '{"SchemaVersion":2,"Results":[]}\n' > "$out"; fi ;;
*) exit 64 ;;
esac`)
}

func calls(t *testing.T, path string) []string {
	data, err := os.ReadFile(path + ".calls") //nolint:gosec // a file of the test folder
	must(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// matchConfig is a valid run of the task match over the given bills of materials.
func matchConfig(s3 *fakeS3, keys ...string) Config {
	return Config{Task: TaskMatch, Run: Ref{Namespace: "catalyst-infra", Name: "scan-1"}, SBOMKeys: keys,
		Evidence: Evidence{Endpoint: s3.srv.URL, Region: "us-east-1", Bucket: "machine-evidence", Prefix: "_match/scan-1/"}}
}

func stored(t *testing.T, s3 *fakeS3, key string, into interface{}) string {
	t.Helper()
	data, ok := s3.objects["/machine-evidence/"+key]
	if !ok {
		t.Fatalf("%s was not stored; keys %v", key, s3.keys())
	}
	must(t, json.Unmarshal(data, into))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// One database download, one Trivy run per bill of materials, offline, and one findings
// file per machine; the result document names only the index, which names each findings
// file with its checksum.
func TestMatchStoresOneFindingsFilePerMachine(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	fakeTrivy(t, env.Trivy, "exit 0")
	s3.objects["/machine-evidence/catalyst-infra/cp-1/scan-1/sbom.cdx.json"] = []byte(`{"bomFormat":"CycloneDX","components":[{"purl":"pkg:npm/lodash@4.17.15"}]}`)
	s3.objects["/machine-evidence/catalyst-infra/cp-2/scan-1/sbom.cdx.json"] = []byte(`{"bomFormat":"CycloneDX","components":[]}`)
	cfg := matchConfig(s3, "catalyst-infra/cp-1/scan-1/sbom.cdx.json", "catalyst-infra/cp-2/scan-1/sbom.cdx.json")
	cfg.DBRepository = "registry.example/trivy-db:2"
	doc := Run(context.Background(), cfg, env)
	if doc.Outcome != runresult.Succeeded || !doc.Completed || len(doc.Evidence) != 1 || doc.Evidence[0].Kind != KindMatchIndex || doc.Evidence[0].Key != "_match/scan-1/index.json" {
		t.Fatalf("got %+v, want Succeeded, completed and the index as the one evidence", doc)
	}
	cache := env.WorkDir + "/trivy-cache"
	got := calls(t, env.Trivy)
	want := []string{
		"image --download-db-only --cache-dir " + cache + " --db-repository registry.example/trivy-db:2",
		"version --format json --cache-dir " + cache,
		"sbom --format json --skip-db-update --skip-java-db-update --offline-scan --cache-dir " + cache + " --output " + env.WorkDir + "/trivy-0.json " + env.WorkDir + "/sbom-0.cdx.json",
		"sbom --format json --skip-db-update --skip-java-db-update --offline-scan --cache-dir " + cache + " --output " + env.WorkDir + "/trivy-1.json " + env.WorkDir + "/sbom-1.cdx.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trivy calls\n%q\nwant\n%q", got, want)
	}
	var index MatchIndex
	if sum := stored(t, s3, doc.Evidence[0].Key, &index); sum != doc.Evidence[0].SHA256 {
		t.Fatalf("the index checksum %s is not the one of the document %s", sum, doc.Evidence[0].SHA256)
	}
	if index.Schema != MatchIndexSchema || index.DatabaseVersion != "2026-11-30T06:00:16Z" || len(index.Machines) != 2 {
		t.Fatalf("index %+v", index)
	}
	for i, machine := range []string{"catalyst-infra/cp-1", "catalyst-infra/cp-2"} {
		e := index.Machines[i]
		var f Findings
		sum := stored(t, s3, "_match/scan-1/"+machine+".json", &f)
		if e.Machine != machine || e.Error != "" || e.FindingsKey != "_match/scan-1/"+machine+".json" || e.SHA256 != sum || e.SBOMKey != cfg.SBOMKeys[i] {
			t.Errorf("entry %d is %+v", i, e)
		}
		if f.Schema != FindingsSchema || f.Machine != machine || f.DatabaseVersion != index.DatabaseVersion || f.SBOMKey != cfg.SBOMKeys[i] {
			t.Errorf("findings of %s: %+v", machine, f)
		}
		if i == 1 && len(f.Vulnerabilities) != 0 {
			t.Errorf("a bill of materials with no vulnerable package gave %+v", f.Vulnerabilities)
		}
		if i == 0 && !reflect.DeepEqual(f.Vulnerabilities, []Finding{{ID: "CVE-2020-8203", Package: "lodash", InstalledVersion: "4.17.15",
			FixedVersion: "4.17.19", Severity: "high", PublishedAt: "2020-07-15T17:15:11.797Z"}}) {
			t.Errorf("findings of the vulnerable package: %+v", f.Vulnerabilities)
		}
	}
}

// Without a database nothing is matched and nothing is stored.
func TestMatchWithoutTheDatabaseIsFailed(t *testing.T) {
	for name, download := range map[string]string{
		"the download fails":           `echo "failed to download vulnerability DB" >&2; exit 1`,
		"the database has no metadata": `exit 0`,
	} {
		s3 := newFakeS3(t)
		env := testEnv(t, s3)
		fakeTrivy(t, env.Trivy, download)
		if name == "the database has no metadata" {
			fakeProgram(t, env.Trivy, `[ "$1" = version ] && printf '{"Version":"0.76.1"}\n'; exit 0`)
		}
		doc := Run(context.Background(), matchConfig(s3, "catalyst-infra/cp-1/scan-1/sbom.cdx.json"), env)
		if doc.Outcome != runresult.Failed || doc.Completed || !strings.HasPrefix(doc.Message, MessageDatabaseUnavailable+": ") || len(doc.Evidence) != 0 {
			t.Errorf("%s: got %+v, want Failed with %s", name, doc, MessageDatabaseUnavailable)
		}
		if s3.requests() != 0 {
			t.Errorf("%s: %d requests reached the evidence store, want 0", name, s3.requests())
		}
	}
}

// A bill of materials that cannot be read is an error of its entry; the others are matched.
func TestMatchRecordsAMissingBillOfMaterials(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	fakeTrivy(t, env.Trivy, "exit 0")
	s3.objects["/machine-evidence/catalyst-infra/cp-2/scan-1/sbom.cdx.json"] = []byte(`{"bomFormat":"CycloneDX","components":[]}`)
	doc := Run(context.Background(), matchConfig(s3, "catalyst-infra/cp-1/scan-1/sbom.cdx.json", "catalyst-infra/cp-2/scan-1/sbom.cdx.json"), env)
	if doc.Outcome != runresult.Succeeded || !doc.Completed {
		t.Fatalf("got %+v", doc)
	}
	var index MatchIndex
	stored(t, s3, "_match/scan-1/index.json", &index)
	if e := index.Machines[0]; !strings.HasPrefix(e.Error, "sbom-unavailable: ") || e.FindingsKey != "" {
		t.Errorf("the missing bill of materials gave %+v", e)
	}
	if e := index.Machines[1]; e.Error != "" || e.FindingsKey != "_match/scan-1/catalyst-infra/cp-2.json" {
		t.Errorf("the other bill of materials gave %+v", e)
	}
}

func TestParseMatchArgs(t *testing.T) {
	good := []string{"--task=match", "--run=catalyst-infra/scan-1", "--sbom-keys=catalyst-infra/cp-1/scan-1/sbom.cdx.json,acme/vm-1/scan-0/sbom.cdx.json",
		"--evidence-endpoint=https://s3.example", "--evidence-region=region-1", "--evidence-bucket=machine-evidence", "--evidence-prefix=_match/scan-1/",
		"--db-repository=harbor.example:8443/proxy/aquasecurity/trivy-db:2"}
	c, err := ParseArgs(good)
	if err != nil || c.Task != TaskMatch || len(c.SBOMKeys) != 2 || c.DBRepository != "harbor.example:8443/proxy/aquasecurity/trivy-db:2" || c.Evidence.Prefix != "_match/scan-1/" {
		t.Fatalf("ParseArgs(%q) = %+v, %v", good, c, err)
	}
	for name, change := range map[string][2]string{
		"a mode":                       {"", "--mode=node"},
		"a machine":                    {"", "--machine=catalyst-infra/cp-1"},
		"no bill of materials":         {"--sbom-keys=catalyst-infra/cp-1/scan-1/sbom.cdx.json,acme/vm-1/scan-0/sbom.cdx.json", "--sbom-keys="},
		"a key that is not a bill":     {"--sbom-keys=catalyst-infra/cp-1/scan-1/sbom.cdx.json,acme/vm-1/scan-0/sbom.cdx.json", "--sbom-keys=catalyst-infra/cp-1/scan-1/hardening.arf.xml"},
		"the prefix of a machine":      {"--evidence-prefix=_match/scan-1/", "--evidence-prefix=catalyst-infra/cp-1/scan-1/"},
		"a database repository as URL": {"--db-repository=harbor.example:8443/proxy/aquasecurity/trivy-db:2", "--db-repository=https://harbor.example/trivy-db"},
	} {
		args := append([]string{}, good...)
		if change[0] == "" {
			args = append(args, change[1])
		}
		for i := range args {
			if args[i] == change[0] {
				args[i] = change[1]
			}
		}
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("ParseArgs accepted the task match with %s", name)
		}
	}
	inventory := []string{"--task=inventory", "--mode=node", "--machine=acme/ledger-db-1", "--run=machine-ops/scan-1", "--os-family=ubuntu", "--os-version=24.04",
		"--evidence-endpoint=https://s3.example", "--evidence-region=region-1", "--evidence-bucket=machine-evidence", "--evidence-prefix=acme/ledger-db-1/scan-1/"}
	if _, err := ParseArgs(inventory); err != nil {
		t.Fatalf("a valid inventory run was refused: %v", err)
	}
	if _, err := ParseArgs(append(inventory, "--db-repository=registry.example/trivy-db:2")); err == nil {
		t.Error("ParseArgs accepted --db-repository on the task inventory")
	}
}
