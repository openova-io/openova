package store

import (
	"context"
	"errors"
	"testing"
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
