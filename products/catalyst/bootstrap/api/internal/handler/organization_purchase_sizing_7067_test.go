// organization_purchase_sizing_7067_test.go — the customer's purchase is
// sized from the plan's overcommit (Refs #7067, organization_purchase_sizing.go).
//
// Reads leave this module (chart values, the org-controller source); run
// under -count=1 (scripts/check-go-test-count1.sh).
package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func purchaseRepoFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{repoRootFor5389(t)}, parts...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return b
}

func digValues(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func shapeOf(t *testing.T, what string, v any) purchaseShape {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s: resources is %T, want a map", what, v)
	}
	get := func(side, res string) string {
		s, _ := digValues(m, side, res)
		if s == nil {
			return ""
		}
		q := resource.MustParse(strings.TrimSpace(toString(s)))
		return q.String()
	}
	return purchaseShape{
		RequestsCPU: get("requests", "cpu"), RequestsMemory: get("requests", "memory"),
		LimitsCPU: get("limits", "cpu"), LimitsMemory: get("limits", "memory"),
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		b, _ := yaml.Marshal(x)
		return strings.TrimSpace(string(b))
	}
}

func canon(s string) string {
	q := resource.MustParse(s)
	return q.String()
}

// The ratio is the org-controller's, not a second opinion.
func TestPurchaseSizing_RatioIsTheOrgControllers(t *testing.T) {
	t.Parallel()
	src := string(purchaseRepoFile(t, "core", "controllers", "organization", "internal", "gitops", "manifests.go"))
	for name, want := range map[string]int{"planOvercommitCPU": purchaseOvercommitCPU, "planOvercommitMemory": purchaseOvercommitMemory} {
		m := regexp.MustCompile(`(?m)^\s*` + name + `\s*=\s*(\d+)\s*$`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in the org-controller source — re-point this pin", name)
		}
		if m[1] != strconv.Itoa(want) {
			t.Errorf("org-controller %s = %s, the BSS door sizes the purchase with %d — the two must move together", name, m[1], want)
		}
	}
}

func TestRatioSized_RoundsUp(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ cpu, mem, wantCPU, wantMem string }{
		{"500m", "1Gi", "84m", "342Mi"}, // 83.3m → 84m; 341.3Mi → 342Mi
		{"100m", "128Mi", "17m", "43Mi"},
		{"100m", "64Mi", "17m", "22Mi"},
		{"600m", "3Gi", "100m", "1Gi"},
	} {
		got := ratioSized(c.cpu, c.mem)
		if canon(got.RequestsCPU) != canon(c.wantCPU) || canon(got.RequestsMemory) != canon(c.wantMem) {
			t.Errorf("ratioSized(%s, %s) = %s / %s, want %s / %s", c.cpu, c.mem, got.RequestsCPU, got.RequestsMemory, c.wantCPU, c.wantMem)
		}
		lc, rc := resource.MustParse(c.cpu), resource.MustParse(got.RequestsCPU)
		if lc.MilliValue() > rc.MilliValue()*purchaseOvercommitCPU {
			t.Errorf("%s / %s exceeds the %d:1 ratio", c.cpu, got.RequestsCPU, purchaseOvercommitCPU)
		}
	}
}

// renderedPurchaseValues renders the overlay and returns the values of the two
// purchase HelmReleases.
func renderedPurchaseValues(t *testing.T) map[string]map[string]any {
	t.Helper()
	files, err := renderOrganizationOverlay(store.OrganizationProvisionRecord{
		OrganizationID: "t1", OTECHFQDN: "t1.omani.works", Subdomain: "acme", TenantNamespace: "acme",
		ParentDomain: "omani.homes", AdminEmail: "a@b.c", CompanyName: "Acme",
	}, OrganizationChartVersions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, rel := range []string{"bp-stalwart-tenant", "bp-wordpress-tenant"} {
		var hr struct {
			Spec struct {
				Values map[string]any `json:"values"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(files[rel+".yaml"]), &hr); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		out[rel] = hr.Spec.Values
	}
	return out
}

// Every purchase container renders request = limit / ratio, its limit is the
// chart's own, and no other resources block hides in the purchase values.
func TestPurchaseSizing_RenderedValuesAreRatioSized(t *testing.T) {
	t.Parallel()
	vals := renderedPurchaseValues(t)
	modelled := map[string]bool{}
	for _, w := range purchasedWorkloads {
		modelled[w.Release+":"+strings.Join(w.ValuesPath, ".")] = true
		v, ok := digValues(vals[w.Release], w.ValuesPath...)
		if !ok {
			t.Errorf("%s: %s renders no %s", w.Key, w.Release, strings.Join(w.ValuesPath, "."))
			continue
		}
		got := shapeOf(t, w.Key, v)
		want := ratioSized(w.LimitsCPU, w.LimitsMemory)
		for _, c := range [][3]string{
			{"requests.cpu", got.RequestsCPU, want.RequestsCPU}, {"requests.memory", got.RequestsMemory, want.RequestsMemory},
			{"limits.cpu", got.LimitsCPU, want.LimitsCPU}, {"limits.memory", got.LimitsMemory, want.LimitsMemory},
		} {
			if c[1] == "" || canon(c[1]) != canon(c[2]) {
				t.Errorf("%s %s = %q, want %q (request = limit / %d cpu, / %d memory)", w.Key, c[0], c[1], c[2], purchaseOvercommitCPU, purchaseOvercommitMemory)
			}
		}
	}
	// Walk every resources block in the purchase values: each one is modelled.
	var walk func(rel string, path []string, v any)
	walk = func(rel string, path []string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		for k, sub := range m {
			p := append(append([]string(nil), path...), k)
			if k == "resources" && !modelled[rel+":"+strings.Join(p, ".")] {
				t.Errorf("%s renders %s outside purchasedWorkloads — size it by the ratio and add it to the table", rel, strings.Join(p, "."))
			}
			walk(rel, p, sub)
		}
	}
	for rel, v := range vals {
		walk(rel, nil, v)
	}
	if len(purchasedWorkloads) != 5 {
		t.Errorf("purchasedWorkloads has %d rows, this test pins 5", len(purchasedWorkloads))
	}
}

// The limits are the charts' own; only the request moves.
func TestPurchaseSizing_LimitsAreTheChartDefaults(t *testing.T) {
	t.Parallel()
	charts := map[string]map[string]any{}
	for rel, dir := range map[string]string{"bp-stalwart-tenant": "stalwart-tenant", "bp-wordpress-tenant": "wordpress-tenant"} {
		var v map[string]any
		if err := yaml.Unmarshal(purchaseRepoFile(t, "platform", dir, "chart", "values.yaml"), &v); err != nil {
			t.Fatal(err)
		}
		charts[rel] = v
	}
	for _, w := range purchasedWorkloads {
		v, ok := digValues(charts[w.Release], w.ValuesPath...)
		if !ok {
			t.Errorf("%s: chart %s has no %s — the values path moved", w.Key, w.Release, strings.Join(w.ValuesPath, "."))
			continue
		}
		def := shapeOf(t, w.Key, v)
		if canon(def.LimitsMemory) != canon(w.LimitsMemory) {
			t.Errorf("%s: chart limits.memory %s, purchasedWorkloads keeps %s", w.Key, def.LimitsMemory, w.LimitsMemory)
		}
		switch {
		case w.Key == "wordpress-db":
			if def.LimitsCPU != "" {
				t.Errorf("wordpress-db: the chart now sets limits.cpu %s — keep the chart's figure instead of the stated 500m", def.LimitsCPU)
			}
		case canon(def.LimitsCPU) != canon(w.LimitsCPU):
			t.Errorf("%s: chart limits.cpu %s, purchasedWorkloads keeps %s", w.Key, def.LimitsCPU, w.LimitsCPU)
		}
	}
}

// TestPurchaseSizing_BundleLimitsAgainstHeadline states, per plan, whether the
// long-running purchase pods' LIMITS fit the package headline (the
// customer-scope plan-quota limits.*). With requests ratio-sized, the request
// side fits whenever this does. The headlines are the catalog seed's
// (S 1/2Gi, M 2/4Gi, L 4/8Gi, XL 8/16Gi).
//
// S does NOT fit the delivered bundle (1.6 vCPU / 2176Mi of container limits
// against 1 vCPU / 2Gi): an S Organization in capped mode cannot run
// WordPress + Stalwart within the package it bought, and in grow mode the
// excess is billed. That is a product decision (the bundle on S), recorded
// here so it is visible rather than borrowed from the platform's slack as it
// was before #7067.
func TestPurchaseSizing_BundleLimitsAgainstHeadline(t *testing.T) {
	t.Parallel()
	var cpu, mem resource.Quantity
	for _, w := range purchasedWorkloads {
		if w.Key == "stalwart-setup" { // a hook: runs only during a Helm operation
			continue
		}
		cpu.Add(resource.MustParse(w.LimitsCPU))
		mem.Add(resource.MustParse(w.LimitsMemory))
	}
	headline := map[string][2]string{"s": {"1", "2Gi"}, "m": {"2", "4Gi"}, "l": {"4", "8Gi"}, "xl": {"8", "16Gi"}}
	fits := map[string]bool{}
	for plan, h := range headline {
		c, m := resource.MustParse(h[0]), resource.MustParse(h[1])
		fits[plan] = cpu.Cmp(c) <= 0 && mem.Cmp(m) <= 0
	}
	for _, p := range []string{"m", "l", "xl"} {
		if !fits[p] {
			t.Errorf("plan %s: the purchase bundle's limits (%s cpu, %s memory) exceed the headline %v", p, cpu.String(), mem.String(), headline[p])
		}
	}
	if fits["s"] {
		t.Logf("plan s now fits the bundle (%s / %s) — drop the S note above", cpu.String(), mem.String())
	} else {
		t.Logf("plan s: bundle limits %s cpu / %s memory exceed the S headline 1 / 2Gi (product decision, see the test comment)", cpu.String(), mem.String())
	}
}
