// wipe_org_dns_test.go — the wipe releases the record sets of the Sovereign's
// Organizations (internal/orgrecords). Drives the real async purge through the
// stub provider of wipe_workdir_retention_5193_test.go, reads the
// Organizations through a fake dynamic client (the live source) and deletes
// through a fake PowerDNS server holding the pool zone.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/core/controllers/pkg/orgdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

const orgPoolZone = "omani.homes."

// fakeOrgPDNS is a PowerDNS server holding the pool zone. It applies DELETE
// rrsets to the zone, records every PATCH, and refuses a PATCH that names a
// record set in refuse.
type fakeOrgPDNS struct {
	mu      sync.Mutex
	zone    map[string]bool // record sets of orgPoolZone
	refuse  map[string]bool
	patches map[string][]string // zone -> every rrset name PATCHed into it
}

func newFakeOrgPDNS(t *testing.T, names []string, refuse ...string) (*fakeOrgPDNS, *httptest.Server) {
	t.Helper()
	f := &fakeOrgPDNS{zone: map[string]bool{}, refuse: map[string]bool{}, patches: map[string][]string{}}
	for _, n := range names {
		f.zone[n] = true
	}
	for _, n := range refuse {
		f.refuse[n] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zone := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			RRSets []powerdns.RRSet `json:"rrsets"`
		}
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, rs := range body.RRSets {
			f.patches[zone] = append(f.patches[zone], rs.Name)
		}
		for _, rs := range body.RRSets {
			if f.refuse[rs.Name] {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":"refused by the test"}`))
				return
			}
		}
		if zone == orgPoolZone {
			for _, rs := range body.RRSets {
				if rs.ChangeType == "DELETE" {
					delete(f.zone, rs.Name)
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeOrgPDNS) patchedNames(zone string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.patches[zone])
}

func (f *fakeOrgPDNS) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.zone[name]
}

func orgObject(name, parent, subdomain string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"slug":         name,
			"kind":         "customer",
			"sovereignRef": "hw-orgdns.omani.works",
			"tenantPublic": map[string]any{"parentDomain": parent, "subdomain": subdomain},
		},
	}}
}

// runOrgDNSWipe wipes deployment id whose live cluster holds orgs (or whose
// read fails with listErr) against the fake PowerDNS at srv.
func runOrgDNSWipe(t *testing.T, id string, srv *httptest.Server, listErr error, orgs ...*unstructured.Unstructured) *wipeResponse {
	t.Helper()
	t.Setenv("CATALYST_TOFU_WORKDIR", t.TempDir())
	setStub5193Result(&providers.WipeResult{TofuDestroyed: true, VerifiedZeroOrphans: true, ProviderPurge: map[string][]string{}})
	t.Cleanup(func() { setStub5193Result(nil) })

	// The live source: the parent's k8scache, holding the deployment's
	// cluster under its id with a client whose Organizations are orgs.
	objs := make([]runtime.Object, 0, len(orgs))
	for _, o := range orgs {
		objs = append(objs, o)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{OrganizationGVR(): "OrganizationList"}, objs...)
	if listErr != nil {
		dyn.PrependReactor("list", "organizations", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, listErr
		})
	}
	cache, err := k8scache.NewFactory(k8scache.Config{
		Logger:   silentLogger(),
		Registry: k8scache.NewRegistry(),
		Clusters: []k8scache.ClusterRef{{ID: id, DynamicClient: dyn, CoreClient: kfake.NewSimpleClientset()}},
	})
	if err != nil {
		t.Fatalf("k8scache.NewFactory: %v", err)
	}
	t.Cleanup(cache.Stop)

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.k8sCache = cache
	h.SetPowerDNSZoneClient(powerdns.New(srv.URL, "pool-key", "localhost"))
	dep := &Deployment{
		ID:        id,
		Status:    "failed",
		StartedAt: time.Now().Add(-45 * time.Minute),
		Request: provisioner.Request{
			SovereignFQDN:       "hw-orgdns.omani.works",
			SovereignDomainMode: "byo",
			Regions:             []provisioner.RegionSpec{{Provider: stub5193ProviderName, CloudRegion: "test"}},
		},
	}
	h.deployments.Store(dep.ID, dep)
	if w := callWipeDeployment(h, dep.ID, "", `{"hetznerToken":"fake-token"}`); w.Code != http.StatusAccepted {
		t.Fatalf("wipe status=%d, want 202 — body=%s", w.Code, w.Body.String())
	}
	waitForWipeDone(t, dep, 60*time.Second)
	dep.mu.Lock()
	defer dep.mu.Unlock()
	return dep.lastWipeReport
}

func sharedNames(orgs ...orgdns.Org) []string {
	var out []string
	for _, o := range orgs {
		out = append(out, orgdns.RecordSetNames(o)...)
	}
	return out
}

var (
	orgAlpha = orgdns.Org{Slug: "alpha", Kind: "customer", SovereignRef: "hw-orgdns.omani.works", ParentDomain: "omani.homes", Subdomain: "alpha"}
	orgBeta  = orgdns.Org{Slug: "beta", Kind: "customer", SovereignRef: "hw-orgdns.omani.works", ParentDomain: "omani.homes", Subdomain: "beta"}
	// orgOther is an Organization of ANOTHER deployment in the same zone.
	orgOther = orgdns.Org{Slug: "other", ParentDomain: "omani.homes"}
)

func TestWipe_ReleasesTheOrganizationRecordSets(t *testing.T) {
	ours := sharedNames(orgAlpha, orgBeta)
	theirs := sharedNames(orgOther)
	if len(ours) != 4 || len(theirs) != 2 {
		t.Fatalf("vacuity: ours=%q theirs=%q", ours, theirs)
	}
	fake, srv := newFakeOrgPDNS(t, append(slices.Clone(ours), theirs...))
	report := runOrgDNSWipe(t, "dep-orgdns-two", srv, nil,
		orgObject("alpha", "omani.homes", "alpha"), orgObject("beta", "omani.homes", ""))

	// Exactly their four record sets were deleted, by the shared names.
	deleted := fake.patchedNames(orgPoolZone)
	slices.Sort(deleted)
	deleted = slices.Compact(deleted)
	want := slices.Clone(ours)
	slices.Sort(want)
	if !slices.Equal(deleted, want) {
		t.Fatalf("record sets deleted from %s = %q, want exactly %q", orgPoolZone, deleted, want)
	}
	for _, n := range ours {
		if fake.has(n) {
			t.Errorf("record set %s is still in the zone after the wipe", n)
		}
	}
	// The other deployment's record sets are untouched.
	for _, n := range theirs {
		if !fake.has(n) {
			t.Errorf("record set %s of ANOTHER deployment was deleted", n)
		}
	}
	// The result lists every record set released, by name.
	got := report.OrgRecordSets
	if got == nil {
		t.Fatalf("the wipe result has no orgRecordSets")
	}
	if !slices.Equal(got.Released, ours) {
		t.Errorf("released = %q, want %q", got.Released, ours)
	}
	if len(got.Left) != 0 || got.NotEnumerated != "" {
		t.Errorf("left=%v notEnumerated=%q, want none", got.Left, got.NotEnumerated)
	}
}

func TestWipe_OrganizationsNotReadableIsNamedAndDeletesNothing(t *testing.T) {
	ours := sharedNames(orgAlpha)
	fake, srv := newFakeOrgPDNS(t, ours)
	report := runOrgDNSWipe(t, "dep-orgdns-unreadable", srv, errors.New("apiserver unreachable"),
		orgObject("alpha", "omani.homes", "alpha"))

	if got := fake.patchedNames(orgPoolZone); len(got) != 0 {
		t.Fatalf("a deletion was attempted in %s while the Organizations could not be read: %q", orgPoolZone, got)
	}
	got := report.OrgRecordSets
	if got == nil || !strings.Contains(got.NotEnumerated, "apiserver unreachable") {
		t.Fatalf("orgRecordSets = %+v, want notEnumerated with the reason", got)
	}
	if len(got.Released) != 0 {
		t.Errorf("released = %q, want none", got.Released)
	}
	if !slices.ContainsFunc(report.Errors, func(e string) bool {
		return strings.HasPrefix(e, "organization record sets not enumerated, they may be left")
	}) {
		t.Errorf("Errors = %q, want the named not-enumerated entry", report.Errors)
	}
}

func TestWipe_RefusedOrganizationRecordSetIsNamed(t *testing.T) {
	ours := sharedNames(orgAlpha, orgBeta)
	refused := orgdns.Name("*", "beta", "omani.homes")
	fake, srv := newFakeOrgPDNS(t, ours, refused)
	report := runOrgDNSWipe(t, "dep-orgdns-refused", srv, nil,
		orgObject("alpha", "omani.homes", "alpha"), orgObject("beta", "omani.homes", "beta"))

	got := report.OrgRecordSets
	if got == nil || len(got.Left) != 1 || got.Left[0].Name != refused {
		t.Fatalf("orgRecordSets = %+v, want exactly %s left", got, refused)
	}
	if !fake.has(refused) {
		t.Errorf("the refused record set %s is gone from the fake zone", refused)
	}
	wantReleased := slices.DeleteFunc(slices.Clone(ours), func(n string) bool { return n == refused })
	if !slices.Equal(got.Released, wantReleased) {
		t.Errorf("released = %q, want %q", got.Released, wantReleased)
	}
	if !slices.ContainsFunc(report.Errors, func(e string) bool {
		return strings.HasPrefix(e, "organization record set left: "+refused)
	}) {
		t.Errorf("Errors = %q, want the left record set named", report.Errors)
	}
}

func TestWipe_NoOrganizationsMakesNoOrganizationDNSCall(t *testing.T) {
	fake, srv := newFakeOrgPDNS(t, sharedNames(orgOther))
	report := runOrgDNSWipe(t, "dep-orgdns-none", srv, nil)

	if got := fake.patchedNames(orgPoolZone); len(got) != 0 {
		t.Fatalf("a deployment without Organizations made DNS calls in %s: %q", orgPoolZone, got)
	}
	got := report.OrgRecordSets
	if got == nil || len(got.Released) != 0 || len(got.Left) != 0 || got.NotEnumerated != "" {
		t.Errorf("orgRecordSets = %+v, want empty", got)
	}
}

// TestOrganizationRecordSetNames_WriterAndWipeShareOneFunction asserts the
// engine's writer (organization_dns.go) writes the record sets the wipe
// releases: both come from orgdns. The organization-controller's writer is
// asserted the same way in core/controllers (tenant_dns_shared_names_test.go).
func TestOrganizationRecordSetNames_WriterAndWipeShareOneFunction(t *testing.T) {
	var mu sync.Mutex
	var written []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			RRSets []pdnsRRSet `json:"rrsets"`
		}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		for _, rs := range body.RRSets {
			written = append(written, rs.Name)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	p := DefaultOrganizationDNSProvisioner{PoolWriter: NewPowerDNSWriter(srv.URL, "pool-key")}
	if err := p.ProvisionFreeSubdomain(context.Background(), "alpha", "omani.homes", "212.72.24.14", ""); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}
	for _, n := range orgdns.RecordSetNames(orgAlpha) {
		if !slices.Contains(written, n) {
			t.Errorf("the writer did not write %s, the name the wipe releases (wrote %q)", n, written)
		}
	}
}
