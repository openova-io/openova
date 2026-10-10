// app_storage_quota_6971_test.go — capped mode caps disk, and only the
// customer's (#6971, founder model 2026-10-10).
//
// The customer's Applications run INSIDE the Org vCluster; the
// per-Organization platform stack (keycloak + postgresql, newapi + CNPG,
// openclaw, agenity) and the vCluster's own data volume are host objects in
// the host `<slug>` namespace. So the disk cap is a ResourceQuota in the apps
// tree (applied into the vCluster) and the host plan-quota carries NO storage
// term — proven here on the rendered objects, per plan and per mode.
package gitops

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type storageQuotaDoc struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Hard map[string]string `json:"hard"`
	} `json:"spec"`
}

func renderStorage(t *testing.T, in Inputs) (map[string][]byte, storageQuotaDoc) {
	t.Helper()
	in.Slug, in.DisplayName, in.Tier = "acme", "Acme", "org"
	in.SovereignFQDN, in.HostCluster, in.VClusterChartVersion = "x.example", "hz", "0.33.*"
	out, err := Render(in)
	if err != nil {
		t.Fatalf("Render(%+v): %v", in, err)
	}
	raw, ok := out["vcluster/apps/"+appStorageQuotaDoc]
	if !ok {
		t.Fatalf("plan %q mode %q: no vcluster/apps/%s rendered", in.PlanSlug, in.OverageMode, appStorageQuotaDoc)
	}
	var d storageQuotaDoc
	if err := yaml.Unmarshal(raw, &d); err != nil {
		t.Fatalf("storagequota.yaml: %v\n%s", err, raw)
	}
	return out, d
}

func TestRender_AppStorageQuotaPerPlanAndMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, plan, mode, ceilDisk, stop string
		want, wantMode                   string
	}{
		// capped: the package disk (workbook S 25 / M 50 / L 100 / XL 250 GB).
		{"s capped", "s", "", "", "", "25Gi", "capped"},
		{"m capped", "m", OverageModeCapped, "", "", "50Gi", "capped"},
		{"l capped", "l", OverageModeCapped, "", "", "100Gi", "capped"},
		{"xl capped", "xl", OverageModeCapped, "", "", "250Gi", "capped"},
		// grow: the disk ceiling — default the XL disk, twice it on XL.
		{"m grow default", "m", OverageModeGrow, "", "", "250Gi", "grow"},
		{"m grow chosen", "m", OverageModeGrow, "120Gi", "", "120Gi", "grow"},
		{"m grow below the package raises", "m", OverageModeGrow, "10Gi", "", "50Gi", "grow"},
		{"m grow above the top lowers", "m", OverageModeGrow, "1Ti", "", "250Gi", "grow"},
		{"xl grow default twice xl", "xl", OverageModeGrow, "", "", "500Gi", "grow"},
		{"xl grow above twice lowers", "xl", OverageModeGrow, "2Ti", "", "500Gi", "grow"},
		// the BSS spend-limit stop: grow held at the package disk.
		{"m grow stopped", "m", OverageModeGrow, "120Gi", "2026-11-01T00:00:00Z", "50Gi", "capped"},
		// a stop on a capped Organization is nothing to stop.
		{"m capped stop ignored", "m", OverageModeCapped, "", "2026-11-01T00:00:00Z", "50Gi", "capped"},
		// flexi retired → S grow.
		{"flexi is s grow", "flexi", "", "", "", "250Gi", "grow"},
	}
	for _, c := range cases {
		out, d := renderStorage(t, Inputs{PlanSlug: c.plan, OverageMode: c.mode, GrowCeilingDisk: c.ceilDisk, OverageStoppedUntil: c.stop})
		if d.Kind != "ResourceQuota" || d.Metadata.Name != AppStorageQuotaName || d.Metadata.Namespace != "apps" {
			t.Errorf("%s: %s %s/%s, want ResourceQuota apps/%s (targetNamespace rewrites apps → <slug> inside the vCluster)",
				c.name, d.Kind, d.Metadata.Namespace, d.Metadata.Name, AppStorageQuotaName)
		}
		if len(d.Spec.Hard) != 1 || d.Spec.Hard["requests.storage"] != c.want {
			t.Errorf("%s: hard = %v, want only requests.storage %s", c.name, d.Spec.Hard, c.want)
		}
		if got := d.Metadata.Annotations["openova.io/overage-mode"]; got != c.wantMode {
			t.Errorf("%s: overage-mode = %q, want %q", c.name, got, c.wantMode)
		}
		stopped := c.stop != "" && c.mode == OverageModeGrow
		if got := d.Metadata.Annotations["openova.io/overage-stopped-until"]; (got != "") != stopped || (stopped && got != c.stop) {
			t.Errorf("%s: overage-stopped-until = %q, stopped %v", c.name, got, stopped)
		}
		// The host plan-quota NEVER carries a storage term: the platform
		// stack's database volumes share the host namespace.
		rq := parseRQ(t, out)
		for k := range rq.Spec.Hard {
			if strings.Contains(k, "storage") || strings.Contains(k, "persistentvolumeclaims") {
				t.Errorf("%s: host plan-quota carries %s — it would refuse the Organization's own databases", c.name, k)
			}
		}
		// The quota is listed in the apps index, so the apps Kustomization
		// actually applies it.
		if !strings.Contains(string(out["vcluster/apps/kustomization.yaml"]), "- "+appStorageQuotaDoc) {
			t.Errorf("%s: apps/kustomization.yaml does not list %s:\n%s", c.name, appStorageQuotaDoc, out["vcluster/apps/kustomization.yaml"])
		}
		// …and NOT in the host boundary index.
		if strings.Contains(string(out["vcluster/kustomization.yaml"]), appStorageQuotaDoc) {
			t.Errorf("%s: the host boundary kustomization lists %s — it must apply inside the vCluster only", c.name, appStorageQuotaDoc)
		}
	}
}

// The spend-limit stop renders the host plan-quota capped too: the headline
// limits, the stop stamped, no grow ceiling — byte-identical to the capped
// render apart from the stop's own comment and annotation lines.
func TestRender_SpendStopRendersTheCappedPlanQuota(t *testing.T) {
	t.Parallel()
	const until = "2026-11-01T00:00:00Z"
	base := Inputs{Slug: "acme", DisplayName: "Acme", Tier: "org", PlanSlug: "m",
		SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.*"}
	capped, stopped := base, base
	capped.OverageMode = OverageModeCapped
	stopped.OverageMode, stopped.GrowCeilingCPU, stopped.GrowCeilingMemory, stopped.OverageStoppedUntil = OverageModeGrow, "4", "8Gi", until
	c, err := Render(capped)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Render(stopped)
	if err != nil {
		t.Fatal(err)
	}
	strip := func(b []byte) string {
		var keep []string
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, until) || strings.Contains(l, "limit was reached this billing period") {
				continue
			}
			keep = append(keep, l)
		}
		return strings.Join(keep, "\n")
	}
	if strip(s["vcluster/resourcequota.yaml"]) != string(c["vcluster/resourcequota.yaml"]) {
		t.Errorf("stopped plan-quota is not the capped one:\n%s\n--- capped ---\n%s", s["vcluster/resourcequota.yaml"], c["vcluster/resourcequota.yaml"])
	}
	if got := parseRQ(t, s).Metadata.Annotations["openova.io/overage-stopped-until"]; got != until {
		t.Errorf("overage-stopped-until = %q, want %q", got, until)
	}
}

func TestQuotaDiskFor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		plan, mode, ceil, want string
		grow                   bool
	}{
		{"s", "", "", "25Gi", false},
		{"s", OverageModeGrow, "", "250Gi", true},
		{"l", OverageModeGrow, "300Gi", "250Gi", true},
		{"xl", OverageModeGrow, "300Gi", "300Gi", true},
		{"flexi", "", "", "250Gi", true},
		{"bogus", "", "", "25Gi", false},
	} {
		got, grow := QuotaDiskFor(c.plan, c.mode, c.ceil)
		if got != c.want || grow != c.grow {
			t.Errorf("QuotaDiskFor(%s,%s,%s) = (%s,%v), want (%s,%v)", c.plan, c.mode, c.ceil, got, grow, c.want, c.grow)
		}
	}
}
