// Package reserved holds the canonical list of subdomain names that no tenant
// may claim under any OpenOva pool domain. It used to live in
// products/catalyst/bootstrap/api/internal/handler/subdomains.go as a private
// var — duplicated knowledge with no clear owner.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the list lives in ONE place: PDM.
// catalyst-api consults PDM via /check; the wizard consults catalyst-api via
// /api/v1/subdomains/check. There is no second copy of this list anywhere
// else in the fleet.
//
// The list is the union of:
//   - control-plane prefixes the allocator publishes as A records into
//     every Sovereign's child PowerDNS zone on /commit (api, admin,
//     console, gitea, harbor — see allocator.canonicalRecordSet)
//   - infrastructure prefixes that map to specific OpenOva services
//     (openova, catalyst, openbao, vault, flux, k8s, system)
//   - operational prefixes that look enough like a Sovereign to be
//     dangerous if a tenant grabbed them (www, mail, smtp, imap, vpn, app,
//     status, docs)
//
// The IsReserved() function is the only exported surface — callers don't
// see (and can't mutate) the underlying map.
package reserved

import "strings"

// reservedSubdomains — names we never let a tenant claim as their Sovereign
// root subdomain. Tenants get *.omantel.omani.works style records
// automatically; allowing a tenant to claim "console" would create
// "console.console.omani.works" which is meaningless and confusing.
var reservedSubdomains = map[string]struct{}{
	"api":      {},
	"admin":    {},
	"console":  {},
	"gitea":    {},
	"harbor":   {},
	"keycloak": {},
	"www":      {},
	"mail":     {},
	"smtp":     {},
	"imap":     {},
	"vpn":      {},
	"openova":  {},
	"catalyst": {},
	"docs":     {},
	"status":   {},
	"app":      {},
	"system":   {},
	"openbao":  {},
	"vault":    {},
	"flux":     {},
	"k8s":      {},
}

// IsReserved reports whether the given subdomain (lower-cased, trimmed) is
// in the reserved set. Caller is responsible for validating the input is a
// well-formed DNS label first; this function only checks set membership.
func IsReserved(subdomain string) bool {
	_, ok := reservedSubdomains[strings.ToLower(strings.TrimSpace(subdomain))]
	return ok
}

// All returns a sorted copy of the reserved list. Used by /api/v1/reserved
// for clients (e.g. the wizard) that want to render the list inline as a
// hint to the user.
func All() []string {
	out := make([]string, 0, len(reservedSubdomains))
	for k := range reservedSubdomains {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
