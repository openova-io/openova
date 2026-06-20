package store

import (
	"testing"
	"time"
)

func TestUserProvisionStore_PutGetList(t *testing.T) {
	s, err := NewUserProvisionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewUserProvisionStore: %v", err)
	}

	rec := UserProvisionRecord{
		OrgUserUUID: "uuid-alice",
		OrganizationID: "tenant-acme",
		Email:       "alice@acme.example",
		State:       UPSPending,
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok := s.Get("tenant-acme", "uuid-alice")
	if !ok {
		t.Fatalf("Get: missing record")
	}
	if got.State != UPSPending {
		t.Errorf("State = %q, want %q", got.State, UPSPending)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not stamped: %+v", got)
	}

	// Upsert preserves CreatedAt; bumps UpdatedAt + state.
	createdAt := got.CreatedAt
	time.Sleep(2 * time.Millisecond)
	got.State = UPSKCCreated
	got.KCUserID = "kc-1"
	got.CreatedAt = time.Time{} // simulate caller forgetting it
	if err := s.Put(got); err != nil {
		t.Fatalf("Put upsert: %v", err)
	}
	got2, _ := s.Get("tenant-acme", "uuid-alice")
	if !got2.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt drifted: was %v, now %v", createdAt, got2.CreatedAt)
	}
	if !got2.UpdatedAt.After(createdAt) {
		t.Errorf("UpdatedAt did not advance")
	}
	if got2.State != UPSKCCreated || got2.KCUserID != "kc-1" {
		t.Errorf("state mutation lost: %+v", got2)
	}

	// Second user — different tenant.
	_ = s.Put(UserProvisionRecord{
		OrgUserUUID: "uuid-bob", OrganizationID: "tenant-beta", Email: "bob@beta.example",
	})

	// List scoped to tenant.
	acme := s.List("tenant-acme")
	if len(acme) != 1 || acme[0].Email != "alice@acme.example" {
		t.Errorf("List(acme) = %+v", acme)
	}
	beta := s.List("tenant-beta")
	if len(beta) != 1 || beta[0].Email != "bob@beta.example" {
		t.Errorf("List(beta) = %+v", beta)
	}
}

func TestUserProvisionStore_Delete(t *testing.T) {
	s, _ := NewUserProvisionStore(t.TempDir())
	_ = s.Put(UserProvisionRecord{OrgUserUUID: "u1", OrganizationID: "t1"})
	if err := s.Delete("t1", "u1"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if _, ok := s.Get("t1", "u1"); ok {
		t.Errorf("record still present after delete")
	}
	// Idempotent.
	if err := s.Delete("t1", "u1"); err != nil {
		t.Errorf("Delete idempotent: %v", err)
	}
}

func TestUserProvisionStore_RejectsBadInput(t *testing.T) {
	s, _ := NewUserProvisionStore(t.TempDir())
	if err := s.Put(UserProvisionRecord{}); err == nil {
		t.Errorf("expected error on empty input")
	}
}

func TestUserProvisionStore_PathTraversalSafe(t *testing.T) {
	s, _ := NewUserProvisionStore(t.TempDir())
	rec := UserProvisionRecord{
		OrgUserUUID: "../../etc/passwd",
		OrganizationID: "../../etc/shadow",
		Email:       "evil@example",
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Round-trip must work without escaping the directory.
	got, ok := s.Get("../../etc/shadow", "../../etc/passwd")
	if !ok {
		t.Fatalf("round-trip failed on sanitised IDs")
	}
	if got.Email != "evil@example" {
		t.Errorf("data mismatch: %+v", got)
	}
}
