package render

// fanout_standby_6268_test.go — the standby leg of an active-hot-standby
// placement: WHAT it boots as, and WHERE it lands.
//
// The measured shape these tests were written against (#6268, hw296 dep
// e689e3b34a75fdec, a freshly Catalog-provisioned `walkfour/r60fresh` —
// bp-postgres, mode active-hot-standby, both regions, phase Ready, no
// hand-written spec.placement.targets[]):
//
//	HelmRelease walkfour/r60fresh-rtz-b
//	  labels: cluster=rtz-B  role=passive  topology=active-hot-standby
//	  spec.kubeConfig:  ABSENT
//	  spec.values:      {_openova_standby: true, replicas: 0, ...}
//
//	region B, namespace walkfour:  did not exist
//
// The Application declared a HOT standby; the platform rendered a COLD
// one, into the PRIMARY region, and reported `Ready` over it.
//
// Refs #6268 #3375 #3986.

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// twoClusterVariant is the shape bp-postgres actually declares for
// active-hot-standby: two clusters, explicit roles, rtz tier. Using the
// catalog's own shape keeps these tests measuring the path a Catalog
// provision takes rather than a shape invented for the test.
func twoClusterVariant() bpv1alpha1.TopologyVariant {
	return bpv1alpha1.TopologyVariant{
		Placement: &bpv1alpha1.PlacementSpec{
			Tier:     "rtz",
			Clusters: []string{"rtz-A", "rtz-B"},
			Roles: map[string]string{
				"rtz-A": "active",
				"rtz-B": "passive",
			},
		},
	}
}

// deliverRegionB resolves the standby cluster to a remote-region
// kubeconfig Secret and the primary cluster to nothing (host placement,
// no pivot) — the resolution a wired cross-region fan-out performs.
func deliverRegionB(cluster string) (string, string) {
	if cluster == "rtz-B" {
		return "region-b-kubeconfig", "catalyst"
	}
	return "", ""
}

// hrByRole returns the single HR carrying the given role label. It fails
// the test when there is not exactly one, so an assertion can never pass
// by finding nothing to assert on.
func hrByRole(t *testing.T, hrs []*unstructured.Unstructured, role string) *unstructured.Unstructured {
	t.Helper()
	var found []*unstructured.Unstructured
	for _, hr := range hrs {
		if hr.GetLabels()[LabelRole] == role {
			found = append(found, hr)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 HR with role=%q; got %d (of %d rendered)", role, len(found), len(hrs))
	}
	return found[0]
}

// hrValues returns spec.values, failing when absent.
func hrValues(t *testing.T, hr *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	spec, ok := hr.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatalf("HR %q has no spec", hr.GetName())
	}
	v, ok := spec["values"].(map[string]interface{})
	if !ok {
		t.Fatalf("HR %q has no spec.values", hr.GetName())
	}
	return v
}

// hrKubeConfigSecretName returns spec.kubeConfig.secretRef.name, or ""
// when the block is absent.
func hrKubeConfigSecretName(hr *unstructured.Unstructured) string {
	name, _, _ := unstructured.NestedString(hr.Object, "spec", "kubeConfig", "secretRef", "name")
	return name
}

// A HOT standby that IS delivered to its own cluster keeps its declared
// replica count and carries the kubeConfig pivot into the standby region.
//
// The replica-count half is the assertion that goes red against the
// pre-#6268 renderer, which applied `replicas: 0` to every passive leg
// regardless of posture. A hot standby is defined by streaming from the
// primary, and a workload scaled to zero cannot stream — so the cold
// overlay did not merely mis-size the standby, it contradicted the
// posture the operator chose in the Catalog.
func TestFanoutHRs_6268_HotStandbyDelivered_StreamsAndCarriesKubeConfig(t *testing.T) {
	variant := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:             "r60fresh",
		AppNamespace:        "walkfour",
		Topology:            bpv1alpha1.BcpActiveHotStandby,
		Variant:             &variant,
		Chart:               "bp-postgres",
		KubeConfigSecretFor: deliverRegionB,
		KubeConfigSecretKey: "config",
		Values:              map[string]interface{}{"replicas": 3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	standby := hrByRole(t, hrs, RolePassive)

	// WHERE — the standby leg pivots into the standby region.
	if got := hrKubeConfigSecretName(standby); got != "region-b-kubeconfig" {
		t.Fatalf("standby HR spec.kubeConfig.secretRef.name = %q, want %q — "+
			"a standby with no kubeConfig installs into the PRIMARY region beside its active peer",
			got, "region-b-kubeconfig")
	}
	// WHAT — a hot standby streams, so it is NOT scaled to zero.
	// Asserted BEFORE the delivery label deliberately: this is the
	// substantive behaviour change, and a preceding label assertion
	// would short-circuit it, leaving the assertion that matters most
	// unproven against the pre-fix renderer.
	sv := hrValues(t, standby)
	if got := sv["replicas"]; got == int64(0) || got == 0 {
		t.Fatalf("hot standby HR replicas = %v — the COLD overlay was applied to a HOT standby; "+
			"a replica scaled to zero cannot stream from the primary", got)
	}
	if got := sv["replicas"]; got != 3 {
		t.Fatalf("hot standby HR replicas = %v (%T), want the declared count 3", got, got)
	}
	// It is still the standby: the boolean marker charts key off
	// (CNPG `replica.enabled`) must survive.
	if got, _ := sv[StandbyMarker].(bool); !got {
		t.Fatalf("hot standby HR must still carry %s=true — it is a replica, not a second primary", StandbyMarker)
	}
	if got := standby.GetLabels()[LabelStandbyDelivery]; got != StandbyDeliveryRemote {
		t.Fatalf("standby HR %s = %q, want %q", LabelStandbyDelivery, got, StandbyDeliveryRemote)
	}

	// The active leg is untouched by any of this.
	active := hrByRole(t, hrs, RoleActive)
	av := hrValues(t, active)
	if got := av["replicas"]; got != 3 {
		t.Fatalf("active HR replicas = %v, want 3", got)
	}
	if _, present := av[StandbyMarker]; present {
		t.Fatalf("active HR must NOT carry the %s marker", StandbyMarker)
	}
	if _, present := active.GetLabels()[LabelStandbyDelivery]; present {
		t.Fatalf("active HR must NOT carry the %s label — it has no standby leg", LabelStandbyDelivery)
	}
	if got := hrKubeConfigSecretName(active); got != "" {
		t.Fatalf("active HR kubeConfig = %q, want none (same-region host placement)", got)
	}
}

// A COLD standby (active-passive) stays scaled to zero even when it IS
// delivered to its own cluster. This is the control that keeps the fix
// honest: "hot standbys stream" must not become "the scale-down was
// deleted". active-passive is the rebuild-on-failover posture and its
// standby runs no process by definition.
func TestFanoutHRs_6268_ColdStandbyDelivered_StaysScaledDown(t *testing.T) {
	variant := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:             "coldapp",
		AppNamespace:        "walkfour",
		Topology:            bpv1alpha1.BcpActivePassive,
		Variant:             &variant,
		Chart:               "bp-postgres",
		KubeConfigSecretFor: deliverRegionB,
		KubeConfigSecretKey: "config",
		Values:              map[string]interface{}{"replicas": 3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	standby := hrByRole(t, hrs, RolePassive)
	sv := hrValues(t, standby)
	if got := sv["replicas"]; got != int64(0) {
		t.Fatalf("cold standby HR replicas = %v (%T), want int64(0) — "+
			"active-passive is the rebuild-on-failover posture", got, got)
	}
	if got, _ := sv[StandbyMarker].(bool); !got {
		t.Fatalf("cold standby HR must carry %s=true", StandbyMarker)
	}
	// Delivered is delivered, regardless of hot/cold.
	if got := standby.GetLabels()[LabelStandbyDelivery]; got != StandbyDeliveryRemote {
		t.Fatalf("cold standby HR %s = %q, want %q", LabelStandbyDelivery, got, StandbyDeliveryRemote)
	}
}

// An UNDELIVERED hot standby — the live hw296 shape — stays cold, and
// says so on the object.
//
// This is the safety property that makes the hot-standby change
// deployable ahead of cross-region delivery. With no kubeConfig the HR
// installs into the cluster the controller itself runs in, i.e. the same
// cluster AND namespace as its active peer. Booting it hot there would
// not produce a standby; it would install a second full primary beside
// the first, under a name that says standby. A future change that
// relaxes standbyIsHot() to "the topology says hot" will fail here, and
// that failure is the point.
func TestFanoutHRs_6268_UndeliveredHotStandby_StaysColdAndSaysSo(t *testing.T) {
	variant := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "r60fresh",
		AppNamespace: "walkfour",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "bp-postgres",
		// No KubeConfigSecretFor at all — the measured hw296 shape,
		// where the CNPG host-side rule suppressed the seam for the
		// whole Application and no remote-region secret was wired.
		Values: map[string]interface{}{"replicas": 3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	standby := hrByRole(t, hrs, RolePassive)

	if got := hrKubeConfigSecretName(standby); got != "" {
		t.Fatalf("precondition broken: this case must render an UNDELIVERED standby; got kubeConfig %q", got)
	}
	sv := hrValues(t, standby)
	if got := sv["replicas"]; got != int64(0) {
		t.Fatalf("undelivered standby HR replicas = %v (%T), want int64(0) — "+
			"it shares a cluster and namespace with its active peer, so booting it hot "+
			"installs a DUPLICATE PRIMARY, not a replica", got, got)
	}
	if got := standby.GetLabels()[LabelStandbyDelivery]; got != StandbyDeliveryLocal {
		t.Fatalf("undelivered standby HR %s = %q, want %q — an unmet placement must be legible "+
			"on the object, not silently identical to a met one",
			LabelStandbyDelivery, got, StandbyDeliveryLocal)
	}
}

// The delivery label is scoped to standby legs only. Without this, the
// label would be an unconditional field rather than a signal, and its
// presence on an object would mean nothing.
func TestFanoutHRs_6268_DeliveryLabelOnlyOnStandbyLegs(t *testing.T) {
	singleton := bpv1alpha1.TopologyVariant{
		Placement: &bpv1alpha1.PlacementSpec{
			Tier:     "rtz",
			Clusters: []string{"rtz-A"},
			Roles:    map[string]string{"rtz-A": "singleton"},
		},
	}
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:             "solo",
		AppNamespace:        "walkfour",
		Topology:            bpv1alpha1.BcpSingleton,
		Variant:             &singleton,
		Chart:               "bp-postgres",
		KubeConfigSecretFor: deliverRegionB,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, hr := range hrs {
		if _, present := hr.GetLabels()[LabelStandbyDelivery]; present {
			t.Fatalf("singleton HR %q carries %s; the label is meaningful only on a standby leg",
				hr.GetName(), LabelStandbyDelivery)
		}
	}

	// The other half of "scoped": the label must actually EXIST on a
	// standby leg. Without this the test passes on a build that never
	// stamps the label anywhere — which is precisely the pre-fix build,
	// and would make this a guard that cannot fail.
	pair := twoClusterVariant()
	paired, err := FanoutHRs(FanoutInputs{
		AppName:             "r60fresh",
		AppNamespace:        "walkfour",
		Topology:            bpv1alpha1.BcpActiveHotStandby,
		Variant:             &pair,
		Chart:               "bp-postgres",
		KubeConfigSecretFor: deliverRegionB,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := hrByRole(t, paired, RolePassive).GetLabels()[LabelStandbyDelivery]; got == "" {
		t.Fatalf("standby leg carries no %s — the label is stamped nowhere, so the scope check above is vacuous",
			LabelStandbyDelivery)
	}
}

// The per-cluster status rollup carries delivery through, so a reader
// asking "is this Application actually in two regions?" can answer it.
// Without this the status reports cluster + role + Ready for a standby
// that never left the primary region — and the HR genuinely IS Ready,
// because it installed successfully, just not where the placement says.
func TestPerClusterStatusesFor_6268_CarriesStandbyDelivery(t *testing.T) {
	variant := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "r60fresh",
		AppNamespace: "walkfour",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "bp-postgres",
		Values:       map[string]interface{}{"replicas": 3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	statuses := PerClusterStatusesFor(hrs)
	if len(statuses) != 2 {
		t.Fatalf("want 2 per-cluster statuses; got %d", len(statuses))
	}
	var sawStandby bool
	for _, s := range statuses {
		switch s.Role {
		case RolePassive:
			sawStandby = true
			if s.StandbyDelivery != StandbyDeliveryLocal {
				t.Fatalf("passive status for %q StandbyDelivery = %q, want %q",
					s.Cluster, s.StandbyDelivery, StandbyDeliveryLocal)
			}
		case RoleActive:
			if s.StandbyDelivery != "" {
				t.Fatalf("active status for %q must not carry StandbyDelivery; got %q",
					s.Cluster, s.StandbyDelivery)
			}
		}
	}
	if !sawStandby {
		t.Fatalf("no passive status found — nothing was asserted")
	}
}
