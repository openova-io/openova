package openova

// A cancelled Organization's customer is CLOSED at the cancellation instant:
// metered and billed up to it, never after — even while the cluster still
// runs the Organization's pods during its removal.
//
// Measured on the two-region environment on 22 November 2026: the chargeback
// product held both cancelled customers active, plan s, billed, with usage
// records 17 hours after the cancellation. On main a CR marked for deletion
// was synced as a live Organization (customer active) and nothing capped the
// collector's windows.

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// endingOrg is the CR the organization-controller leaves behind when it ends a
// cancelled tenant's Organization: the ended-at annotation, then the deletion
// mark (a few seconds later), the finalizer still holding it while the
// controller removes it from every region.
func endingOrg(slug string, cancelled, marked time.Time) *unstructured.Unstructured {
	u := orgUnstructured(slug, nil)
	u.SetAnnotations(map[string]string{OrganizationEndedAtAnnotation: cancelled.UTC().Format(time.RFC3339)})
	ts := metav1.NewTime(marked)
	u.SetDeletionTimestamp(&ts)
	u.SetFinalizers([]string{"orgs.openova.io/tenant-networking"})
	return u
}

func usageAfter(recs []store.UsageRecord, t time.Time) []store.UsageRecord {
	var out []store.UsageRecord
	for _, r := range recs {
		if !r.WindowStart.Before(t) || r.WindowEnd.After(t) {
			out = append(out, r)
		}
	}
	return out
}

func planHours(t *testing.T, recs []store.UsageRecord) float64 {
	t.Helper()
	sum := 0.0
	for _, r := range recs {
		if r.ResourceKind != store.PlanKind {
			continue
		}
		q, err := strconv.ParseFloat(string(r.Quantity), 64)
		if err != nil {
			t.Fatal(err)
		}
		sum += q
	}
	return sum
}

func TestCancelledOrganization_ClosedAtTheCancellation_NothingMeteredAfter(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	cancelled := time.Date(2026, 11, 22, 1, 15, 45, 0, time.UTC)
	clock := cancelled.Add(-3 * time.Hour)
	now := func() time.Time { return clock }
	s := &OrgSync{Repo: repo, Metrics: metrics.New(), Now: now}
	pc := &PlatformCollector{Repo: repo, Metrics: metrics.New(), Now: now}

	// Two live Organizations, one whose name starts like the other's.
	for _, slug := range []string{"acme", "acme-2"} {
		if err := s.SyncOrganization(ctx, orgUnstructured(slug, nil)); err != nil {
			t.Fatal(err)
		}
		pc.ObserveNamespace(orgNamespace(slug))
		pc.ObservePod(testPod(slug, "web-0", "pod-"+slug, clock.Add(-time.Hour), "500m", "1Gi"))
	}

	// Metered while live, up to 90 minutes before the cancellation.
	clock = cancelled.Add(-90 * time.Minute)
	for _, slug := range []string{"acme", "acme-2"} {
		if _, err := pc.EmitOrg(ctx, slug); err != nil {
			t.Fatal(err)
		}
	}

	// The cancellation: the CR is stamped and marked for deletion.
	clock = cancelled.Add(5 * time.Second)
	cr := endingOrg("acme", cancelled, cancelled.Add(5*time.Second))
	if err := s.SyncOrganization(ctx, cr); err != nil { // the informer's update
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("acme")
	if c.EndedAt == nil || !c.EndedAt.Equal(cancelled) {
		t.Fatalf("customer ended_at = %v, want the cancellation instant %s", c.EndedAt, cancelled)
	}
	if c.Status == "active" {
		t.Fatal("the customer of a cancelled Organization is still active")
	}

	// 17 hours later its pods are still observed (the removal is slow): a
	// meter reading now must charge nothing after the cancellation.
	clock = cancelled.Add(17 * time.Hour)
	if _, err := pc.EmitOrg(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	src := repo.sourcesOf(c.ID)[0]
	recs := repo.usageRecords(src.ID)
	if late := usageAfter(recs, cancelled); len(late) != 0 {
		t.Fatalf("%d usage records reach past the cancellation, first %s %s..%s", len(late), late[0].SKU, late[0].WindowStart, late[0].WindowEnd)
	}
	// Billed UP TO the cancellation: the hour before it in full, and the
	// partial hour it fell in up to the instant (01:00:00..01:15:45 = 0.2625 h).
	var lastHour, partial []store.UsageRecord
	for _, r := range recs {
		switch r.WindowStart {
		case cancelled.Truncate(time.Hour).Add(-time.Hour):
			lastHour = append(lastHour, r)
		case cancelled.Truncate(time.Hour):
			partial = append(partial, r)
			if !r.WindowEnd.Equal(cancelled) {
				t.Errorf("%s window ends %s, want the cancellation %s", r.SKU, r.WindowEnd, cancelled)
			}
		}
	}
	if got := planHours(t, lastHour); math.Abs(got-1) > 1e-6 {
		t.Errorf("plan hours in the hour before the cancellation = %v, want 1", got)
	}
	if got := planHours(t, partial); math.Abs(got-0.2625) > 1e-6 {
		t.Errorf("plan hours from 01:00 to the cancellation at 01:15:45 = %v, want 0.2625", got)
	}

	// A second reading, the delete event and a resync change nothing.
	before := len(repo.usageRecords(src.ID))
	clock = cancelled.Add(18 * time.Hour)
	if n, err := pc.EmitOrg(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("second reading wrote %d records (err %v), want 0", n, err)
	}
	if err := s.EndOrganization(ctx, cr); err != nil { // the informer's delete
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, endingOrg("acme", cancelled.Add(time.Hour), cancelled.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if c, _ = repo.customerBySlug("acme"); c.EndedAt == nil || !c.EndedAt.Equal(cancelled) {
		t.Errorf("a repeated end moved ended_at to %v", c.EndedAt)
	}
	if got := len(repo.usageRecords(src.ID)); got != before {
		t.Errorf("records after the repeated end = %d, want %d", got, before)
	}

	// The other Organization is untouched: active and still metered.
	other, _ := repo.customerBySlug("acme-2")
	if other.Status != "active" || other.EndedAt != nil {
		t.Fatalf("another Organization's customer was closed: %+v", other)
	}
	if _, err := pc.EmitOrg(ctx, "acme-2"); err != nil {
		t.Fatal(err)
	}
	if late := usageAfter(repo.usageRecords(repo.sourcesOf(other.ID)[0].ID), cancelled); len(late) == 0 {
		t.Error("another Organization stopped being metered")
	}
}

func TestOrgEndedAt(t *testing.T) {
	cancelled := time.Date(2026, 11, 22, 1, 15, 45, 0, time.UTC)
	if _, ok := orgEndedAt(orgUnstructured("acme", nil)); ok {
		t.Fatal("a live Organization reads as ended")
	}
	if got, ok := orgEndedAt(endingOrg("acme", cancelled, cancelled.Add(5*time.Second))); !ok || !got.Equal(cancelled) {
		t.Fatalf("got %v %v, want the annotated cancellation %s", got, ok, cancelled)
	}
	// No annotation (the sovereign-admin door's delete): the deletion mark.
	u := orgUnstructured("acme", nil)
	ts := metav1.NewTime(cancelled)
	u.SetDeletionTimestamp(&ts)
	if got, ok := orgEndedAt(u); !ok || !got.Equal(cancelled) {
		t.Fatalf("got %v %v, want the deletionTimestamp", got, ok)
	}
	// An annotation after the deletion mark is not believed: never later.
	if got, _ := orgEndedAt(endingOrg("acme", cancelled.Add(time.Hour), cancelled)); !got.Equal(cancelled) {
		t.Fatalf("got %v, want the earlier deletionTimestamp", got)
	}
}

func TestReopenedSlugIsMeteredFromItsReturnOnly(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	cancelled := time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC)
	clock := cancelled
	s := &OrgSync{Repo: repo, Metrics: metrics.New(), Now: func() time.Time { return clock }}
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", nil)); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, endingOrg("acme", cancelled, cancelled)); err != nil {
		t.Fatal(err)
	}
	clock = cancelled.Add(48 * time.Hour)
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", nil)); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("acme")
	if c.Status != "active" || c.EndedAt != nil {
		t.Fatalf("a new Organization of the slug: status %s ended_at %v, want active and open", c.Status, c.EndedAt)
	}
	if got := repo.sourcesOf(c.ID)[0].LastCollectedAt; got == nil || !got.Equal(clock) {
		t.Fatalf("collection restarts at %v, want %v (the ended gap is never metered)", got, clock)
	}
}
