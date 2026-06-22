// environment_ensure_test.go — issue #4077. Locks the contract that the
// organization-controller find-or-creates the parent Environment CR
// (`<slug>-<envType>`) so Applications installed into the Org resolve their
// Environment instead of wedging at Ready=False:EnvironmentMissing.
package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// envEnsureScheme registers core + orgapi + the unstructured Environment
// CR so the fake client can get/create it.
func envEnsureScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := orgapi.AddToScheme(s); err != nil {
		t.Fatalf("add orgapi scheme: %v", err)
	}
	s.AddKnownTypeWithName(environmentGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(environmentGVK.GroupVersion().WithKind(environmentGVK.Kind+"List"), &unstructured.UnstructuredList{})
	return s
}

func envEnsureOrg() *orgapi.Organization {
	return &orgapi.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name: "acme",
			UID:  "00000000-0000-0000-0000-0000000000aa",
		},
		Spec: orgapi.OrganizationSpec{
			Slug:                   "acme",
			DisplayName:            "ACME Corp",
			SovereignRef:           "omantel.omani.works",
			DefaultEnvironmentType: "prod",
		},
	}
}

func getEnv(t *testing.T, cl client.Client, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(environmentGVK)
	if err := cl.Get(context.Background(), client.ObjectKey{Name: name}, u); err != nil {
		t.Fatalf("get Environment %q: %v", name, err)
	}
	return u
}

// TestEnsureEnvironment_CreatesWhenMissing is the core #4077 contract:
// the controller creates `<slug>-prod` with a CRD-valid single region and
// the org/envType the Application controller resolves.
func TestEnsureEnvironment_CreatesWhenMissing(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(envEnsureScheme(t)).Build()
	r := &Reconciler{
		Client:      cl,
		Log:         logr.Discard(),
		HostCluster: "omantel.biz", // Huawei FQDN — not the 4-segment form
	}
	org := envEnsureOrg()

	if err := r.ensureEnvironment(context.Background(), org); err != nil {
		t.Fatalf("ensureEnvironment: %v", err)
	}

	env := getEnv(t, cl, "acme-prod")
	if got, _, _ := unstructured.NestedString(env.Object, "spec", "organizationRef"); got != "acme" {
		t.Errorf("spec.organizationRef = %q, want acme", got)
	}
	if got, _, _ := unstructured.NestedString(env.Object, "spec", "envType"); got != "prod" {
		t.Errorf("spec.envType = %q, want prod", got)
	}
	if got, _, _ := unstructured.NestedString(env.Object, "spec", "placement"); got != "single-region" {
		t.Errorf("spec.placement = %q, want single-region", got)
	}
	regions, _, _ := unstructured.NestedSlice(env.Object, "spec", "regions")
	if len(regions) != 1 {
		t.Fatalf("spec.regions length = %d, want 1", len(regions))
	}
	reg := regions[0].(map[string]any)
	// region must satisfy the CRD pattern ^[a-z]{3}[a-z0-9]?$.
	if rc, _ := reg["region"].(string); !envRegionCodePattern.MatchString(rc) {
		t.Errorf("spec.regions[0].region = %q does not match CRD pattern", rc)
	}
	if p, _ := reg["provider"].(string); !envProviderAllowed[p] {
		t.Errorf("spec.regions[0].provider = %q not in CRD enum", p)
	}
	if bb, _ := reg["buildingBlock"].(string); !envBuildingBlockAllowed[bb] {
		t.Errorf("spec.regions[0].buildingBlock = %q not in CRD enum", bb)
	}
	// The real host cluster must be bound via the explicit override on Huawei.
	if hc, _ := reg["hostCluster"].(string); hc != "omantel.biz" {
		t.Errorf("spec.regions[0].hostCluster = %q, want omantel.biz", hc)
	}
	// OwnerReference ties the Environment lifecycle to the Org.
	owners := env.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Kind != "Organization" || owners[0].Name != "acme" {
		t.Errorf("ownerReferences = %+v, want a single Organization/acme owner", owners)
	}
}

// TestEnsureEnvironment_Idempotent: a second call is a no-op and never
// clobbers an existing Environment (operator/funnel-authored regions are
// preserved).
func TestEnsureEnvironment_Idempotent(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(envEnsureScheme(t)).Build()
	r := &Reconciler{Client: cl, Log: logr.Discard(), HostCluster: "omantel.biz"}
	org := envEnsureOrg()

	if err := r.ensureEnvironment(context.Background(), org); err != nil {
		t.Fatalf("first ensureEnvironment: %v", err)
	}
	// Mutate the live Environment to simulate operator-authored regions.
	env := getEnv(t, cl, "acme-prod")
	_ = unstructured.SetNestedField(env.Object, "MARKER", "spec", "placement")
	if err := cl.Update(context.Background(), env); err != nil {
		t.Fatalf("update env: %v", err)
	}

	if err := r.ensureEnvironment(context.Background(), org); err != nil {
		t.Fatalf("second ensureEnvironment: %v", err)
	}
	env2 := getEnv(t, cl, "acme-prod")
	if got, _, _ := unstructured.NestedString(env2.Object, "spec", "placement"); got != "MARKER" {
		t.Errorf("second ensure clobbered an existing Environment: placement = %q, want preserved MARKER", got)
	}
}

// TestEnsureEnvironment_HostClusterParsedWhenCanonical: when HostCluster is
// the canonical {provider}-{region}-{bb}-{envType} form (Hetzner), those
// segments win over the configured defaults.
func TestEnsureEnvironment_HostClusterParsedWhenCanonical(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(envEnsureScheme(t)).Build()
	r := &Reconciler{
		Client:      cl,
		Log:         logr.Discard(),
		HostCluster: "hetzner-fsn-rtz-prod",
		// configured defaults should be OVERRIDDEN by the parsed host cluster
		EnvRegionProvider:      "huawei",
		EnvRegionCode:          "mee",
		EnvRegionBuildingBlock: "dmz",
	}
	org := envEnsureOrg()
	if err := r.ensureEnvironment(context.Background(), org); err != nil {
		t.Fatalf("ensureEnvironment: %v", err)
	}
	reg := getEnvRegion(t, cl, "acme-prod")
	if p, _ := reg["provider"].(string); p != "hetzner" {
		t.Errorf("provider = %q, want hetzner (parsed from host cluster)", p)
	}
	if rc, _ := reg["region"].(string); rc != "fsn" {
		t.Errorf("region = %q, want fsn (parsed from host cluster)", rc)
	}
	if bb, _ := reg["buildingBlock"].(string); bb != "rtz" {
		t.Errorf("buildingBlock = %q, want rtz (parsed from host cluster, not configured dmz)", bb)
	}
}

// TestEnsureEnvironment_ConfiguredDefaultsOnFQDNHostCluster: when
// HostCluster is the Sovereign FQDN (Huawei), the configured triple is used.
func TestEnsureEnvironment_ConfiguredDefaultsOnFQDNHostCluster(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(envEnsureScheme(t)).Build()
	r := &Reconciler{
		Client:                 cl,
		Log:                    logr.Discard(),
		HostCluster:            "omantel.biz",
		EnvRegionProvider:      "huawei",
		EnvRegionCode:          "meb",
		EnvRegionBuildingBlock: "rtz",
	}
	org := envEnsureOrg()
	if err := r.ensureEnvironment(context.Background(), org); err != nil {
		t.Fatalf("ensureEnvironment: %v", err)
	}
	reg := getEnvRegion(t, cl, "acme-prod")
	if p, _ := reg["provider"].(string); p != "huawei" {
		t.Errorf("provider = %q, want huawei", p)
	}
	if rc, _ := reg["region"].(string); rc != "meb" {
		t.Errorf("region = %q, want meb (configured)", rc)
	}
}

// TestEnsureEnvironment_FallsBackToProdEnvType: an empty/invalid
// defaultEnvironmentType yields the canonical `prod` env (matching the
// Application's environmentRef `<org>-prod` assumption).
func TestEnsureEnvironment_FallsBackToProdEnvType(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(envEnsureScheme(t)).Build()
	r := &Reconciler{Client: cl, Log: logr.Discard(), HostCluster: "omantel.biz"}
	org := envEnsureOrg()
	org.Spec.DefaultEnvironmentType = "" // invalid → prod

	if err := r.ensureEnvironment(context.Background(), org); err != nil {
		t.Fatalf("ensureEnvironment: %v", err)
	}
	// Name + envType must be `<slug>-prod` / prod.
	env := getEnv(t, cl, "acme-prod")
	if got, _, _ := unstructured.NestedString(env.Object, "spec", "envType"); got != "prod" {
		t.Errorf("spec.envType = %q, want prod fallback", got)
	}
	// And there must be NO `acme-` env under any other name.
	stray := &unstructured.Unstructured{}
	stray.SetGroupVersionKind(environmentGVK)
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "acme-"}, stray); !apierrors.IsNotFound(err) {
		t.Errorf("expected no stray `acme-` Environment, got err=%v", err)
	}
}

func getEnvRegion(t *testing.T, cl client.Client, name string) map[string]any {
	t.Helper()
	env := getEnv(t, cl, name)
	regions, _, _ := unstructured.NestedSlice(env.Object, "spec", "regions")
	if len(regions) != 1 {
		t.Fatalf("spec.regions length = %d, want 1", len(regions))
	}
	return regions[0].(map[string]any)
}
