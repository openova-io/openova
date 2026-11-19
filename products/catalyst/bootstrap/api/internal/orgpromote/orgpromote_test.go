package orgpromote

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func orgCR(name string, deleting bool) unstructured.Unstructured {
	var u unstructured.Unstructured
	u.SetAPIVersion("orgs.openova.io/v1")
	u.SetKind("Organization")
	u.SetName(name)
	if deleting {
		now := metav1.Now()
		u.SetDeletionTimestamp(&now)
	}
	return u
}

func rec(id, subdomain string, state store.OrganizationProvisionState) store.OrganizationProvisionRecord {
	return store.OrganizationProvisionRecord{OrganizationID: id, Subdomain: subdomain, State: state}
}

func ids(recs []store.OrganizationProvisionRecord) []string {
	out := []string{}
	for _, r := range recs {
		out = append(out, r.OrganizationID)
	}
	return out
}

// Only a record held at tenant_registered whose CR exists and is not being
// deleted is due; every other case is refused, each for its own reason.
func TestDue(t *testing.T) {
	pending := []store.OrganizationProvisionRecord{
		rec("held-live", "acme", store.STSTenantRegistered),
		rec("held-mixed-case", " Beta ", store.STSTenantRegistered),
		rec("held-cr-gone", "gone", store.STSTenantRegistered),
		rec("held-cr-deleting", "leaving", store.STSTenantRegistered),
		rec("earlier-step", "early", store.STSKeycloakClientsProvisioned),
		rec("pending", "fresh", store.STSPending),
	}
	listed := []unstructured.Unstructured{
		orgCR("acme", false),
		orgCR("beta", false),
		orgCR("leaving", true),
		orgCR("early", false),
		orgCR("fresh", false),
	}
	got := ids(Due(pending, listed))
	want := []string{"held-live", "held-mixed-case"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Due = %v, want %v", got, want)
	}
	if got := Due(pending, nil); len(got) != 0 {
		t.Fatalf("with no CR listed nothing is due, got %v", ids(got))
	}
}

// Run runs exactly the due records and counts those that reached done.
func TestRun(t *testing.T) {
	pending := []store.OrganizationProvisionRecord{
		rec("a", "a-org", store.STSTenantRegistered),
		rec("b", "b-org", store.STSTenantRegistered),
		rec("c", "c-org", store.STSTenantRegistered),
	}
	listed := []unstructured.Unstructured{orgCR("a-org", false), orgCR("b-org", false)}
	unchanged := func(id string) (store.OrganizationProvisionRecord, bool) {
		for _, r := range pending {
			if r.OrganizationID == id {
				return r, true
			}
		}
		return store.OrganizationProvisionRecord{}, false
	}
	var ran []string
	done := Run(context.Background(), pending, listed, unchanged, func(_ context.Context, r store.OrganizationProvisionRecord) store.OrganizationProvisionRecord {
		ran = append(ran, r.OrganizationID)
		if r.OrganizationID == "a" {
			r.State = store.STSDone
		}
		return r
	})
	if !reflect.DeepEqual(ran, []string{"a", "b"}) || done != 1 {
		t.Fatalf("ran %v done %d, want [a b] and 1", ran, done)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran = nil
	if n := Run(ctx, pending, listed, unchanged, func(_ context.Context, r store.OrganizationProvisionRecord) store.OrganizationProvisionRecord {
		ran = append(ran, r.OrganizationID)
		return r
	}); n != 0 || len(ran) != 0 {
		t.Fatalf("an ended context runs nothing, ran %v", ran)
	}
}

// A record that was deleted, finished or removed after the pass read its list
// is skipped: Run reads each record again just before running it and runs only
// a record still held at tenant_registered, handing run that fresh copy.
func TestRun_RereadsEachRecordBeforeRunningIt(t *testing.T) {
	pending := []store.OrganizationProvisionRecord{
		rec("deleted", "deleted-org", store.STSTenantRegistered),
		rec("finished", "finished-org", store.STSTenantRegistered),
		rec("removed", "removed-org", store.STSTenantRegistered),
		rec("held", "held-org", store.STSTenantRegistered),
	}
	listed := []unstructured.Unstructured{
		orgCR("deleted-org", false), orgCR("finished-org", false),
		orgCR("removed-org", false), orgCR("held-org", false),
	}
	now := map[string]store.OrganizationProvisionRecord{
		"deleted":  rec("deleted", "deleted-org", store.STSDeleted),
		"finished": rec("finished", "finished-org", store.STSDone),
		"held":     {OrganizationID: "held", Subdomain: "held-org", State: store.STSTenantRegistered, BoundaryPhase: "Provisioning"},
	}
	current := func(id string) (store.OrganizationProvisionRecord, bool) {
		r, ok := now[id]
		return r, ok
	}
	var ran []store.OrganizationProvisionRecord
	Run(context.Background(), pending, listed, current, func(_ context.Context, r store.OrganizationProvisionRecord) store.OrganizationProvisionRecord {
		ran = append(ran, r)
		return r
	})
	if got := ids(ran); !reflect.DeepEqual(got, []string{"held"}) {
		t.Fatalf("ran %v, want only [held]", got)
	}
	if ran[0].BoundaryPhase != "Provisioning" {
		t.Fatalf("run must get the record as read just now, got boundary %q", ran[0].BoundaryPhase)
	}
}
