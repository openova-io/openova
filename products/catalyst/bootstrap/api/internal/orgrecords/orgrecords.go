// Package orgrecords is the part of the wipe of a Sovereign that releases the
// record sets of the Sovereign's Organizations.
//
// Every Organization of a Sovereign has two record sets in the pool zone the
// parent's PowerDNS serves: console.<label>.<parent> and *.<label>.<parent>
// (core/controllers/pkg/orgdns). The Sovereign writes them itself — its
// organization-controller (tenant_dns.go) and its own engine
// (handler/organization_dns.go) PATCH that PowerDNS directly — so the parent
// keeps no stored record of them. The wipe deleted only the Sovereign's
// canonical records, so they were left; flight-sweep counts them and FND-G13
// failed the next fresh install.
//
// The wipe (handler/wipe.go) therefore reads the Organizations from the LIVE
// Sovereign before it destroys anything (Read), through the client the
// parent's k8scache already holds for the deployment's cluster (no new
// credential, no new network path), computes the names with
// orgdns.RecordSetNames (the writer's function) and deletes each record set
// (Release) through the PowerDNS client the canonical teardown uses. It
// releases them a second time once the cluster is destroyed, because until
// then the Sovereign's organization-controller re-asserts them on every
// reconcile; the second pass is the one reported.
//
// No silent success: Organizations that cannot be read, and a record set whose
// deletion is refused, are named in the Report and in its Errors. Only names
// computed from the Organizations of the deployment being wiped are deleted —
// never a pattern over the zone. A deployment without Organizations makes no
// DNS call.
package orgrecords

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/pkg/orgdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
)

// organizationGVR is the cluster-scoped Organization resource of a Sovereign.
var organizationGVR = schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}

// listTimeout bounds the read of the Organizations: a dead apiserver must not
// stall the wipe.
const listTimeout = 30 * time.Second

// Clients gives the live client of a cluster by its id; the parent's
// k8scache.Factory satisfies it (the primary cluster of a deployment has the
// deployment's id).
type Clients interface {
	DynamicClientFor(clusterID string) (dynamic.Interface, error)
}

// Zones is the part of the PowerDNS client the release uses; the handler's
// parent-zone client satisfies it.
type Zones interface {
	ZoneExists(ctx context.Context, name string) (bool, error)
	PatchRRSets(ctx context.Context, zone string, rrsets []powerdns.RRSet) error
}

// Left is a record set the wipe could not delete, and why.
type Left struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Report is what the wipe result says about the record sets of the
// Sovereign's Organizations.
type Report struct {
	// Released names every record set deleted.
	Released []string `json:"released"`
	// Left names every record set whose deletion was refused.
	Left []Left `json:"left,omitempty"`
	// NotEnumerated is set, with the reason, when the Organizations could
	// not be read from the live Sovereign: their record sets may be left.
	NotEnumerated string `json:"notEnumerated,omitempty"`
}

// Errors are the entries the Report adds to the wipe's errors.
func (r *Report) Errors() []string {
	var out []string
	if r.NotEnumerated != "" {
		out = append(out, "organization record sets not enumerated, they may be left: "+r.NotEnumerated)
	}
	for _, l := range r.Left {
		out = append(out, "organization record set left: "+l.Name+": "+l.Reason)
	}
	return out
}

// Set is the Organizations of one deployment, read from its live cluster.
type Set struct {
	Orgs []orgdns.Org
	Err  error // the Organizations could not be read
}

// Read reads the Organizations of deployment id from its live cluster through
// the parent's k8scache (nil: not configured, nothing can be read).
func Read(cache *k8scache.Factory, id string) Set {
	if cache == nil {
		return Set{Err: errors.New("no live client of the Sovereign's cluster: k8scache not configured")}
	}
	return read(cache, id)
}

// read lists the Organizations through the live client of cluster id. A
// cluster without the Organization resource has no Organizations.
func read(clients Clients, id string) Set {
	dyn, err := clients.DynamicClientFor(id)
	if err != nil {
		return Set{Err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	list, err := dyn.Resource(organizationGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Set{}
		}
		return Set{Err: fmt.Errorf("list organizations: %w", err)}
	}
	set := Set{Orgs: make([]orgdns.Org, 0, len(list.Items))}
	for i := range list.Items {
		obj := list.Items[i].Object
		field := func(path ...string) string {
			s, _, _ := unstructured.NestedString(obj, path...)
			return s
		}
		set.Orgs = append(set.Orgs, orgdns.Org{
			Slug:         field("spec", "slug"),
			Kind:         field("spec", "kind"),
			SovereignRef: field("spec", "sovereignRef"),
			ParentDomain: field("spec", "tenantPublic", "parentDomain"),
			Subdomain:    field("spec", "tenantPublic", "subdomain"),
		})
	}
	return set
}

// Release deletes the record sets of the Organizations of s from the zone the
// writer put them in (the first of orgdns.Parent's zones the server holds),
// one record set per request so a refusal names exactly one, and reports each
// on emit (the wipe's event stream). When s could not be read it deletes
// nothing and the Report says so.
func (s Set) Release(zones Zones, emit func(phase, level, msg string)) *Report {
	report := &Report{Released: []string{}}
	if s.Err != nil {
		report.NotEnumerated = s.Err.Error()
		emit("wipe", "error", "the Organizations could not be read from the live Sovereign; their record sets may be left in the pool zone: "+report.NotEnumerated)
		return report
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, o := range s.Orgs {
		names := orgdns.RecordSetNames(o)
		if len(names) == 0 {
			continue
		}
		zone, zoneErr := zoneOf(ctx, zones, o)
		for _, n := range names {
			err := zoneErr
			if err == nil {
				err = zones.PatchRRSets(ctx, zone, []powerdns.RRSet{{Name: n, Type: "A", ChangeType: "DELETE"}})
			}
			if err != nil {
				report.Left = append(report.Left, Left{Name: n, Reason: err.Error()})
				emit("wipe", "warn", "organization record set not released: "+n+": "+err.Error())
				continue
			}
			report.Released = append(report.Released, n)
			emit("wipe", "info", "organization record set released: "+n)
		}
	}
	return report
}

// zoneOf picks the zone the writer (tenant_dns.go poolZoneFor) put the
// Organization's records in: the first of its zones the server holds, the last
// without a question.
func zoneOf(ctx context.Context, zones Zones, o orgdns.Org) (string, error) {
	if zones == nil {
		return "", errors.New("no PowerDNS client configured")
	}
	_, candidates := orgdns.Parent(o)
	for i, zone := range candidates {
		if i == len(candidates)-1 {
			return zone, nil
		}
		held, err := zones.ZoneExists(ctx, zone)
		if err != nil {
			return "", fmt.Errorf("find the zone: %w", err)
		}
		if held {
			return zone, nil
		}
	}
	return "", errors.New("no zone")
}
