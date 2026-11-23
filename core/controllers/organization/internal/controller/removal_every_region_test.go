package controller

// An ended Organization is removed in EVERY region, reports what it could not
// remove in the named condition Removed=False/RemovalIncomplete, is safe to
// run twice, and never removes another Organization's objects.
//
// On main the deletion branch only logged a failing step: the assertion on
// the Removed condition fails there.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// flakyRegionGit is the in-memory region Git with one path that refuses to
// be deleted (an unreachable region's Gitea) and a count of real removals.
type flakyRegionGit struct {
	*fakeRegionGit
	mu       sync.Mutex
	failPath string
	removed  int
}

func (f *flakyRegionGit) DeleteFile(ctx context.Context, org, repo, branch, path, msg string) (bool, error) {
	f.mu.Lock()
	fail := f.failPath != "" && path == f.failPath
	f.mu.Unlock()
	if fail {
		return false, errors.New("gitea: HTTP 503: service unavailable")
	}
	if _, ok := f.get(org + "/" + repo + "/" + path); !ok {
		return false, nil // absent: nothing to change
	}
	f.mu.Lock()
	f.removed++
	f.mu.Unlock()
	return f.fakeRegionGit.DeleteFile(ctx, org, repo, branch, path, msg)
}

func (f *flakyRegionGit) removals() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed
}

func TestDelete_RemovesTheOrganizationInEveryRegion_ReportsWhatItCouldNot(t *testing.T) {
	ctx := context.Background()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)
	driveToDeletion(t, r)

	// The per-region files a previous reconcile committed: the deleted
	// Organization and another one whose name starts the same way.
	fg := &flakyRegionGit{fakeRegionGit: newFakeRegionGit()}
	r.GiteaInClusterURL = "http://gitea-http.gitea.svc.cluster.local:3000"
	r.FluxGiteaSecretRef = "openova-org-tenants-git-auth"
	r.Regions = testRegions
	r.RegionGit = fg
	sibling := sampleOrg()
	sibling.Name, sibling.Spec.Slug = "acme-2", "acme-2"
	for _, o := range []*orgapi.Organization{sampleOrg(), sibling} {
		if err := r.reconcilePerOrgFlux(ctx, o); err != nil {
			t.Fatalf("seed %s: %v", o.Name, err)
		}
	}
	fqdn := org.Spec.SovereignRef
	file := func(slug, region string) string {
		return "openova/sovereign-regions/" + perOrgRegionFilePath(fqdn, region, slug)
	}
	for _, region := range []string{testRegionA, testRegionB} {
		for _, slug := range []string{"acme", "acme-2"} {
			if _, ok := fg.get(file(slug, region)); !ok {
				t.Fatalf("precondition: no file of %s in %s", slug, region)
			}
		}
	}

	// Region b's Gitea refuses: the removal is incomplete and SAYS so.
	fg.failPath = perOrgRegionFilePath(fqdn, testRegionB, "acme")
	if _, err := r.Reconcile(ctx, deleteReq); err != nil {
		t.Fatalf("delete reconcile: %v", err)
	}
	var held orgapi.Organization
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &held); err != nil {
		t.Fatalf("the CR must stay while its removal is incomplete: %v", err)
	}
	var cond *orgapi.Condition
	for i := range held.Status.Conditions {
		if held.Status.Conditions[i].Type == OrganizationRemovedCondition {
			cond = &held.Status.Conditions[i]
		}
	}
	if cond == nil {
		t.Fatalf("no %s condition names what could not be removed; conditions=%+v", OrganizationRemovedCondition, held.Status.Conditions)
	}
	if cond.Status != "False" || cond.Reason != RemovalIncompleteReason {
		t.Errorf("condition = %s/%s, want False/%s", cond.Status, cond.Reason, RemovalIncompleteReason)
	}
	if !strings.Contains(cond.Message, testRegionB) || !strings.Contains(cond.Message, "503") {
		t.Errorf("condition message %q must name the region and the error", cond.Message)
	}

	// Region b is back: the next passes finish and the CR tombstones.
	fg.mu.Lock()
	fg.failPath = ""
	fg.mu.Unlock()
	for i := 0; i < 6; i++ {
		if _, err := r.Reconcile(ctx, deleteReq); err != nil {
			t.Fatalf("delete reconcile pass %d: %v", i+2, err)
		}
		if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &held); apierrors.IsNotFound(err) {
			break
		}
	}
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &held); !apierrors.IsNotFound(err) {
		t.Fatalf("the CR must tombstone once every region is clean, get err = %v", err)
	}
	for _, region := range []string{testRegionA, testRegionB} {
		if _, ok := fg.get(file("acme", region)); ok {
			t.Errorf("region %s still holds the ended Organization's Flux objects", region)
		}
		if _, ok := fg.get(file("acme-2", region)); !ok {
			t.Errorf("region %s lost the Flux objects of acme-2, another Organization", region)
		}
	}

	// A second run of the removal changes nothing.
	before := fg.removals()
	if err := r.teardownPerOrgFlux(ctx, sampleOrg()); err != nil {
		t.Fatalf("second removal: %v", err)
	}
	if _, err := r.Reconcile(ctx, deleteReq); err != nil {
		t.Fatalf("reconcile after tombstone: %v", err)
	}
	if fg.removals() != before {
		t.Errorf("second run removed %d more files", fg.removals()-before)
	}
}

func TestReportRemovalIncomplete_OncePerStep(t *testing.T) {
	ctx := context.Background()
	org := sampleOrg()
	r, _, _ := makeReconciler(t, org)
	var live orgapi.Organization
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &live); err != nil {
		t.Fatal(err)
	}
	r.reportRemovalIncomplete(ctx, &live, "step one", errors.New("first error"))
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &live); err != nil {
		t.Fatal(err)
	}
	rv := live.ResourceVersion
	// The same step failing with a different error text is not rewritten
	// (the controller watches its own status: a rewrite per attempt loops).
	r.reportRemovalIncomplete(ctx, &live, "step one", errors.New("second error, different text"))
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &live); err != nil {
		t.Fatal(err)
	}
	if live.ResourceVersion != rv {
		t.Error("the same failing step rewrote the status")
	}
	// A different step replaces the message.
	r.reportRemovalIncomplete(ctx, &live, "step two", errors.New("boom"))
	if err := r.Get(ctx, client.ObjectKey{Name: "acme"}, &live); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range live.Status.Conditions {
		if c.Type == OrganizationRemovedCondition {
			n++
			if !strings.HasPrefix(c.Message, removalStepPrefix("step two")) {
				t.Errorf("message = %q, want the step that fails now", c.Message)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d %s conditions, want exactly 1", n, OrganizationRemovedCondition)
	}
}

// The per-Org Flux names are derived from the slug and collide across
// Organizations: catalyst-tenant-b-host-apps is BOTH the apps Kustomization
// of "b-host" and the host-apps Kustomization of "b". Removing "b-host" must
// leave b's object — a prune:true Kustomization deleted takes everything it
// applied with it. On main the retire loop deleted it by name.
func TestTeardownPerOrgFlux_LeavesAnotherOrganizationsSameNamedObject(t *testing.T) {
	ctx := context.Background()
	kust := func(name, org string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(fluxKustomizationGVK)
		u.SetNamespace("flux-system")
		u.SetName(name)
		u.SetLabels(map[string]string{OrganizationLabel: org})
		return u
	}
	others := kust(perOrgHostAppsKustomizationName("b"), "b")
	_, ownName := perOrgFluxNames("b-host")
	own := kust(ownName, "b-host")
	if others.GetName() != perOrgAppsKustomizationName("b-host") {
		t.Fatalf("precondition: %q and %q should collide", others.GetName(), perOrgAppsKustomizationName("b-host"))
	}
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).WithObjects(others, own).Build()
	r := &Reconciler{
		Log:               logr.Discard(),
		GiteaInClusterURL: "http://gitea-http.gitea.svc.cluster.local:3000",
		Regions:           testRegions,
		RegionGit:         newFakeRegionGit(),
	}
	r.Client = cl
	if err := r.teardownPerOrgFlux(ctx, newTestOrg("b-host")); err != nil {
		t.Fatal(err)
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(fluxKustomizationGVK)
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "flux-system", Name: others.GetName()}, got); err != nil {
		t.Fatalf("Organization b lost its %s when b-host was removed: %v", others.GetName(), err)
	}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "flux-system", Name: ownName}, got); !apierrors.IsNotFound(err) {
		t.Fatalf("b-host's own %s survived its removal: %v", ownName, err)
	}
}
