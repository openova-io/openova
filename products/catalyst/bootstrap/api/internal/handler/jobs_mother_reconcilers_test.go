// jobs_mother_reconcilers_test.go — coverage for the mother-side in-cluster
// reconciler ingestion (#3896 / Refs #3646).
//
// On the mothership during phase1-watching the catalyst-api watches
// HelmReleases (install-* leaves) but never surfaced the raw in-cluster
// Jobs/CronJobs/Kustomizations the convergence spins up — invisible during
// the most useful window. motherSeedInClusterReconcilers closes that gap:
// it lists §5a reconciler observations via the deployment's reachable
// kubeconfig and projects them into the same jobs.Store the /jobs read
// returns. These tests prove (a) a running in-cluster Job surfaces as a
// task-* leaf on the mother, and (b) the chroot-mode short-circuit so the
// projection never double-runs on a Sovereign-side catalyst-api.
package handler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeJobDynamicClient builds a dynamic.Interface serving the given batch
// Jobs (each running, no owner) so ListReconcilerObservations surfaces them
// as standalone task-<name> leaves. Every reconciler GVR's List kind is
// registered (ListReconcilerObservations enumerates all four — an
// unregistered List kind panics the fake rather than returning the empty
// list production would see).
func fakeJobDynamicClient(t *testing.T, jobNames ...string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, lk := range []schema.GroupVersionKind{
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList"},
		{Group: "batch", Version: "v1", Kind: "CronJobList"},
		{Group: "batch", Version: "v1", Kind: "JobList"},
		{Group: "apps", Version: "v1", Kind: "DeploymentList"},
	} {
		scheme.AddKnownTypeWithName(lk, &unstructured.UnstructuredList{})
	}
	objs := make([]runtime.Object, 0, len(jobNames))
	for _, name := range jobNames {
		u := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "batch/v1",
				"kind":       "Job",
				"metadata": map[string]any{
					"name":      name,
					"namespace": "flux-system",
				},
				// No conditions ⇒ jobRunStatus defaults to "running".
				"status": map[string]any{},
			},
		}
		u.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "batch", Version: "v1", Kind: "Job",
		})
		objs = append(objs, u)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			helmwatch.KustomizationGVR: "KustomizationList",
			helmwatch.CronJobGVR:       "CronJobList",
			helmwatch.JobGVR:           "JobList",
			helmwatch.DeploymentGVR:    "DeploymentList",
		},
		objs...,
	)
}

// writeTempKubeconfig drops a placeholder kubeconfig file on disk so
// sovereignDynamicClient's os.ReadFile succeeds; the bytes are never parsed
// because the test injects h.dynamicFactory.
func writeTempKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	if err := os.WriteFile(path, []byte("placeholder: kubeconfig"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// TestMotherSeedInClusterReconcilers_SurfacesRunningJob is the end-to-end
// proof for #3896: a running in-cluster Job must surface as a task-* leaf
// in jobs.Store on the MOTHER (SOVEREIGN_FQDN unset) during
// phase1-watching, via the deployment's reachable kubeconfig.
func TestMotherSeedInClusterReconcilers_SurfacesRunningJob(t *testing.T) {
	// Mother mode — explicitly unset so a polluted env never flips us to
	// the chroot short-circuit.
	t.Setenv("SOVEREIGN_FQDN", "")

	const depID = "dep3896"
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	fake := fakeJobDynamicClient(t, "cnpg-pair-join", "openbao-init", "keycloak-realm-seed")
	h := &Handler{
		jobs: st,
		log:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		dynamicFactory: func(string) (dynamic.Interface, error) {
			return fake, nil
		},
	}
	dep := &Deployment{
		ID:     depID,
		Status: "phase1-watching",
		Request: provisioner.Request{
			SovereignFQDN: "hw170.omani.works",
			Regions:       []provisioner.RegionSpec{{Provider: "huawei"}},
		},
		Result: &provisioner.Result{KubeconfigPath: writeTempKubeconfig(t)},
	}

	h.motherSeedInClusterReconcilers(context.Background(), dep)

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	want := map[string]bool{
		jobs.TaskJobPrefix + "cnpg-pair-join":      false,
		jobs.TaskJobPrefix + "openbao-init":        false,
		jobs.TaskJobPrefix + "keycloak-realm-seed": false,
	}
	sawReconcilersGroup := false
	for _, j := range got {
		if j.JobName == jobs.GroupReconcilers {
			sawReconcilersGroup = true
		}
		if _, tracked := want[j.JobName]; tracked {
			want[j.JobName] = true
			if j.Status != jobs.StatusRunning {
				t.Errorf("task %q status = %q, want running", j.JobName, j.Status)
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("in-cluster Job %q never surfaced in jobs.Store on the mother", name)
		}
	}
	if !sawReconcilersGroup {
		t.Errorf("the Reconcilers parent group was never materialised")
	}
}

// TestMotherSeedInClusterReconcilers_ChrootShortCircuits proves the
// projection never double-runs on a Sovereign-side (chroot) catalyst-api:
// when SOVEREIGN_FQDN matches the deployment, motherSeedInClusterReconcilers
// is a no-op (chrootSeedReconcilerObservations already covers it).
func TestMotherSeedInClusterReconcilers_ChrootShortCircuits(t *testing.T) {
	const (
		depID         = "dep3896c"
		sovereignFQDN = "hw170.omani.works"
	)
	t.Setenv("SOVEREIGN_FQDN", sovereignFQDN)

	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// If the chroot short-circuit fails, this factory would be invoked and
	// seed task rows. We assert it is NEVER called.
	factoryCalled := false
	h := &Handler{
		jobs: st,
		log:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
		dynamicFactory: func(string) (dynamic.Interface, error) {
			factoryCalled = true
			return fakeJobDynamicClient(t, "should-not-surface"), nil
		},
	}
	dep := &Deployment{
		ID:      depID,
		Request: provisioner.Request{SovereignFQDN: sovereignFQDN},
		Result:  &provisioner.Result{KubeconfigPath: writeTempKubeconfig(t)},
	}

	h.motherSeedInClusterReconcilers(context.Background(), dep)

	if factoryCalled {
		t.Fatalf("chroot mode must short-circuit; dynamicFactory was invoked")
	}
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, j := range got {
		if j.JobName == jobs.TaskJobPrefix+"should-not-surface" {
			t.Fatalf("chroot mode seeded a task row via the mother path: %q", j.JobName)
		}
	}
}

// TestMotherSeedInClusterReconcilers_NoKubeconfigIsNoOp proves the early
// phase1-watching window (kubeconfig not posted back yet) is a graceful
// no-op rather than an error — the /jobs read just returns what it has and
// the next poll retries.
func TestMotherSeedInClusterReconcilers_NoKubeconfigIsNoOp(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	const depID = "dep3896nk"
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := &Handler{
		jobs: st,
		log:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
	dep := &Deployment{
		ID:      depID,
		Request: provisioner.Request{SovereignFQDN: "hw170.omani.works"},
		// No Result/KubeconfigPath — cloud-init still in flight.
	}

	// Must not panic; store stays empty.
	h.motherSeedInClusterReconcilers(context.Background(), dep)

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty store when kubeconfig unreachable, got %d jobs", len(got))
	}
}
