package store

import (
	"testing"
	"time"
)

func TestOrganizationProvisionStore_PutGetRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, err := NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	rec := OrganizationProvisionRecord{
		OrganizationID:     "t-acme",
		State:           STSPending,
		Subdomain:       "acme",
		DomainMode:      OrganizationDomainFreeSubdomain,
		AdminEmail:      "admin@acme.test",
		CompanyName:     "Acme Corp",
		OTECHFQDN:       "otech.example",
		VClusterName:    "vc-acme",
		TenantNamespace: "org-t-acme",
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

func TestOrganizationProvisionStore_PreservesCreatedAtOnUpsert(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewOrganizationProvisionStore(dir)
	rec := OrganizationProvisionRecord{OrganizationID: "t-1", State: STSPending}
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

func TestOrganizationProvisionStore_ListPending(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewOrganizationProvisionStore(dir)
	cases := []OrganizationProvisionRecord{
		{OrganizationID: "p1", State: STSPending},
		{OrganizationID: "p2", State: STSVClusterCreated},
		{OrganizationID: "p3", State: STSDone},
		{OrganizationID: "p4", State: STSFailed},
		{OrganizationID: "p5", State: STSDeleted},
		{OrganizationID: "p6", State: STSTenantRegistered},
	}
	for _, c := range cases {
		if err := st.Put(c); err != nil {
			t.Fatalf("put %s: %v", c.OrganizationID, err)
		}
	}
	pending := st.ListPending()
	if len(pending) != 3 {
		t.Errorf("pending count: want 3 got %d", len(pending))
	}
	for _, p := range pending {
		switch p.OrganizationID {
		case "p1", "p2", "p6":
		default:
			t.Errorf("unexpected pending: %s", p.OrganizationID)
		}
	}
}

func TestOrganizationProvisionStore_PutRequiresID(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewOrganizationProvisionStore(dir)
	if err := st.Put(OrganizationProvisionRecord{}); err == nil {
		t.Errorf("expected error for empty tenant id")
	}
}

func TestOrganizationProvisionStore_DeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewOrganizationProvisionStore(dir)
	if err := st.Delete("never-existed"); err != nil {
		t.Errorf("delete missing should be nil: %v", err)
	}
}
