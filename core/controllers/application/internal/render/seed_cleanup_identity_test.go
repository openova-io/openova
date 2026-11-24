// Check FND-G12 (journey J5.2): the identity the DeletingPolicy
// replication-ca-seed-orphan (platform/kyverno-policies) keys on.
//
// bp-wordpress-tenant labels its seed CA Secret with
// `catalyst.openova.io/app: <openova.application.name>`, and the policy deletes
// the Secret once no HelmRelease labelled `catalyst.openova.io/app` with that
// value is left in the Secret's namespace. These tests pin the three facts that
// rule rests on: every HelmRelease carries the whole Application name in
// LabelApp, even when its own name is shortened; the values the chart reads
// carry the same name; and a host leg's HelmRelease lives in
// openova.application.namespace (the chart labels the Secret only there).

package render

import (
	"strings"
	"testing"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// seedCleanupLongApp is the long Application name of the policy suite
// (platform/kyverno-policies/chart/tests/replication-ca-seed-orphan).
const seedCleanupLongApp = "marketing-site-of-the-acme-trading-company-in-muscat-oman-2"

func applicationValues(t *testing.T, hr *unstructured.Unstructured) (string, string) {
	t.Helper()
	name, _, err := unstructured.NestedString(hr.Object, "spec", "values", TopologyValuesKey, "application", "name")
	if err != nil {
		t.Fatalf("%s: openova.application.name: %v", hr.GetName(), err)
	}
	ns, _, err := unstructured.NestedString(hr.Object, "spec", "values", TopologyValuesKey, "application", "namespace")
	if err != nil {
		t.Fatalf("%s: openova.application.namespace: %v", hr.GetName(), err)
	}
	return name, ns
}

func TestFanoutHRs_SeedCleanupIdentity_HostLegs(t *testing.T) {
	variant := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		RegionFor:    testRegionFor,
		AppName:      seedCleanupLongApp,
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "bp-wordpress-tenant",
	})
	if err != nil {
		t.Fatalf("FanoutHRs: %v", err)
	}
	if len(hrs) != 2 {
		t.Fatalf("want 2 HelmReleases, got %d", len(hrs))
	}
	for _, hr := range hrs {
		cluster := hr.GetLabels()[LabelCluster]
		if full := strings.ToLower(seedCleanupLongApp + "-" + cluster); hr.GetName() == full || len(hr.GetName()) > HRName63 {
			t.Fatalf("HelmRelease name %q: want the shortened form of %q (<= %d chars)", hr.GetName(), full, HRName63)
		}
		if got := hr.GetLabels()[LabelApp]; got != seedCleanupLongApp {
			t.Fatalf("%s: label %s = %q, want the whole Application name %q", hr.GetName(), LabelApp, got, seedCleanupLongApp)
		}
		name, ns := applicationValues(t, hr)
		if name != seedCleanupLongApp {
			t.Fatalf("%s: openova.application.name = %q, want %q (the value the seed Secret's label carries)", hr.GetName(), name, seedCleanupLongApp)
		}
		if hr.GetNamespace() != ns {
			t.Fatalf("%s: HelmRelease namespace %q != openova.application.namespace %q", hr.GetName(), hr.GetNamespace(), ns)
		}
		if _, found, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace"); found {
			t.Fatalf("%s: spec.targetNamespace is set; the release namespace must be the HelmRelease's own", hr.GetName())
		}
	}
}

// On a vCluster pivot leg the HelmRelease lives in the vCluster's host
// namespace, not in openova.application.namespace, so the chart leaves the
// Secret unlabelled there and the policy never judges it.
func TestFanoutHRs_SeedCleanupIdentity_PivotLegLivesElsewhere(t *testing.T) {
	variant := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		RegionFor:      testRegionFor,
		AppName:        "blog",
		AppNamespace:   "acme",
		WriteNamespace: "vc-rtz-host",
		Topology:       bpv1alpha1.BcpActiveHotStandby,
		Variant:        &variant,
		Chart:          "bp-wordpress-tenant",
		KubeConfigSecretFor: func(cluster string) (string, string) {
			return "vc-" + strings.ToLower(cluster), "vc-rtz-host"
		},
	})
	if err != nil {
		t.Fatalf("FanoutHRs: %v", err)
	}
	for _, hr := range hrs {
		if got := hr.GetLabels()[LabelApp]; got != "blog" {
			t.Fatalf("%s: label %s = %q, want blog", hr.GetName(), LabelApp, got)
		}
		_, ns := applicationValues(t, hr)
		if hr.GetNamespace() == ns {
			t.Fatalf("%s: a pivot leg's HelmRelease is in %q, the Application namespace; the PR's pivot statement assumes the host namespace", hr.GetName(), ns)
		}
	}
}

// The suite's long-name HelmRelease is the name HRNameFor gives.
func TestHRNameFor_SeedCleanupFixture(t *testing.T) {
	const want = "marketing-site-of-the-acme-trading-company-in-muscat-oman-7a7c1"
	if got := HRNameFor(seedCleanupLongApp, "rtz-A"); got != want {
		t.Fatalf("HRNameFor = %q, want %q (context.yaml of the policy suite)", got, want)
	}
}
