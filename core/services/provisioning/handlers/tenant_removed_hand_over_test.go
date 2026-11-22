package handlers

// A cancelled marketplace tenant hands its Organization over to its end:
// after the Git prune, handleTenantDeleted publishes provision.tenant_removed
// with the instant the tenant ended, and the organization-controller deletes
// the Organization CR on it (core/controllers/organization/internal/
// controller/organization_end.go). A lost hand-over strands the Organization
// whole and billed, so a failed publish fails the event for redelivery.
//
// On main the event carried only {id, slug} and its publish error was
// dropped: both tests below fail there.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/shared/events"
)

// failingPublisher refuses every Publish (a broker outage).
type failingPublisher struct{ calls int }

func (f *failingPublisher) Publish(context.Context, string, *events.Event) error {
	f.calls++
	return errors.New("nats: no responders available for request")
}
func (f *failingPublisher) Close() {}

// teardownHandler runs handleTenantDeleted's Git side against an in-memory
// Gitea (giteaParentStub: the parent index is absent, every write succeeds).
func teardownHandler(t *testing.T, pub events.BrokerPublisher) *Handler {
	t.Helper()
	clearK8sEnv(t) // the funnel's own Kustomization deletes stay offline
	const base = "clusters/t99.omani.works/org-tenants"
	srv, _ := giteaParentStub(t, base+"/kustomization.yaml", "", http.StatusNotFound)
	return &Handler{
		Producer:      pub,
		Generator:     &gitops.ManifestGenerator{BasePath: base},
		GitHubClient:  ghclient.NewClientWithAPIURL("token", "openova", "openova", srv.URL),
		GitBasePath:   base,
		SovereignFQDN: "t99.omani.works",
	}
}

func tenantDeletedEvent(t *testing.T, id, slug string, at time.Time) *events.Event {
	t.Helper()
	ev, err := events.NewEvent("tenant.deleted", "tenant-service", id, map[string]string{"id": id, "slug": slug})
	if err != nil {
		t.Fatal(err)
	}
	ev.Timestamp = at
	return ev
}

func TestHandleTenantDeleted_HandsTheOrganizationOverWithTheCancellationInstant(t *testing.T) {
	pub := &recordingPublisher{}
	h := teardownHandler(t, pub)
	cancelled := time.Date(2026, 11, 22, 1, 15, 45, 0, time.UTC)
	if err := h.handleTenantDeleted(context.Background(), tenantDeletedEvent(t, "4ef578b9", "g01-cel26uza", cancelled)); err != nil {
		t.Fatalf("handleTenantDeleted: %v", err)
	}
	ev := pub.byType("provision.tenant_removed")
	if ev == nil {
		t.Fatal("no provision.tenant_removed: nothing ends the Organization")
	}
	var got map[string]string
	if err := json.Unmarshal(ev.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "4ef578b9" || got["slug"] != "g01-cel26uza" {
		t.Errorf("payload = %v, want the tenant's id and slug", got)
	}
	if got["ended_at"] != cancelled.Format(time.RFC3339) {
		t.Errorf("ended_at = %q, want the cancellation instant %s (the chargeback customer is closed there)", got["ended_at"], cancelled.Format(time.RFC3339))
	}
}

func TestHandleTenantDeleted_LostHandOverIsRedelivered(t *testing.T) {
	pub := &failingPublisher{}
	h := teardownHandler(t, pub)
	err := h.handleTenantDeleted(context.Background(), tenantDeletedEvent(t, "4ef578b9", "g01-cel26uza", time.Now()))
	if err == nil {
		t.Fatal("a failed provision.tenant_removed publish was acknowledged: the Organization would stay whole and billed")
	}
	if pub.calls == 0 {
		t.Fatal("the hand-over was never attempted")
	}
}
