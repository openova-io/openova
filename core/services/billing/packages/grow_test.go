package packages

// grow_test.go — the grow-mode parts of the document (founder model
// 2026-10-10): the package headline (`includes`), the per-package `grow`
// block with its ceiling and overage rates, and the `grow_only` cell (DR
// active-passive on S/M/L, available only in grow mode, billed as usage).

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

const growDoc = `{"currency":"OMR","price_book":"b","prices_as_of":"2026-10-10",
 "packages":[
  {"sku":"plan.s","name":"S","price_month":"2.490","includes":{"vcpu":1,"memory_gb":2,"disk_gb":25,"bandwidth_mbps":50},
   "grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[
     {"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.992"},
     {"key":"memory","sku":"k8s.mem_gb","unit":"GB","price_month":"0.374"}]}},
  {"sku":"plan.m","name":"M","price_month":"4.490","includes":{"vcpu":2,"memory_gb":4,"bandwidth_mbps":100,"note":"text is ignored"},
   "grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16.5,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[
     {"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.796"}]}},
  {"sku":"plan.l","name":"L","price_month":"7.990","grow":{"allowed":false}},
  {"sku":"plan.xl","name":"XL","price_month":"13.990"}],
 "features":[
  {"key":"dr_topology","name":"DR topology","kind":"level","levels":["single region","active-passive"],"cells":{
    "plan.s":{"state":"optional","level":0,"grow_only":true,"included_from":"plan.xl"},
    "plan.m":{"state":"optional","level":0,"grow_only":true,"included_from":"plan.xl"},
    "plan.l":{"state":"included","level":0,"included_from":"plan.xl"},
    "plan.xl":{"state":"included","level":1}}},
  {"key":"vuln","name":"Vulnerability dashboard","kind":"boolean","cells":{
    "plan.s":{"state":"teaser","included_from":"plan.m"},
    "plan.m":{"state":"included"}}}
 ]}`

func TestParse_GrowBlockIncludesAndRates(t *testing.T) {
	doc, err := Parse([]byte(growDoc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, _ := doc.Package("plan.s")
	if s.Grow == nil || !s.Grow.Allowed {
		t.Fatalf("S grow = %+v, want allowed", s.Grow)
	}
	if want := (Resources{"vcpu": 8, "memory_gb": 16, "disk_gb": 250, "bandwidth_mbps": 1000}); !reflect.DeepEqual(s.Grow.Ceiling, want) {
		t.Errorf("S ceiling = %v, want %v", s.Grow.Ceiling, want)
	}
	wantRates := []OverageRate{
		{Key: "vcpu", SKU: "k8s.vcpu", Unit: "vCPU", PriceMinor: 1992},
		{Key: "memory", SKU: "k8s.mem_gb", Unit: "GB", PriceMinor: 374},
	}
	if !reflect.DeepEqual(s.Grow.OverageRates, wantRates) {
		t.Errorf("S rates = %+v, want %+v", s.Grow.OverageRates, wantRates)
	}
	if s.Grow.OverageRates[0].PriceMonth() != "1.992" {
		t.Errorf("PriceMonth = %q", s.Grow.OverageRates[0].PriceMonth())
	}
	if want := (Resources{"vcpu": 1, "memory_gb": 2, "disk_gb": 25, "bandwidth_mbps": 50}); !reflect.DeepEqual(s.Includes, want) {
		t.Errorf("S includes = %v, want %v", s.Includes, want)
	}

	// Rates are per package; fractional ceilings parse; a missing headline
	// key stays missing and a non-numeric one is ignored.
	m, _ := doc.Package("plan.m")
	if m.Grow == nil || len(m.Grow.OverageRates) != 1 || m.Grow.OverageRates[0].PriceMinor != 1796 || m.Grow.Ceiling["memory_gb"] != 16.5 {
		t.Errorf("M grow = %+v", m.Grow)
	}
	if _, has := m.Includes["disk_gb"]; has {
		t.Errorf("M includes invented disk_gb: %v", m.Includes)
	}
	if want := (Resources{"vcpu": 2, "memory_gb": 4, "bandwidth_mbps": 100}); !reflect.DeepEqual(m.Includes, want) {
		t.Errorf("M includes = %v, want %v", m.Includes, want)
	}

	// allowed:false and an omitted block both mean no grow.
	for _, sku := range []string{"plan.l", "plan.xl"} {
		if p, _ := doc.Package(sku); p.Grow != nil {
			t.Errorf("%s grow = %+v, want nil", sku, p.Grow)
		}
	}
	if xl, _ := doc.Package("plan.xl"); len(xl.Includes) != 0 {
		t.Errorf("XL with no includes: %v", xl.Includes)
	}
}

func TestParse_GrowOnlyCellAcceptedAndReported(t *testing.T) {
	doc, err := Parse([]byte(growDoc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tc := range []struct {
		sku      string
		label    string
		growOnly bool
	}{
		{"plan.s", "single region", true},
		{"plan.m", "single region", true},
		{"plan.l", "single region", false},
		{"plan.xl", DRActivePassive, false},
	} {
		label, growOnly, known := doc.DRTopologyCell(tc.sku)
		if !known || label != tc.label || growOnly != tc.growOnly {
			t.Errorf("%s: DRTopologyCell = %q %v %v, want %q %v true", tc.sku, label, growOnly, known, tc.label, tc.growOnly)
		}
		if l, k := doc.DRTopology(tc.sku); !k || l != tc.label {
			t.Errorf("%s: DRTopology = %q %v", tc.sku, l, k)
		}
	}
}

// A grow_only cell is never an add-on, even when a document names a SKU on it.
func TestPrice_GrowOnlyCellIsNotAnAddon(t *testing.T) {
	doc, err := Parse([]byte(`{"currency":"OMR","price_book":"b","prices_as_of":"d",
	 "packages":[{"sku":"plan.s","name":"S","price_month":"1.000"},{"sku":"plan.xl","name":"XL","price_month":"9.000"}],
	 "features":[{"key":"dr","name":"DR","kind":"boolean","cells":{
	   "plan.s":{"state":"optional","grow_only":true,"addon_sku":"addon.dr","price_month":"5.000"},
	   "plan.xl":{"state":"optional","addon_sku":"addon.dr","price_month":"5.000"}}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var refused *RefusedError
	if _, err := doc.Price("plan.s", []string{"addon.dr"}); !errors.As(err, &refused) {
		t.Fatalf("Price(grow_only cell as add-on) = %v; want a RefusedError", err)
	}
	if q, err := doc.Price("plan.xl", []string{"addon.dr"}); err != nil || q.Total() != 14000 {
		t.Fatalf("XL add-on still sells: %v %+v", err, q)
	}
}

func TestParse_GrowRefusals(t *testing.T) {
	wrap := func(pkg, cells string) string {
		return `{"currency":"OMR","price_book":"b","prices_as_of":"d","packages":[` + pkg + `],
		 "features":[{"key":"f","name":"F","kind":"boolean","cells":{` + cells + `}}]}`
	}
	const okPkg = `{"sku":"plan.s","name":"S","price_month":"1.000"}`
	for _, tc := range []struct {
		name, body, want string
	}{
		{"optional without grow_only and without addon", wrap(okPkg, `"plan.s":{"state":"optional","level":0}`), "names no addon_sku"},
		{"grow ceiling missing a dimension", wrap(`{"sku":"plan.s","name":"S","price_month":"1.000","grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250}}}`, `"plan.s":{"state":"included"}`), "bandwidth_mbps"},
		{"grow ceiling zero", wrap(`{"sku":"plan.s","name":"S","price_month":"1.000","grow":{"allowed":true,"ceiling":{"vcpu":0,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1}}}`, `"plan.s":{"state":"included"}`), "vcpu"},
		{"overage rate bad money", wrap(`{"sku":"plan.s","name":"S","price_month":"1.000","grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[{"key":"vcpu","sku":"k8s.vcpu","price_month":"1.9921"}]}}`, `"plan.s":{"state":"included"}`), "overage rate"},
		{"overage rate without sku", wrap(`{"sku":"plan.s","name":"S","price_month":"1.000","grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[{"key":"vcpu","price_month":"1.000"}]}}`, `"plan.s":{"state":"included"}`), "no key or sku"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse = %v; want an error containing %q", err, tc.want)
			}
		})
	}
	// Teaser stays accepted beside the new state.
	if _, err := Parse([]byte(wrap(okPkg, `"plan.s":{"state":"teaser","included_from":"plan.m"}`))); err != nil {
		t.Fatalf("teaser refused: %v", err)
	}
}

func mustParseLive(t *testing.T) *Document {
	t.Helper()
	body, err := os.ReadFile("testdata/live-hw307-2026-10-10.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse(live): %v", err)
	}
	return doc
}

// The pinned live document has no grow blocks: it still parses, and every
// package reports no grow and its headline.
func TestParse_LiveDocumentHasHeadlineAndNoGrow(t *testing.T) {
	doc := mustParseLive(t)
	m, ok := doc.Package("plan.m")
	if !ok || m.Grow != nil || m.Includes["vcpu"] != 2 || m.Includes["disk_gb"] != 50 {
		t.Fatalf("live M = %+v", m)
	}
}
