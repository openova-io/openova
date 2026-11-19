package orgrecords

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/pkg/orgdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
)

// fakeZones records every call; held names the zones the server holds.
type fakeZones struct {
	held    map[string]bool
	exists  []string
	patches []string // "<zone> <name> <changetype>"
}

func (f *fakeZones) ZoneExists(_ context.Context, name string) (bool, error) {
	f.exists = append(f.exists, name)
	return f.held[name], nil
}

func (f *fakeZones) PatchRRSets(_ context.Context, zone string, rrsets []powerdns.RRSet) error {
	for _, rs := range rrsets {
		f.patches = append(f.patches, zone+" "+rs.Name+" "+rs.ChangeType)
	}
	return nil
}

func noEmit(string, string, string) {}

func TestRelease_NoOrganizationRecordSetMakesNoCall(t *testing.T) {
	for _, set := range []Set{{}, {Orgs: []orgdns.Org{{Slug: "noparent", Kind: "customer"}}}} {
		z := &fakeZones{}
		r := set.Release(z, noEmit)
		if len(z.exists) != 0 || len(z.patches) != 0 {
			t.Fatalf("Release(%+v) made calls: exists=%q patches=%q", set, z.exists, z.patches)
		}
		if len(r.Released) != 0 || len(r.Left) != 0 || r.NotEnumerated != "" || len(r.Errors()) != 0 {
			t.Fatalf("Release(%+v) = %+v, want empty", set, r)
		}
	}
}

func TestRelease_NotEnumeratedDeletesNothingAndSaysSo(t *testing.T) {
	z := &fakeZones{}
	r := Set{Err: errors.New("apiserver unreachable")}.Release(z, noEmit)
	if len(z.exists)+len(z.patches) != 0 {
		t.Fatalf("calls made: %q %q", z.exists, z.patches)
	}
	if r.NotEnumerated != "apiserver unreachable" || len(r.Errors()) != 1 {
		t.Fatalf("report = %+v errors=%q", r, r.Errors())
	}
}

// An internal Organization's records are in the Sovereign FQDN's zone when the
// server holds it, else in its parent: the writer's rule (tenant_dns.go).
func TestRelease_InternalOrganizationUsesTheWritersZone(t *testing.T) {
	org := orgdns.Org{Slug: "ops", Kind: "internal", SovereignRef: "hw1.omani.works"}
	for _, held := range []bool{true, false} {
		z := &fakeZones{held: map[string]bool{"hw1.omani.works": held}}
		r := Set{Orgs: []orgdns.Org{org}}.Release(z, noEmit)
		zone := "omani.works"
		if held {
			zone = "hw1.omani.works"
		}
		want := []string{zone + " console.ops.hw1.omani.works. DELETE", zone + " *.ops.hw1.omani.works. DELETE"}
		if !slices.Equal(z.patches, want) {
			t.Fatalf("held=%v patches = %q, want %q", held, z.patches, want)
		}
		if !slices.Equal(r.Released, orgdns.RecordSetNames(org)) {
			t.Fatalf("released = %q", r.Released)
		}
	}
}

func TestRelease_NilClientLeavesEveryRecordSetNamed(t *testing.T) {
	org := orgdns.Org{Slug: "acme", ParentDomain: "omani.homes"}
	r := Set{Orgs: []orgdns.Org{org}}.Release(nil, noEmit)
	if len(r.Left) != 2 || r.Left[0].Name != "console.acme.omani.homes." || r.Left[1].Name != "*.acme.omani.homes." {
		t.Fatalf("left = %+v", r.Left)
	}
}

// fakeClients holds the live client of one cluster.
type fakeClients struct {
	id  string
	dyn dynamic.Interface
}

func (f fakeClients) DynamicClientFor(id string) (dynamic.Interface, error) {
	if id != f.id {
		return nil, errors.New("k8scache: cluster " + id + " not registered")
	}
	return f.dyn, nil
}

func TestRead_MapsTheOrganizationSpec(t *testing.T) {
	org := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1", "kind": "Organization",
		"metadata": map[string]any{"name": "acme"},
		"spec": map[string]any{"slug": "acme", "kind": "customer", "sovereignRef": "hw1.omani.works",
			"tenantPublic": map[string]any{"parentDomain": "omani.homes", "subdomain": "shop"}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{organizationGVR: "OrganizationList"}, org)
	clients := fakeClients{id: "dep1", dyn: dyn}

	set := read(clients, "dep1")
	want := orgdns.Org{Slug: "acme", Kind: "customer", SovereignRef: "hw1.omani.works", ParentDomain: "omani.homes", Subdomain: "shop"}
	if set.Err != nil || len(set.Orgs) != 1 || set.Orgs[0] != want {
		t.Fatalf("read = %+v, want [%+v]", set, want)
	}
	if other := read(clients, "dep2"); other.Err == nil || !strings.Contains(other.Err.Error(), "not registered") {
		t.Fatalf("read of a cluster the cache does not hold = %+v, want an error", other)
	}
	if none := Read(nil, "dep1"); none.Err == nil {
		t.Fatalf("Read without a k8scache = %+v, want an error", none)
	}
}
