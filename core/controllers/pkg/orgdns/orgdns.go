// Package orgdns names the DNS record sets the platform writes for an
// Organization's public hostname: `console.<label>.<parent>` and
// `*.<label>.<parent>`. The organization-controller writes them into the pool
// zone the parent's PowerDNS serves (tenant_dns.go); the engine of the parent
// deletes them when it wipes the Sovereign that holds the Organization
// (products/catalyst/bootstrap/api/internal/handler/wipe_org_dns.go). Both
// compute the names here, so the writer and the wipe cannot drift.
package orgdns

import "strings"

// Org is the part of an Organization's spec the record-set names depend on.
type Org struct {
	Slug         string // spec.slug
	Kind         string // spec.kind ("customer" or "internal")
	SovereignRef string // spec.sovereignRef (the FQDN of the hosting Sovereign)
	ParentDomain string // spec.tenantPublic.parentDomain
	Subdomain    string // spec.tenantPublic.subdomain (defaults to spec.slug)
}

// Prefixes are the leftmost labels of the record sets written for every
// Organization, in write order.
var Prefixes = []string{"console", "*"}

// Parent returns the parent domain an Organization's hosts live under and the
// zones, in order, its records may be written into:
//
//   - spec.tenantPublic.parentDomain when set: that one zone;
//   - otherwise, for an internal Organization whose sovereignRef has at least
//     three labels: the Sovereign FQDN, then its parent (the writer picks the
//     first zone the PowerDNS server holds);
//   - otherwise "" and no zones: the Organization has no record sets of its own.
func Parent(o Org) (parent string, zones []string) {
	parent = strings.TrimSpace(o.ParentDomain)
	if parent != "" {
		return parent, []string{parent}
	}
	if !strings.EqualFold(strings.TrimSpace(o.Kind), "internal") {
		return "", nil
	}
	sov := strings.ToLower(strings.TrimSpace(o.SovereignRef))
	if strings.Count(sov, ".") < 2 {
		return "", nil
	}
	return sov, []string{sov, sov[strings.Index(sov, ".")+1:]}
}

// Label returns the leftmost label of the Organization's hosts:
// spec.tenantPublic.subdomain, or spec.slug when that is empty.
func Label(o Org) string {
	if s := strings.TrimSpace(o.Subdomain); s != "" {
		return s
	}
	return strings.TrimSpace(o.Slug)
}

// Name returns the fully-qualified record-set name `<prefix>.<label>.<parent>.`
// (PowerDNS API convention: trailing dot).
func Name(prefix, label, parent string) string {
	return prefix + "." + label + "." + strings.TrimRight(parent, ".") + "."
}

// RecordSetNames returns the names of the record sets written for the
// Organization, one per Prefixes entry, in write order. Nil when the
// Organization has no parent domain or no label.
func RecordSetNames(o Org) []string {
	parent, _ := Parent(o)
	label := Label(o)
	if parent == "" || label == "" {
		return nil
	}
	names := make([]string, 0, len(Prefixes))
	for _, prefix := range Prefixes {
		names = append(names, Name(prefix, label, parent))
	}
	return names
}
