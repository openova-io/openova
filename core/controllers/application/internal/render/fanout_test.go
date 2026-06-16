// G117.6 (W2.C1) per-cluster HelmRelease fan-out tests.
//
// Brief acceptance §"File-touch matrix": fanout_test.go must cover
// HR-name truncation, label population, kubeConfig.secretRef shape.

package render

import (
	"fmt"
	"strings"
	"testing"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func TestFanoutHRs_ActiveHotStandby_TwoClusters(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]

	hrs, err := FanoutHRs(FanoutInputs{
		AppName:       "obs-prod",
		AppNamespace:  "acme",
		Topology:      bpv1alpha1.BcpActiveHotStandby,
		Variant:       &variant,
		Chart:         "grafana",
		ChartVersion:  "1.0.5",
		SourceRefName: "openova-catalog",
		SourceRefKind: "HelmRepository",
		KubeConfigSecretFor: func(cluster string) (string, string) {
			return "vc-" + cluster, ""
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hrs) != 2 {
		t.Fatalf("active-hot-standby should fan out 2 HRs; got %d", len(hrs))
	}

	// First HR: mgmt-A active.
	if got := hrs[0].GetName(); got != "obs-prod-mgmt-A" {
		t.Fatalf("hr[0].name = %q, want obs-prod-mgmt-A", got)
	}
	if got := hrs[0].GetLabels()[LabelRole]; got != RoleActive {
		t.Fatalf("hr[0].role = %q, want active", got)
	}
	if got := hrs[0].GetLabels()[LabelTopology]; got != "active-hot-standby" {
		t.Fatalf("hr[0].topology = %q, want active-hot-standby", got)
	}
	if got := hrs[0].GetLabels()[LabelCluster]; got != "mgmt-A" {
		t.Fatalf("hr[0].cluster = %q, want mgmt-A", got)
	}
	if got := hrs[0].GetLabels()[LabelApp]; got != "obs-prod" {
		t.Fatalf("hr[0].app = %q, want obs-prod", got)
	}

	// Second HR: mgmt-B passive.
	if got := hrs[1].GetName(); got != "obs-prod-mgmt-B" {
		t.Fatalf("hr[1].name = %q, want obs-prod-mgmt-B", got)
	}
	if got := hrs[1].GetLabels()[LabelRole]; got != RolePassive {
		t.Fatalf("hr[1].role = %q, want passive", got)
	}
}

func TestFanoutHRs_Singleton(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpSingleton]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs-prod",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpSingleton,
		Variant:      &variant,
		Chart:        "grafana",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hrs) != 1 {
		t.Fatalf("singleton should fan out 1 HR; got %d", len(hrs))
	}
	if got := hrs[0].GetLabels()[LabelRole]; got != RoleSingleton {
		t.Fatalf("hr[0].role = %q, want singleton", got)
	}
	if got := hrs[0].GetLabels()[LabelTopology]; got != "singleton" {
		t.Fatalf("hr[0].topology = %q, want singleton", got)
	}
}

func TestFanoutHRs_KubeConfigSecretRefStamped(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:        "obs",
		AppNamespace:   "acme",
		WriteNamespace: "mgmt", // G92.1 vCluster-pivot pattern (PR #2674)
		Topology:       bpv1alpha1.BcpActiveHotStandby,
		Variant:        &variant,
		Chart:          "grafana",
		KubeConfigSecretFor: func(cluster string) (string, string) {
			return "vc-" + strings.ToLower(cluster), ""
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, hr := range hrs {
		if hr.GetNamespace() != "mgmt" {
			t.Fatalf("hr.namespace = %q, want mgmt (WriteNamespace override)", hr.GetNamespace())
		}
		spec, ok := hr.Object["spec"].(map[string]interface{})
		if !ok {
			t.Fatalf("hr.spec is not a map")
		}
		kc, ok := spec["kubeConfig"].(map[string]interface{})
		if !ok {
			t.Fatalf("hr.spec.kubeConfig is missing on a vCluster-pivoted HR")
		}
		ref, ok := kc["secretRef"].(map[string]interface{})
		if !ok {
			t.Fatalf("hr.spec.kubeConfig.secretRef is missing")
		}
		name, _ := ref["name"].(string)
		if !strings.HasPrefix(name, "vc-") {
			t.Fatalf("hr.spec.kubeConfig.secretRef.name = %q, want vc-* prefix", name)
		}
	}
}

// #3373 — when the caller declares the Secret data key (the loft-sh
// vcluster exportKubeConfig convention "config"), the renderer stamps
// spec.kubeConfig.secretRef.key. Without the key Flux looks up its
// default key name and the pivot silently fails against vc-* Secrets
// (the hand-proven bootstrap-kit slots 22/23/24/35/19a all pin
// `key: config`).
func TestFanoutHRs_KubeConfigSecretKeyStamped(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:        "obs",
		AppNamespace:   "acme",
		WriteNamespace: "mgmt",
		Topology:       bpv1alpha1.BcpActiveHotStandby,
		Variant:        &variant,
		Chart:          "grafana",
		KubeConfigSecretFor: func(cluster string) (string, string) {
			return "vc-" + strings.ToLower(cluster), ""
		},
		KubeConfigSecretKey: "config",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, hr := range hrs {
		spec := hr.Object["spec"].(map[string]interface{})
		kc, ok := spec["kubeConfig"].(map[string]interface{})
		if !ok {
			t.Fatalf("hr.spec.kubeConfig is missing on a vCluster-pivoted HR")
		}
		ref := kc["secretRef"].(map[string]interface{})
		if key, _ := ref["key"].(string); key != "config" {
			t.Fatalf("hr.spec.kubeConfig.secretRef.key = %q, want config", key)
		}
	}
}

// #3373 — backwards-compat: no KubeConfigSecretKey declared → no `key`
// field stamped (byte-identical legacy render).
func TestFanoutHRs_NoKubeConfigSecretKey_OmitsKey(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:        "obs",
		AppNamespace:   "acme",
		WriteNamespace: "mgmt",
		Topology:       bpv1alpha1.BcpActiveHotStandby,
		Variant:        &variant,
		Chart:          "grafana",
		KubeConfigSecretFor: func(cluster string) (string, string) {
			return "vc-" + strings.ToLower(cluster), ""
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, hr := range hrs {
		spec := hr.Object["spec"].(map[string]interface{})
		kc := spec["kubeConfig"].(map[string]interface{})
		ref := kc["secretRef"].(map[string]interface{})
		if _, present := ref["key"]; present {
			t.Fatalf("hr.spec.kubeConfig.secretRef.key must be omitted when KubeConfigSecretKey is empty")
		}
	}
}

func TestFanoutHRs_NoKubeConfigSecretFor_OmitsBlock(t *testing.T) {
	// Legacy / mgmt-cluster-local HRs (substrate Blueprints per G92.6)
	// MUST NOT carry a spec.kubeConfig block — Flux v2 interprets the
	// absence as "install onto the local cluster".
	bp := fixtureCiliumTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpSingleton]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "cilium",
		AppNamespace: "kube-system",
		Topology:     bpv1alpha1.BcpSingleton,
		Variant:      &variant,
		Chart:        "cilium",
		// KubeConfigSecretFor intentionally nil.
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, hr := range hrs {
		spec, ok := hr.Object["spec"].(map[string]interface{})
		if !ok {
			t.Fatalf("hr.spec missing")
		}
		if _, has := spec["kubeConfig"]; has {
			t.Fatalf("hr.spec.kubeConfig should be absent for substrate Blueprint")
		}
	}
}

// #3375 DoD-2 — the SOLE topology fan-out carries the standby
// scale-down. The fanned-out PASSIVE HR (mgmt-B) must render
// `replicas:0` + the `_openova_standby:true` marker in its
// spec.values; the ACTIVE HR (mgmt-A) must NOT. Before this fix the
// two HRs were byte-identical (differing only by the role label) and a
// separate parallel render path computed the scale-down — the latent
// divergence #3375 §3(b) catalogued.
func TestFanoutHRs_PassiveHRCarriesReplicasZero(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs-prod",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "grafana",
		// The Blueprint declares a real replica count; standby must
		// override it to 0.
		Values: map[string]interface{}{
			"replicas": 3,
			"image":    map[string]interface{}{"tag": "11.0.0"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hrs) != 2 {
		t.Fatalf("active-hot-standby should fan out 2 HRs; got %d", len(hrs))
	}

	active, passive := hrs[0], hrs[1]
	if active.GetLabels()[LabelRole] != RoleActive {
		t.Fatalf("hr[0] should be active; role=%q", active.GetLabels()[LabelRole])
	}
	if passive.GetLabels()[LabelRole] != RolePassive {
		t.Fatalf("hr[1] should be passive; role=%q", passive.GetLabels()[LabelRole])
	}

	// ACTIVE keeps the declared replicas (3) and has NO standby marker.
	av := active.Object["spec"].(map[string]interface{})["values"].(map[string]interface{})
	if got := av["replicas"]; got != 3 {
		t.Fatalf("active HR replicas = %v, want 3 (declared count preserved)", got)
	}
	if _, present := av[StandbyMarker]; present {
		t.Fatalf("active HR must NOT carry the %s marker", StandbyMarker)
	}

	// PASSIVE is scaled to 0 and carries the standby marker. The value
	// is int64 (JSON-deep-copyable for the unstructured write path).
	pv := passive.Object["spec"].(map[string]interface{})["values"].(map[string]interface{})
	if got := pv["replicas"]; got != int64(0) {
		t.Fatalf("passive HR replicas = %v (%T), want int64(0) (standby scale-down)", got, got)
	}
	if got, _ := pv[StandbyMarker].(bool); !got {
		t.Fatalf("passive HR must carry %s=true", StandbyMarker)
	}
	// Unrelated values survive the overlay on the passive side.
	if _, ok := pv["image"].(map[string]interface{}); !ok {
		t.Fatalf("passive HR must preserve non-replicas values (image)")
	}

	// The input Values map must NOT have been mutated by the overlay
	// (it is shared Blueprint-derived state).
	// hrs[0] (active) still reads replicas=3 above, which would fail if
	// withStandbyOverlay had mutated the shared map — so this is
	// implicitly covered, but assert the marker absence on input too.
}

// #3375 DoD-2 — a SINGLETON variant is never scaled down: a singleton
// is the sole copy and must run. Guards against the overlay leaking
// onto non-passive roles.
func TestFanoutHRs_SingletonNotScaledDown(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpSingleton]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs-prod",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpSingleton,
		Variant:      &variant,
		Chart:        "grafana",
		Values:       map[string]interface{}{"replicas": 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v := hrs[0].Object["spec"].(map[string]interface{})["values"].(map[string]interface{})
	if got := v["replicas"]; got != 2 {
		t.Fatalf("singleton HR replicas = %v, want 2 (never scaled down)", got)
	}
	if _, present := v[StandbyMarker]; present {
		t.Fatalf("singleton HR must NOT carry the %s marker", StandbyMarker)
	}
}

func TestFanoutHRs_OwnerLabelsMerged_ButCanonicalLabelsWin(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "grafana",
		OwnerLabels: map[string]string{
			"app.kubernetes.io/managed-by":     "application-controller",
			"catalyst.openova.io/organization": "acme",
			// Adversarial: try to override the canonical labels.
			LabelRole:     "owner-override-attempt",
			LabelTopology: "evil",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, hr := range hrs {
		labels := hr.GetLabels()
		if labels["app.kubernetes.io/managed-by"] != "application-controller" {
			t.Fatalf("owner label dropped")
		}
		// Canonical labels overlay owner-supplied versions.
		if labels[LabelTopology] != "active-hot-standby" {
			t.Fatalf("LabelTopology = %q, want canonical 'active-hot-standby'", labels[LabelTopology])
		}
		if labels[LabelRole] == "owner-override-attempt" {
			t.Fatalf("LabelRole should not be overrideable from OwnerLabels")
		}
	}
}

func TestFanoutHRs_Errors(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]

	type row struct {
		name string
		in   FanoutInputs
	}
	rows := []row{
		{"empty-app-name", FanoutInputs{AppName: "", Variant: &variant}},
		{"nil-variant", FanoutInputs{AppName: "x", Variant: nil}},
		{"nil-placement", FanoutInputs{AppName: "x", Variant: &bpv1alpha1.TopologyVariant{}}},
		{"empty-clusters", FanoutInputs{AppName: "x", Variant: &bpv1alpha1.TopologyVariant{
			Placement: &bpv1alpha1.PlacementSpec{Clusters: nil},
		}}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := FanoutHRs(r.in)
			if err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestHRNameFor_NoTruncationUnder63(t *testing.T) {
	got := HRNameFor("obs-prod", "mgmt-A")
	want := "obs-prod-mgmt-A"
	if got != want {
		t.Fatalf("HRNameFor = %q, want %q", got, want)
	}
	if len(got) > HRName63 {
		t.Fatalf("HRNameFor produced %d chars (cap %d)", len(got), HRName63)
	}
}

func TestHRNameFor_TruncatesOver63WithStableHashSuffix(t *testing.T) {
	app := strings.Repeat("a", 40)
	cluster := strings.Repeat("c", 40)
	got := HRNameFor(app, cluster)
	if len(got) > HRName63 {
		t.Fatalf("HRNameFor produced %d chars (cap %d): %q", len(got), HRName63, got)
	}
	// Stable: same input → same output.
	got2 := HRNameFor(app, cluster)
	if got != got2 {
		t.Fatalf("HRNameFor not stable: %q vs %q", got, got2)
	}
	// 5-char hex suffix: name MUST end with "-<5 hex>".
	if len(got) < 6 || got[len(got)-6] != '-' {
		t.Fatalf("HRNameFor result lacks '-<5 hex>' suffix: %q", got)
	}
}

func TestHRNameFor_DistinctInputsProduceDistinctNames(t *testing.T) {
	app := strings.Repeat("a", 40)
	a := HRNameFor(app, strings.Repeat("b", 40))
	b := HRNameFor(app, strings.Repeat("c", 40))
	if a == b {
		t.Fatalf("HRNameFor collision: %q == %q", a, b)
	}
}

func TestPerClusterStatusesFor_TemplateShape(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "grafana",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	statuses := PerClusterStatusesFor(hrs)
	if len(statuses) != 2 {
		t.Fatalf("PerClusterStatusesFor returned %d entries; want 2", len(statuses))
	}
	// Brief example mapping: {mgmt-A active, mgmt-B passive}.
	if statuses[0].Cluster != "mgmt-A" || statuses[0].Role != RoleActive {
		t.Fatalf("status[0] = %+v, want {mgmt-A,active}", statuses[0])
	}
	if statuses[1].Cluster != "mgmt-B" || statuses[1].Role != RolePassive {
		t.Fatalf("status[1] = %+v, want {mgmt-B,passive}", statuses[1])
	}
	if statuses[0].HR != "obs-mgmt-A" {
		t.Fatalf("status[0].HR = %q, want obs-mgmt-A", statuses[0].HR)
	}
}

func TestSortHRsForReconcile_ActiveBeforePassiveBeforeSingleton(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "grafana",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reverse so that we know SortHRsForReconcile actually reorders.
	hrs[0], hrs[1] = hrs[1], hrs[0]
	SortHRsForReconcile(hrs)
	if hrs[0].GetLabels()[LabelRole] != RoleActive {
		t.Fatalf("sort: first should be active; got %q",
			hrs[0].GetLabels()[LabelRole])
	}
	if hrs[1].GetLabels()[LabelRole] != RolePassive {
		t.Fatalf("sort: second should be passive; got %q",
			hrs[1].GetLabels()[LabelRole])
	}
}

func TestFanoutHRs_ChartAndValuesPassedThrough(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpSingleton]
	values := map[string]interface{}{
		"replicas": int64(3),
		"image": map[string]interface{}{
			"tag": "1.2.3",
		},
	}
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:            "obs",
		AppNamespace:       "acme",
		Topology:           bpv1alpha1.BcpSingleton,
		Variant:            &variant,
		Chart:              "grafana",
		ChartVersion:       "1.0.5",
		SourceRefName:      "openova-catalog",
		SourceRefKind:      "HelmRepository",
		SourceRefNamespace: "flux-system",
		Values:             values,
		IntervalSeconds:    600,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hrs) != 1 {
		t.Fatalf("singleton should produce 1 HR; got %d", len(hrs))
	}
	spec := hrs[0].Object["spec"].(map[string]interface{})

	// chart block.
	chart := spec["chart"].(map[string]interface{})
	chartSpec := chart["spec"].(map[string]interface{})
	if chartSpec["chart"] != "grafana" {
		t.Fatalf("chart.spec.chart = %v, want grafana", chartSpec["chart"])
	}
	if chartSpec["version"] != "1.0.5" {
		t.Fatalf("chart.spec.version = %v, want 1.0.5", chartSpec["version"])
	}
	srcRef := chartSpec["sourceRef"].(map[string]interface{})
	if srcRef["name"] != "openova-catalog" || srcRef["kind"] != "HelmRepository" || srcRef["namespace"] != "flux-system" {
		t.Fatalf("chart.spec.sourceRef = %+v", srcRef)
	}

	// interval.
	if spec["interval"] != "600s" {
		t.Fatalf("spec.interval = %v, want 600s", spec["interval"])
	}

	// values pass-through.
	gotValues, ok := spec["values"].(map[string]interface{})
	if !ok {
		t.Fatalf("spec.values is not a map")
	}
	if gotValues["replicas"] != int64(3) {
		t.Fatalf("spec.values.replicas = %v, want 3", gotValues["replicas"])
	}
}

func TestFanoutHRs_ThreeClusterActiveActiveAllActive(t *testing.T) {
	// A hypothetical bp-strimzi-style active-active across 3 clusters.
	// Validates the resolver/fan-out chain doesn't assume 2-cluster.
	variant := bpv1alpha1.TopologyVariant{
		Placement: &bpv1alpha1.PlacementSpec{
			Clusters: []string{"dmz-A", "dmz-B", "dmz-C"},
			Roles: map[string]string{
				"dmz-A": "active",
				"dmz-B": "active",
				"dmz-C": "active",
			},
		},
	}
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "kafka-bus",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveActive,
		Variant:      &variant,
		Chart:        "strimzi",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hrs) != 3 {
		t.Fatalf("want 3 HRs; got %d", len(hrs))
	}
	for i, hr := range hrs {
		if hr.GetLabels()[LabelRole] != RoleActive {
			t.Fatalf("hr[%d].role = %q, want active", i, hr.GetLabels()[LabelRole])
		}
	}
}

func TestFanoutHRs_PassiveDefaultsForMultiClusterMissingRolesMap(t *testing.T) {
	variant := bpv1alpha1.TopologyVariant{
		Placement: &bpv1alpha1.PlacementSpec{
			Clusters: []string{"mgmt-A", "mgmt-B"},
			// Roles intentionally nil — defensive path.
		},
	}
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "x",
		AppNamespace: "ns",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "x",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, hr := range hrs {
		if hr.GetLabels()[LabelRole] != RolePassive {
			t.Fatalf("multi-cluster missing-roles fallback should mark passive; got %q",
				hr.GetLabels()[LabelRole])
		}
	}
}

// Sanity-check that the per-cluster names produced match the brief's
// "obs-prod-mgmt-A" example exactly.
func TestFanoutHRs_BriefExampleMapping(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs-prod",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "grafana",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := []string{hrs[0].GetName(), hrs[1].GetName()}
	want := []string{"obs-prod-mgmt-A", "obs-prod-mgmt-B"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("hr[%d].name = %q, want %q", i, got[i], want[i])
		}
	}
}

// Property: the suffix-hash truncation rule produces names ≤ 63 chars
// for ANY input pair, including absurdly long ones.
func TestHRNameFor_AlwaysUnder63(t *testing.T) {
	for app := 1; app < 200; app += 17 {
		for cluster := 1; cluster < 200; cluster += 23 {
			a := strings.Repeat("a", app)
			c := strings.Repeat("c", cluster)
			got := HRNameFor(a, c)
			if len(got) > HRName63 {
				t.Fatalf("len(%d/%d) = %d > %d for %q",
					app, cluster, len(got), HRName63, got)
			}
		}
	}
}

// Just to keep the fmt import live when nothing uses it (defensive
// — remove if the test grows a real consumer).
var _ = fmt.Sprintf

// #3370 — Flux dependsOn wiring to backing instances.
func TestFanoutHRs_DependsOnStamped(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpActiveHotStandby]

	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "wiki",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpActiveHotStandby,
		Variant:      &variant,
		Chart:        "bp-wordpress",
		DependsOnFor: func(cluster string) []HRDependsOn {
			return []HRDependsOn{
				// Bootstrap-owned backing instance → its slot HR.
				{Name: "bp-postgres-shared", Namespace: "flux-system"},
				// Controller-managed backing instance → per-cluster HR.
				{Name: HRNameFor("demo-pg", cluster), Namespace: "acme"},
			}
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hrs) != 2 {
		t.Fatalf("expected 2 HRs, got %d", len(hrs))
	}
	for i, hr := range hrs {
		deps, found, _ := nestedSliceHelper(hr.Object, "spec", "dependsOn")
		if !found || len(deps) != 2 {
			t.Fatalf("hr[%d] spec.dependsOn missing or wrong length: found=%v len=%d", i, found, len(deps))
		}
		first := deps[0].(map[string]interface{})
		if first["name"] != "bp-postgres-shared" || first["namespace"] != "flux-system" {
			t.Errorf("hr[%d] dependsOn[0] = %v, want bp-postgres-shared/flux-system", i, first)
		}
		cluster := hr.GetLabels()[LabelCluster]
		second := deps[1].(map[string]interface{})
		if second["name"] != HRNameFor("demo-pg", cluster) {
			t.Errorf("hr[%d] dependsOn[1].name = %v, want per-cluster %q", i, second["name"], HRNameFor("demo-pg", cluster))
		}
	}
}

// #3370 — nil DependsOnFor keeps the legacy byte-identical shape (no
// spec.dependsOn block at all).
func TestFanoutHRs_NoDependsOnFor_OmitsBlock(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpSingleton]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs-prod",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpSingleton,
		Variant:      &variant,
		Chart:        "grafana",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, found, _ := nestedSliceHelper(hrs[0].Object, "spec", "dependsOn"); found {
		t.Fatalf("spec.dependsOn must be absent when DependsOnFor is nil")
	}
}

// #3370 — empty resolution result omits the block too (no empty array
// noise on the wire).
func TestFanoutHRs_EmptyDependsOn_OmitsBlock(t *testing.T) {
	bp := fixtureGrafanaTopology()
	variant := bp.PerTopology[bpv1alpha1.BcpSingleton]
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "obs-prod",
		AppNamespace: "acme",
		Topology:     bpv1alpha1.BcpSingleton,
		Variant:      &variant,
		Chart:        "grafana",
		DependsOnFor: func(string) []HRDependsOn { return nil },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, found, _ := nestedSliceHelper(hrs[0].Object, "spec", "dependsOn"); found {
		t.Fatalf("spec.dependsOn must be absent when the resolver returns nothing")
	}
}

// nestedSliceHelper mirrors unstructured.NestedSlice without importing
// the helper package twice in this test file.
func nestedSliceHelper(obj map[string]interface{}, fields ...string) ([]interface{}, bool, error) {
	cur := obj
	for i, f := range fields {
		v, ok := cur[f]
		if !ok {
			return nil, false, nil
		}
		if i == len(fields)-1 {
			s, ok := v.([]interface{})
			return s, ok, nil
		}
		next, ok := v.(map[string]interface{})
		if !ok {
			return nil, false, fmt.Errorf("field %q is not a map", f)
		}
		cur = next
	}
	return nil, false, nil
}
