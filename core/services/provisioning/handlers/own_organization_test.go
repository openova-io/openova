// own_organization_test.go — the paths keyed by the slug after the creation
// path (the order's timeline and the per-Org commit of the cart) act only on
// the order's OWN Organization, and the creation path's last delivery is never
// a silent drop.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/shared/events"
)

const ownTenantID = "tenant-own"

// ownOrganizationAPI answers GET organizations/<any> with an Organization
// labelled tenantID, and every other in-cluster request as off-cluster (what
// those tests saw before the ownership read existed).
func ownOrganizationAPI(tenantID string) func(method, path string, body []byte) ([]byte, error) {
	return func(method, path string, _ []byte) ([]byte, error) {
		if method == http.MethodGet && strings.HasPrefix(path, "/apis/orgs.openova.io/v1/organizations/") {
			return []byte(existingOrg(tenantID, false)), nil
		}
		return nil, fmt.Errorf("not running in cluster")
	}
}

func fastOwnOrganizationPoll(t *testing.T) {
	t.Helper()
	prev := ownOrganizationPollEvery
	ownOrganizationPollEvery = time.Millisecond
	t.Cleanup(func() { ownOrganizationPollEvery = prev })
}

// The order's timeline on a reused name: the previous customer's Organization
// is live. The wait ends at once with the permanent refusal, so the timeline
// fails the order (provision.failed) before the vCluster wait,
// provision.completed or the spec.tenantPublic PATCH, and the message names
// neither customer.
func TestAwaitOwnOrganization_AnotherTenantsOrganizationEndsTheWaitAtOnce(t *testing.T) {
	fastOwnOrganizationPoll(t)
	api := &fakeAPIServer{gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-previous", false)}}}
	h := &Handler{k8sDo: api.do}

	start := time.Now()
	err := h.awaitOwnOrganization("acme", "tenant-new", time.Hour)
	if !errors.Is(err, errOrganizationOfAnotherTenant) {
		t.Fatalf("the timeline accepted (or kept waiting on) the previous customer's Organization: err=%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("a permanent refusal waited %s", time.Since(start))
	}
	for _, r := range api.requests() {
		if r != "GET "+orgPathAcme {
			t.Fatalf("the wait touched more than the Organization it reads: %q", r)
		}
	}
	msg := ownOrganizationFailureMessage("acme", err)
	if !strings.Contains(msg, "another Organization") || strings.Contains(msg, "tenant-previous") || strings.Contains(msg, "tenant-new") {
		t.Fatalf("customer-facing failure %q must name the cause and no tenant id", msg)
	}
}

// The previous Organization is being removed, then the order's own appears:
// the timeline waits through it and proceeds.
func TestAwaitOwnOrganization_WaitsThroughATerminatingPredecessor(t *testing.T) {
	fastOwnOrganizationPoll(t)
	api := &fakeAPIServer{gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-previous", true)}}}
	var reads atomic.Int32
	h := &Handler{k8sDo: func(method, path string, body []byte) ([]byte, error) {
		if reads.Add(1) == 3 {
			api.setGet(orgPathAcme, fakeAPIResponse{body: existingOrg("tenant-new", false)})
		}
		return api.do(method, path, body)
	}}
	if err := h.awaitOwnOrganization("acme", "tenant-new", time.Minute); err != nil {
		t.Fatalf("the order's own Organization appeared after the predecessor went, want nil, got %v", err)
	}
}

// Nothing of this order ever appears: the wait ends with an error that is not
// the permanent refusal, after the timeout.
func TestAwaitOwnOrganization_TimesOutWhenNoOwnOrganizationAppears(t *testing.T) {
	fastOwnOrganizationPoll(t)
	api := &fakeAPIServer{}
	h := &Handler{k8sDo: api.do}
	err := h.awaitOwnOrganization("acme", "tenant-new", 20*time.Millisecond)
	if err == nil || errors.Is(err, errOrganizationOfAnotherTenant) || !errors.Is(err, errOrganizationNotYetOwn) {
		t.Fatalf("an absent Organization must time out as not-yet-own, got %v", err)
	}
	if len(api.requests()) < 2 {
		t.Fatalf("the wait read once and gave up: %v", api.requests())
	}
}

// perOrgCommitHandler is a Sovereign handler whose Gitea counts every request.
func perOrgCommitHandler(t *testing.T, k8s func(method, path string, body []byte) ([]byte, error)) (*Handler, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	gen := gitops.NewManifestGenerator("clusters/sov/org-tenants")
	gen.ParentDomain = "omani.homes"
	return &Handler{
		Generator:    gen,
		GitHubClient: ghclient.NewClientWithAPIURL("token", "openova", "openova", srv.URL),
		GitBranch:    "main",
		PerOrgGitops: true,
		k8sDo:        k8s,
	}, &hits
}

func acmeCartInstall() appChangeData {
	return appChangeData{TenantID: "tenant-new", TenantSlug: "acme", AppSlug: "wordpress", Apps: []string{"wordpress"}, PlanID: "s"}
}

// The cart of the new customer is never committed into the previous
// customer's `acme/catalyst-tenant`: refused permanently, before any request
// reaches Gitea.
func TestApplyTenantChangePerOrg_RefusesAnotherTenantsOrganization(t *testing.T) {
	api := &fakeAPIServer{gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-previous", false)}}}
	h, hits := perOrgCommitHandler(t, api.do)

	err := h.applyTenantChange(context.Background(), acmeCartInstall(), "install")
	if !errors.Is(err, errOrganizationOfAnotherTenant) {
		t.Fatalf("the cart was not refused for another customer's Organization: err=%v", err)
	}
	if isGiteaNotReadyError(err) {
		t.Fatalf("a permanent refusal is classified as the retryable Gitea race: it would be parked and retried forever")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the previous customer's repo", n)
	}

	// CONTROL — the order's own Organization: the commit path runs (Gitea is
	// reached), so the refusal above is the ownership check and nothing else.
	own := &fakeAPIServer{gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-new", false)}}}
	h2, hits2 := perOrgCommitHandler(t, own.do)
	_ = h2.applyTenantChange(context.Background(), acmeCartInstall(), "install")
	if hits2.Load() == 0 {
		t.Fatalf("control: the order's own Organization never reached Gitea")
	}
}

// A predecessor being removed (or no Organization yet) is the retryable race,
// so the install is retried and parked, never committed into the dying repo.
func TestApplyTenantChangePerOrg_TerminatingPredecessorIsRetried(t *testing.T) {
	api := &fakeAPIServer{gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-previous", true)}}}
	h, hits := perOrgCommitHandler(t, api.do)

	err := h.applyTenantChange(context.Background(), acmeCartInstall(), "install")
	if err == nil || !isGiteaNotReadyError(err) {
		t.Fatalf("a Terminating predecessor must be the retryable race, got %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the repo of an Organization being removed", n)
	}
}

func finalTenantCreated(t *testing.T, order tenantCreatedPayload) *events.Event {
	t.Helper()
	ev, err := events.NewEvent("tenant.created", "tenant-service", order.ID, order)
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	ev.Metadata[events.MetaFinalDelivery] = "true"
	return ev
}

// The last delivery with the name still held by an Organization being removed:
// reported and acked, never returned into JetStream's drop.
func TestHandleTenantCreated_LastDeliveryReportsATerminatingPredecessor(t *testing.T) {
	api := &fakeAPIServer{
		post: fakeAPIResponse{status: http.StatusConflict, body: alreadyExists},
		gets: map[string]fakeAPIResponse{orgPathAcme: {body: existingOrg("tenant-previous", true)}},
	}
	h, pub := newOwnerTestHandler(api)

	if err := h.handleTenantCreated(context.Background(), finalTenantCreated(t, acmeOrder("tenant-new"))); err != nil {
		t.Fatalf("the last delivery returned an error: JetStream drops it with nothing reported: %v", err)
	}
	if pub.byType("provision.org_create_failed") == nil {
		t.Fatalf("the last delivery dropped the order with no failure event")
	}
}

// The last delivery with neither the catalog nor the order naming the plan:
// the Organization is created at the logged "s" (the floor before the wait).
func TestHandleTenantCreated_LastDeliveryWithoutAPlanCreatesTheOrganization(t *testing.T) {
	cat := catalogPlans(t, http.StatusServiceUnavailable)
	api := &fakeAPIServer{post: fakeAPIResponse{status: http.StatusCreated, body: `{}`}}
	h, _ := newOwnerTestHandler(api)
	h.CatalogURL = cat.URL

	order := acmeOrder("tenant-new")
	order.PlanID = "6f1c2b9e-0000-4000-8000-00000000000l"
	if err := h.handleTenantCreated(context.Background(), finalTenantCreated(t, order)); err != nil {
		t.Fatalf("the last delivery returned an error: the paid order is dropped with no Organization: %v", err)
	}
	if got := postedPlanSlug(t, api); got != "s" {
		t.Fatalf("plan = %q, want the logged floor s", got)
	}
}
