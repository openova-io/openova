package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// newJobNoID returns a Job with an empty ID so markJobStep/finalizeJob are
// safe no-ops in tests that don't wire a Store (they early-return on ID == "").
func newJobNoID() *store.Job {
	return &store.Job{
		TenantSlug: "s3376walk",
		Kind:       "install",
		AppSlug:    "wordpress",
		Steps: []store.JobStep{
			{Name: "Committing manifests to Git", Status: "running"},
			{Name: "Waiting for wordpress to be ready", Status: "pending"},
			{Name: "Installation complete", Status: "pending"},
		},
	}
}

// #4404 self-heal — the in-line commit retry budget covers the common
// fast-create case, but a per-Org Gitea repo whose creation latency exceeds even
// the widened budget would still drop the purchased app FOREVER (the failed
// attempt poisons the shared idempotency Job). The pending-install reconciler is
// the durable backstop: a parked install is re-attempted on a cadence until the
// repo finally exists and the commit lands — zero-touch.

// noopPublisher is a stub BrokerPublisher so the self-heal wait-tail goroutine
// (and any failure-path publishEvent) doesn't panic on a nil Producer in tests.
type noopPublisher struct{ published int32 }

func (n *noopPublisher) Publish(_ context.Context, _ string, _ *events.Event) error {
	atomic.AddInt32(&n.published, 1)
	return nil
}
func (n *noopPublisher) Close() {}

// giteaToggle is a fake per-Org Gitea whose repo "becomes ready" only after its
// ready flag is flipped — modelling the org-controller finishing the per-Org
// repo create well after the funnel cart dispatched the install.
type giteaToggle struct {
	ready    atomic.Bool
	refReads atomic.Int32
}

func newToggleGitea(t *testing.T) (*httptest.Server, *giteaToggle) {
	t.Helper()
	g := &giteaToggle{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/plans"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"s","slug":"s"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			g.refReads.Add(1)
			if !g.ready.Load() {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":["user redirect does not exist [name: s3376walk]"],"message":"GetUserByName"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tree":[],"truncated":false}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha0000000000000000000000000000"}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, g
}

func newHandlerForReconcileTest(t *testing.T, apiURL string) *Handler {
	t.Helper()
	gen := gitops.NewManifestGenerator("clusters/sov/org-tenants")
	gen.ParentDomain = "omani.homes"
	return &Handler{
		Generator:    gen,
		GitHubClient: ghclient.NewClientWithAPIURL("token", "s3376walk", "catalyst-tenant", apiURL),
		GitBranch:    "main",
		PerOrgGitops: true,
		CatalogURL:   apiURL,
		Producer:     &noopPublisher{},
	}
}

func parkedTestData() appChangeData {
	return appChangeData{
		TenantID:       "tid-1",
		TenantSlug:     "s3376walk",
		AppSlug:        "wordpress",
		AppID:          "app-wp",
		IdempotencyKey: "idem-4404",
		Apps:           []string{"wordpress"},
		PlanID:         "s",
	}
}

// TestPendingInstallReconciler_HealsOnceRepoReady_4404 is the core proof: a
// day-2 install parked while the per-Org repo did not exist is NOT dropped — the
// reconciler keeps it parked while the repo is missing, then commits it (and
// drains it) the moment the repo appears.
func TestPendingInstallReconciler_HealsOnceRepoReady_4404(t *testing.T) {
	srv, g := newToggleGitea(t)
	h := newHandlerForReconcileTest(t, srv.URL)

	data := parkedTestData()
	// Park it (job has empty ID so markJobStep/finalizeJob are safe no-ops).
	h.pendingInstalls.Enqueue(data, newJobNoID())

	// Repo still not ready → a reconcile pass keeps it parked, commits nothing.
	h.reconcilePendingInstalls(context.Background())
	if got := h.pendingInstalls.Len(); got != 1 {
		t.Fatalf("install should stay parked while repo is not ready, registry len=%d", got)
	}

	// org-controller finishes the per-Org repo create.
	g.ready.Store(true)

	// Next reconcile pass commits the parked install and drains it.
	h.reconcilePendingInstalls(context.Background())
	if got := h.pendingInstalls.Len(); got != 0 {
		t.Fatalf("install should be drained after the repo became ready, registry len=%d", got)
	}
	// The commit must have actually been attempted against the now-ready repo.
	if got := g.refReads.Load(); got < 2 {
		t.Errorf("expected the reconciler to re-read the branch ref across both passes, got %d", got)
	}
}

// TestPendingInstallReconciler_PermanentErrorFails_4404 proves a parked install
// that hits a genuinely permanent error on re-attempt is failed + drained
// (not retried forever).
func TestPendingInstallReconciler_PermanentErrorFails_4404(t *testing.T) {
	srv, g := newToggleGitea(t)
	h := newHandlerForReconcileTest(t, srv.URL)
	g.ready.Store(true) // repo exists, so the not-ready race is NOT the failure

	data := parkedTestData()
	data.TenantSlug = "" // malformed slug → permanent error in applyTenantChange
	h.pendingInstalls.Enqueue(data, newJobNoID())

	h.reconcilePendingInstalls(context.Background())
	if got := h.pendingInstalls.Len(); got != 0 {
		t.Fatalf("permanent-error install should be drained, registry len=%d", got)
	}
}

// TestPendingInstallReconciler_AgesOut_4404 proves a parked install whose repo
// NEVER appears is eventually given up (failed + drained) rather than retried
// forever.
func TestPendingInstallReconciler_AgesOut_4404(t *testing.T) {
	srv, _ := newToggleGitea(t) // ready stays false forever
	h := newHandlerForReconcileTest(t, srv.URL)

	origAge := pendingInstallMaxAge
	pendingInstallMaxAge = 1 * time.Millisecond
	t.Cleanup(func() { pendingInstallMaxAge = origAge })

	data := parkedTestData()
	h.pendingInstalls.Enqueue(data, newJobNoID())
	time.Sleep(2 * time.Millisecond) // exceed the max age

	h.reconcilePendingInstalls(context.Background())
	if got := h.pendingInstalls.Len(); got != 0 {
		t.Fatalf("aged-out install should be drained, registry len=%d", got)
	}
}

// TestPendingInstallReconciler_RemoveAllForTeardown_4404 proves a tenant.deleted
// drops the doomed Org's parked installs so the reconciler stops re-attempting
// against a repo that is being torn down.
func TestPendingInstallReconciler_RemoveAllForTeardown_4404(t *testing.T) {
	var reg pendingInstallRegistry
	reg.Enqueue(parkedTestData(), newJobNoID())
	other := parkedTestData()
	other.TenantSlug = "otherorg"
	other.IdempotencyKey = "idem-other"
	reg.Enqueue(other, newJobNoID())

	if got := reg.RemoveAllFor("s3376walk"); got != 1 {
		t.Errorf("RemoveAllFor should drop exactly the matching tenant, removed=%d", got)
	}
	if got := reg.Len(); got != 1 {
		t.Errorf("the other tenant's parked install must survive, registry len=%d", got)
	}
}

// TestPendingInstallRegistry_Coalesces_4404 proves a re-dispatch of the same
// install (same idempotency key) does not stack a second parked record.
func TestPendingInstallRegistry_Coalesces_4404(t *testing.T) {
	var reg pendingInstallRegistry
	reg.Enqueue(parkedTestData(), newJobNoID())
	reg.Enqueue(parkedTestData(), newJobNoID()) // same key
	if got := reg.Len(); got != 1 {
		t.Errorf("same-key re-dispatch should coalesce, registry len=%d", got)
	}
}
