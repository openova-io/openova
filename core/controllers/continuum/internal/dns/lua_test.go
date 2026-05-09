// Tests for the lua-record body synthesizer. Exercises golden-file
// stability + the switchover flip (probe-<old> → probe-<new>, IP-set
// re-ordering with primary first).

package dns

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var update = flag.Bool("update", false, "update golden files")

// goldenPath returns the absolute path to a golden file in
// internal/dns/testdata/.
func goldenPath(name string) string {
	return filepath.Join("testdata", name)
}

func newApp(hostnames []string) *unstructured.Unstructured {
	app := &unstructured.Unstructured{}
	app.Object = map[string]interface{}{
		"apiVersion": "apps.openova.io/v1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "demo-app", "namespace": "demo"},
		"spec":       map[string]interface{}{},
	}
	if len(hostnames) > 0 {
		hns := make([]interface{}, 0, len(hostnames))
		for _, h := range hostnames {
			hns = append(hns, h)
		}
		app.Object["spec"].(map[string]interface{})["routes"] = []interface{}{
			map[string]interface{}{"hostnames": hns},
		}
	}
	return app
}

// commonParams returns the shared SynthParams used by both golden
// tests — only the PrimaryRegion differs between fsn-primary and
// hel-promoted.
func commonParams(primary string) SynthParams {
	return SynthParams{
		PrimaryRegion: primary,
		RegionToIPs: map[string][]string{
			"hz-fsn-rtz-prod": {"5.1.2.3", "5.1.2.4"},
			"hz-hel-rtz-prod": {"5.5.6.7"},
		},
		Selector:                   "ifurlup",
		HealthCheckURL:             "https://probe-fsn.example.com/healthz",
		HealthCheckIntervalSeconds: 5,
		HealthCheckTimeoutSeconds:  2,
		Hostnames:                  []string{"web.example.com", "api.example.com"},
		TTL:                        30,
	}
}

func TestSynthesize_Golden_FsnPrimary(t *testing.T) {
	t.Parallel()
	app := newApp(nil)
	records, err := Synthesize(app, commonParams("hz-fsn-rtz-prod"))
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	got, err := MarshalRecords(records)
	if err != nil {
		t.Fatalf("MarshalRecords: %v", err)
	}
	checkGolden(t, "active-hotstandby-fsn-primary.golden", got)
}

func TestSynthesize_Golden_HelPromoted(t *testing.T) {
	t.Parallel()
	app := newApp(nil)
	records, err := Synthesize(app, commonParams("hz-hel-rtz-prod"))
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	got, err := MarshalRecords(records)
	if err != nil {
		t.Fatalf("MarshalRecords: %v", err)
	}
	checkGolden(t, "active-hotstandby-hel-promoted.golden", got)
}

func TestSynthesize_FailsOnMissingPrimary(t *testing.T) {
	t.Parallel()
	app := newApp([]string{"a.com"})
	_, err := Synthesize(app, SynthParams{
		RegionToIPs: map[string][]string{"r1": {"1.1.1.1"}},
		HealthCheckURL: "https://example.com/healthz",
	})
	if err == nil {
		t.Fatal("expected error on missing PrimaryRegion")
	}
}

func TestSynthesize_FailsOnPrimaryNotInMap(t *testing.T) {
	t.Parallel()
	app := newApp([]string{"a.com"})
	_, err := Synthesize(app, SynthParams{
		PrimaryRegion:  "missing",
		RegionToIPs:    map[string][]string{"r1": {"1.1.1.1"}},
		HealthCheckURL: "https://example.com/healthz",
	})
	if err == nil {
		t.Fatal("expected error when PrimaryRegion not in RegionToIPs")
	}
}

func TestSynthesize_FailsOnEmptyRegionToIPs(t *testing.T) {
	t.Parallel()
	app := newApp([]string{"a.com"})
	_, err := Synthesize(app, SynthParams{
		PrimaryRegion:  "r1",
		HealthCheckURL: "https://example.com/healthz",
	})
	if err == nil {
		t.Fatal("expected error on empty RegionToIPs")
	}
}

func TestSynthesize_FallbackHostnamesFromApplication(t *testing.T) {
	t.Parallel()
	app := newApp([]string{"only-from-app.example.com"})
	params := SynthParams{
		PrimaryRegion:  "r1",
		RegionToIPs:    map[string][]string{"r1": {"1.1.1.1"}},
		HealthCheckURL: "https://example.com/healthz",
	}
	records, err := Synthesize(app, params)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(records) != 1 || records[0].Hostname != "only-from-app.example.com" {
		t.Fatalf("expected one record from app spec, got %+v", records)
	}
}

func TestSynthesize_NoHostnamesAtAll(t *testing.T) {
	t.Parallel()
	app := newApp(nil)
	params := SynthParams{
		PrimaryRegion:  "r1",
		RegionToIPs:    map[string][]string{"r1": {"1.1.1.1"}},
		HealthCheckURL: "https://example.com/healthz",
	}
	if _, err := Synthesize(app, params); err == nil {
		t.Fatal("expected error when no hostnames")
	}
}

func TestSynthesize_StableAcrossRuns(t *testing.T) {
	t.Parallel()
	app := newApp(nil)
	r1, err := Synthesize(app, commonParams("hz-fsn-rtz-prod"))
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	r2, err := Synthesize(app, commonParams("hz-fsn-rtz-prod"))
	if err != nil {
		t.Fatalf("Synthesize 2: %v", err)
	}
	b1, _ := MarshalRecords(r1)
	b2, _ := MarshalRecords(r2)
	if string(b1) != string(b2) {
		t.Fatalf("expected byte-stable output\n  first:  %s\n  second: %s", b1, b2)
	}
}

func TestSynthesize_PrimaryFlipChangesProbeURL(t *testing.T) {
	t.Parallel()
	app := newApp([]string{"a.com"})
	r1, err := Synthesize(app, commonParams("hz-fsn-rtz-prod"))
	if err != nil {
		t.Fatalf("Synthesize fsn: %v", err)
	}
	r2, err := Synthesize(app, commonParams("hz-hel-rtz-prod"))
	if err != nil {
		t.Fatalf("Synthesize hel: %v", err)
	}
	if !strings.Contains(r1[0].LuaBody, "probe-fsn.example.com") {
		t.Fatalf("fsn-primary: probe URL not flipped to fsn:\n%s", r1[0].LuaBody)
	}
	if !strings.Contains(r2[0].LuaBody, "probe-hel.example.com") {
		t.Fatalf("hel-promoted: probe URL not flipped to hel:\n%s", r2[0].LuaBody)
	}
}

func TestSynthesize_StandbyOmittedWhenSingleRegion(t *testing.T) {
	t.Parallel()
	app := newApp([]string{"a.com"})
	params := SynthParams{
		PrimaryRegion:  "r1",
		RegionToIPs:    map[string][]string{"r1": {"1.1.1.1", "1.1.1.2"}},
		HealthCheckURL: "https://example.com/healthz",
	}
	r, err := Synthesize(app, params)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	body := r[0].LuaBody
	// Should have ONE group of IPs, not two.
	groups := strings.Count(body, "{\"")
	if groups != 1 {
		t.Fatalf("expected 1 IP group in body, got %d (body=%s)", groups, body)
	}
}

func TestRewriteProbeURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, region, want string
	}{
		{"https://probe-fsn.example.com/healthz", "hel", "https://probe-hel.example.com/healthz"},
		{"https://api.example.com/healthz", "fsn", "https://probe-fsn.api.example.com/healthz"},
		{"https://probe-old.x.y.z/path?q=1", "new", "https://probe-new.x.y.z/path?q=1"},
		{"https://probe-fsn.example.com:8443/healthz", "hel", "https://probe-hel.example.com:8443/healthz"},
	}
	for _, c := range cases {
		got, err := rewriteProbeURL(c.in, c.region)
		if err != nil {
			t.Errorf("rewriteProbeURL(%q, %q): err=%v", c.in, c.region, err)
			continue
		}
		if got != c.want {
			t.Errorf("rewriteProbeURL(%q, %q):\n  got:  %s\n  want: %s", c.in, c.region, got, c.want)
		}
	}
}

func TestRewriteProbeURL_FailsOnEmpty(t *testing.T) {
	t.Parallel()
	if _, err := rewriteProbeURL("", "fsn"); err == nil {
		t.Fatal("expected error on empty url")
	}
	if _, err := rewriteProbeURL("https://x.com/", ""); err == nil {
		t.Fatal("expected error on empty region")
	}
}

// checkGolden compares `got` against the bytes in testdata/<name>.
// Pass `-update` to rewrite the golden file on a known-good run.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := goldenPath(name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden mismatch for %s\n  got:  %s\n  want: %s\n  (re-run with -update to refresh)", name, got, want)
	}
}
