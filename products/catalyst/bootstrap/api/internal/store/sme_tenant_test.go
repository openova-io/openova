package store

import (
	"testing"
	"time"
)

func TestSMETenantProvisionStore_PutGetRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, err := NewSMETenantProvisionStore(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	rec := SMETenantProvisionRecord{
		SMETenantID:     "t-acme",
		State:           STSPending,
		Subdomain:       "acme",
		DomainMode:      SMEDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "sme-t-acme",
	}
	if err := st.Put(rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := st.Get("t-acme")
	if !ok {
		t.Fatalf("get: not found")
	}
	if got.Subdomain != "acme" || got.OTECHFQDN != "otech.example" {
		t.Errorf("roundtrip: got %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt unset")
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt unset")
	}
}

func TestSMETenantProvisionStore_PreservesCreatedAtOnUpsert(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewSMETenantProvisionStore(dir)
	rec := SMETenantProvisionRecord{SMETenantID: "t-1", State: STSPending}
	if err := st.Put(rec); err != nil {
		t.Fatalf("put1: %v", err)
	}
	first, _ := st.Get("t-1")
	time.Sleep(2 * time.Millisecond)
	rec.State = STSVClusterCreated
	if err := st.Put(rec); err != nil {
		t.Fatalf("put2: %v", err)
	}
	second, _ := st.Get("t-1")
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreatedAt should be preserved: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("UpdatedAt should advance")
	}
	if second.State != STSVClusterCreated {
		t.Errorf("State not updated: %v", second.State)
	}
}

func TestSMETenantProvisionStore_ListPending(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewSMETenantProvisionStore(dir)
	cases := []SMETenantProvisionRecord{
		{SMETenantID: "p1", State: STSPending},
		{SMETenantID: "p2", State: STSVClusterCreated},
		{SMETenantID: "p3", State: STSDone},
		{SMETenantID: "p4", State: STSFailed},
		{SMETenantID: "p5", State: STSDeleted},
		{SMETenantID: "p6", State: STSTenantRegistered},
	}
	for _, c := range cases {
		if err := st.Put(c); err != nil {
			t.Fatalf("put %s: %v", c.SMETenantID, err)
		}
	}
	pending := st.ListPending()
	if len(pending) != 3 {
		t.Errorf("pending count: want 3 got %d", len(pending))
	}
	for _, p := range pending {
		switch p.SMETenantID {
		case "p1", "p2", "p6":
		default:
			t.Errorf("unexpected pending: %s", p.SMETenantID)
		}
	}
}

func TestSMETenantProvisionStore_PutRequiresID(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewSMETenantProvisionStore(dir)
	if err := st.Put(SMETenantProvisionRecord{}); err == nil {
		t.Errorf("expected error for empty tenant id")
	}
}

func TestSMETenantProvisionStore_DeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewSMETenantProvisionStore(dir)
	if err := st.Delete("never-existed"); err != nil {
		t.Errorf("delete missing should be nil: %v", err)
	}
}
