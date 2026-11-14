package runner

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

const rulePrefix = "xccdf_org.ssgproject.content_rule_"

// arf is an ARF file with one XCCDF test result. The rule-result of another namespace
// is not an XCCDF result and is not read.
var arf = `<?xml version="1.0" encoding="UTF-8"?>
<arf:asset-report-collection xmlns:arf="http://scap.nist.gov/schema/asset-reporting-format/1.1">
  <arf:reports><arf:report id="xccdf1"><arf:content>
    <TestResult xmlns="http://checklists.nist.gov/xccdf/1.2" id="xccdf_org.open-scap_testresult_p">
` + ruleResult("a", "pass") + ruleResult("b", "pass") + ruleResult("c", "fixed") + ruleResult("d", "fail") +
	ruleResult("e", "notapplicable") + ruleResult("f", "error") + ruleResult("g", "unknown") +
	ruleResult("h", "notchecked") + ruleResult("i", "informational") +
	ruleResult("j", "notselected") + ruleResult("k", "notselected") + `
    </TestResult>
    <x:rule-result xmlns:x="urn:example:other" idref="z"><x:result>fail</x:result></x:rule-result>
  </arf:content></arf:report></arf:reports>
</arf:asset-report-collection>
`

func ruleResult(id, result string) string {
	return `      <rule-result idref="` + rulePrefix + id + `" severity="medium">
        <result>` + result + `</result>
      </rule-result>
`
}

// writeARF is a fake oscap-chroot that writes the ARF fixture to the path after
// --results-arf and exits 2, as oscap does when a rule failed.
func writeARF(t *testing.T, env Env) {
	fixture := filepath.Join(filepath.Dir(env.OscapChroot), "fixture.arf.xml")
	must(t, os.WriteFile(fixture, []byte(arf), 0o600))
	fakeProgram(t, env.OscapChroot, `out=""; prev=""
for a in "$@"; do [ "$prev" = "--results-arf" ] && out="$a"; prev="$a"; done
cp "`+fixture+`" "$out"; exit 2`)
}

func TestDataStreamNames(t *testing.T) {
	cases := []struct{ family, version, want string }{
		{"ubuntu", "22.04", "ssg-ubuntu2204-ds.xml"},
		{"ubuntu", "24.04", "ssg-ubuntu2404-ds.xml"},
		{"rhel", "9.4", "ssg-rhel9-ds.xml"},
		{"rhel", "10", "ssg-rhel10-ds.xml"},
		{"almalinux", "9.5", "ssg-almalinux9-ds.xml"},
		{"oraclelinux", "8.10", "ssg-ol8-ds.xml"},
		{"ubuntu", "24", ""},
		{"rocky", "9.4", ""},
		{"debian", "12", ""},
		{"windows", "2022", ""},
	}
	for _, c := range cases {
		got, ok := DataStream(c.family, c.version)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("DataStream(%q, %q) = %q, %v; want %q", c.family, c.version, got, ok, c.want)
		}
	}
}

func TestHardeningWithoutADataStreamHasNoProfileForRelease(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	cfg := config(TaskHardening, s3)
	cfg.OSVersion = "26.10" // the content holds no ssg-ubuntu2610-ds.xml
	doc := Run(context.Background(), cfg, env)
	want := &runresult.Hardening{ProfileID: cfg.ProfileID, ContentRelease: "0.1.82",
		ResultsKey: "acme/ledger-db-1/scan-1/hardening.results.json"}
	if doc.Outcome != runresult.Succeeded || doc.Message != MessageNoProfile || !doc.Completed || !reflect.DeepEqual(doc.Hardening, want) {
		t.Fatalf("got %+v %+v, want Succeeded, no-profile-for-release, zero rules", doc, doc.Hardening)
	}
	if args(t, env.OscapChroot) != nil {
		t.Fatal("oscap-chroot ran")
	}
	var results HardeningResults
	must(t, json.Unmarshal(s3.objects["/machine-evidence/acme/ledger-db-1/scan-1/hardening.results.json"], &results))
	if results.Reason != MessageNoProfile || len(results.Rules) != 0 || results.DataStream != "" || results.Schema != HardeningResultsSchema {
		t.Fatalf("stored results %+v", results)
	}
	if len(doc.Evidence) != 1 || doc.Evidence[0].Kind != KindHardeningResults {
		t.Fatalf("evidence %+v", doc.Evidence)
	}
}

func TestHardeningCountsTheRuleResults(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	writeARF(t, env)
	cfg := config(TaskHardening, s3)
	doc := Run(context.Background(), cfg, env)
	if doc.Outcome != runresult.Succeeded || !doc.Completed || doc.Message != "" {
		t.Fatalf("got %+v", doc)
	}
	ds := filepath.Join(env.ContentDir, "0.1.82", "ssg-ubuntu2404-ds.xml")
	wantArgs := []string{env.HostRoot, "xccdf", "eval", "--results-arf", filepath.Join(env.WorkDir, FileHardeningARF),
		"--profile", cfg.ProfileID, ds}
	if got := args(t, env.OscapChroot); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("oscap-chroot arguments %q, want %q", got, wantArgs)
	}
	want := &runresult.Hardening{ProfileID: cfg.ProfileID, ContentRelease: "0.1.82", Pass: 3, Fail: 1, NotApplicable: 1, Error: 2,
		ResultsKey: "acme/ledger-db-1/scan-1/hardening.results.json"}
	if !reflect.DeepEqual(doc.Hardening, want) {
		t.Fatalf("hardening %+v, want %+v", doc.Hardening, want)
	}
	var kinds []string
	for _, e := range doc.Evidence {
		kinds = append(kinds, e.Kind+" "+e.Key)
	}
	wantKinds := []string{"hardening-arf acme/ledger-db-1/scan-1/hardening.arf.xml", "hardening-results acme/ledger-db-1/scan-1/hardening.results.json"}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("evidence %q, want %q", kinds, wantKinds)
	}
	var results HardeningResults
	must(t, json.Unmarshal(s3.objects["/machine-evidence/acme/ledger-db-1/scan-1/hardening.results.json"], &results))
	if len(results.Rules) != 9 || results.Rules[0] != (RuleResult{ID: rulePrefix + "a", Result: "pass"}) ||
		results.DataStream != "ssg-ubuntu2404-ds.xml" || results.Counts != (Counts{Pass: 3, Fail: 1, NotApplicable: 1, Error: 2}) {
		t.Fatalf("stored results %+v", results)
	}
	if string(s3.objects["/machine-evidence/acme/ledger-db-1/scan-1/hardening.arf.xml"]) != arf {
		t.Fatal("the stored ARF file is not the one oscap wrote")
	}
}

func TestHardeningTailorsTheDisabledRules(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	writeARF(t, env)
	cfg := config(TaskHardening, s3)
	cfg.DisabledRules = []string{rulePrefix + "a", rulePrefix + "b"}
	doc := Run(context.Background(), cfg, env)
	if doc.Outcome != runresult.Succeeded {
		t.Fatalf("got %+v", doc)
	}
	tailoring := filepath.Join(env.WorkDir, "hardening.tailoring.xml")
	got := args(t, env.OscapChroot)
	want := []string{"--tailoring-file", tailoring, "--profile", TailoredProfileID(cfg.ProfileID)}
	if len(got) != 10 || !reflect.DeepEqual(got[5:9], want) {
		t.Fatalf("oscap-chroot arguments %q, want %q in the middle", got, want)
	}
	data, err := os.ReadFile(tailoring) //nolint:gosec // a file of the test folder
	must(t, err)
	var doc12 struct {
		XMLName   xml.Name `xml:"http://checklists.nist.gov/xccdf/1.2 Tailoring"`
		Benchmark struct {
			Href string `xml:"href,attr"`
		} `xml:"benchmark"`
		Profile struct {
			ID      string `xml:"id,attr"`
			Extends string `xml:"extends,attr"`
			Selects []struct {
				IDRef    string `xml:"idref,attr"`
				Selected string `xml:"selected,attr"`
			} `xml:"select"`
		} `xml:"Profile"`
	}
	must(t, xml.Unmarshal(data, &doc12))
	if doc12.Profile.ID != cfg.ProfileID+"_openova_tailored" || doc12.Profile.Extends != cfg.ProfileID ||
		len(doc12.Profile.Selects) != 2 || doc12.Profile.Selects[1].IDRef != rulePrefix+"b" ||
		doc12.Profile.Selects[1].Selected != "false" || !strings.HasSuffix(doc12.Benchmark.Href, "ssg-ubuntu2404-ds.xml") {
		t.Fatalf("tailoring %+v", doc12)
	}
}

func TestTailoringIsFixedText(t *testing.T) {
	got := string(Tailoring("/content/scap/0.1.82/ssg-ubuntu2404-ds.xml", "xccdf_org.ssgproject.content_profile_p",
		[]string{rulePrefix + "a"}, time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)))
	want := `<?xml version="1.0" encoding="UTF-8"?>
<xccdf:Tailoring xmlns:xccdf="http://checklists.nist.gov/xccdf/1.2" id="xccdf_io.openova_tailoring_machine-runner">
  <xccdf:benchmark href="/content/scap/0.1.82/ssg-ubuntu2404-ds.xml"/>
  <xccdf:version time="2026-11-14T09:30:00">1</xccdf:version>
  <xccdf:Profile id="xccdf_org.ssgproject.content_profile_p_openova_tailored" extends="xccdf_org.ssgproject.content_profile_p">
    <xccdf:title>xccdf_org.ssgproject.content_profile_p tailored by machine-runner</xccdf:title>
    <xccdf:select idref="xccdf_org.ssgproject.content_rule_a" selected="false"/>
  </xccdf:Profile>
</xccdf:Tailoring>
`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestHardeningWithAContentReleaseNotInTheImageIsRefused(t *testing.T) {
	env := testEnv(t, nil)
	cfg := config(TaskHardening, nil)
	cfg.ContentRelease = "0.1.79"
	doc := Run(context.Background(), cfg, env)
	if doc.Outcome != runresult.Refused || doc.Message != "content-release-not-in-image: 0.1.79" || doc.Completed || doc.Hardening != nil {
		t.Fatalf("got %+v", doc)
	}
}

func TestHardeningScannerEndsWithoutAResult(t *testing.T) {
	cases := map[string]struct {
		body    string
		outcome runresult.Outcome
		message string
	}{
		"an error exit":   {`echo "OpenSCAP Error: no such profile" >&2; exit 1`, runresult.Failed, "oscap-chroot exited 1: OpenSCAP Error: no such profile"},
		"a killed scan":   {`kill -KILL $$`, runresult.Incomplete, "oscap-chroot was stopped: signal: killed"},
		"no rule results": {`out=""; prev=""; for a in "$@"; do [ "$prev" = "--results-arf" ] && out="$a"; prev="$a"; done; printf '<arf/>' > "$out"; exit 0`, runresult.Failed, "hardening.arf.xml holds no rule result"},
		"no file":         {`exit 0`, runresult.Failed, "hardening.arf.xml cannot be read: "},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s3 := newFakeS3(t)
			env := testEnv(t, s3)
			fakeProgram(t, env.OscapChroot, c.body)
			doc := Run(context.Background(), config(TaskHardening, s3), env)
			if doc.Outcome != c.outcome || doc.Completed || !strings.HasPrefix(doc.Message, c.message) || doc.Hardening != nil {
				t.Fatalf("got %+v, want %s %q", doc, c.outcome, c.message)
			}
			if s3.requests() != 0 {
				t.Fatalf("%d requests reached the evidence store, want 0", s3.requests())
			}
		})
	}
}
