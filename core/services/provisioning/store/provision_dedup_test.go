package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCreateProvisionIfAbsent_RejectsEmptyTenant proves the dedup guard refuses
// a blank TenantID BEFORE touching Mongo. This matters because the partial
// unique index that enforces the #3744 dedup is keyed on tenant_id filtered to
// in-flight statuses — a blank tenant_id would either collide spuriously across
// unrelated provisions or never collide at all, defeating the guarantee. The
// guard makes the precondition explicit instead of relying on index semantics.
func TestCreateProvisionIfAbsent_RejectsEmptyTenant(t *testing.T) {
	s := &Store{} // no db needed; the guard returns before any collection access
	err := s.CreateProvisionIfAbsent(context.Background(), &Provision{TenantID: ""})
	if err == nil {
		t.Fatal("CreateProvisionIfAbsent with empty TenantID must error, got nil")
	}
	// It must NOT be the duplicate-key sentinel — that would mislead the caller
	// into surfacing a non-existent in-flight provision.
	if errors.Is(err, ErrProvisionExists) {
		t.Fatalf("empty-tenant error must not be ErrProvisionExists, got %v", err)
	}
}

// TestInFlightProvisionStatuses_ExcludesTerminal locks down the exact set of
// statuses the dedup index and lookup treat as "a provision is already running
// for this tenant". A terminal state (completed/failed) MUST NOT appear here,
// otherwise a tenant could never be legitimately re-provisioned after a prior
// run finished — and a failed self-race (the #3744 symptom) would permanently
// wedge re-provisioning instead of self-healing.
func TestInFlightProvisionStatuses_ExcludesTerminal(t *testing.T) {
	want := map[string]bool{"pending": true, "provisioning": true, "running": true}
	got := map[string]bool{}
	for _, s := range inFlightProvisionStatuses {
		got[s] = true
	}
	if len(got) != len(want) {
		t.Fatalf("inFlightProvisionStatuses = %v, want exactly %v", inFlightProvisionStatuses, []string{"pending", "provisioning", "running"})
	}
	for s := range want {
		if !got[s] {
			t.Errorf("inFlightProvisionStatuses missing in-flight status %q", s)
		}
	}
	for _, terminal := range []string{"completed", "failed"} {
		if got[terminal] {
			t.Errorf("inFlightProvisionStatuses must NOT include terminal status %q (would block legitimate re-provision)", terminal)
		}
	}
}

// TestProvisionIsStale_BoundaryAndFreshness pins down the staleness predicate
// the #3898 reaper's `updated_at < now-staleAfter` Mongo filter mirrors. The
// two load-bearing properties:
//
//   - a row whose updated_at is OLDER than the threshold is stale (reapable) —
//     this is what lets a crash-/roll-stranded "provisioning" row release the
//     uniq_inflight_tenant index so the tenant can re-provision;
//   - a row that was JUST refreshed (or is exactly at the cutoff) is NOT stale,
//     so a healthy workflow — which stamps updated_at on every step — is never
//     reaped out from under itself.
func TestProvisionIsStale_BoundaryAndFreshness(t *testing.T) {
	now := time.Now().UTC()
	const staleAfter = 30 * time.Minute

	cases := []struct {
		name      string
		updatedAt time.Time
		want      bool
	}{
		{"freshly refreshed", now, false},
		{"one second old", now.Add(-1 * time.Second), false},
		{"just under threshold", now.Add(-(staleAfter - time.Minute)), false},
		{"exactly at cutoff is not stale", now.Add(-staleAfter), false},
		{"just over threshold", now.Add(-(staleAfter + time.Minute)), true},
		{"abandoned for hours", now.Add(-6 * time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provisionIsStale(tc.updatedAt, now, staleAfter); got != tc.want {
				t.Fatalf("provisionIsStale(updatedAt=%s ago) = %v, want %v",
					now.Sub(tc.updatedAt), got, tc.want)
			}
		})
	}
}

// TestReapStaleInFlightProvision_RequiresTenant proves the reaper refuses to run
// an unscoped UpdateMany. A blank tenant_id with the in-flight-status filter
// would otherwise reap EVERY tenant's stale provisions at once; the guard keeps
// the reap scoped to the one tenant whose re-provision triggered the collision.
func TestReapStaleInFlightProvision_RequiresTenant(t *testing.T) {
	s := &Store{} // no db — the guard must return before any collection access
	n, err := s.ReapStaleInFlightProvision(context.Background(), "", time.Minute)
	if err == nil {
		t.Fatal("ReapStaleInFlightProvision with empty tenantID must error, got nil")
	}
	if n != 0 {
		t.Fatalf("ReapStaleInFlightProvision with empty tenantID must reap 0, got %d", n)
	}
}
