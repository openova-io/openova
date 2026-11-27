// Package orglistener holds the route admission of an Organization's listener
// pair on the console Gateway, `console-https-<label>` and
// `console-http-<label>` for `*.<label>.<parent>` (NET-191). The pair has two
// writers: the organization-controller
// (core/controllers/organization/internal/controller/tenant_console_tls.go)
// and the catalyst-api
// (products/catalyst/bootstrap/api/internal/handler/org_console_tls.go). Both
// take the admission from here, so they write one value and never rewrite each
// other's listener. NET-195 adds the Organization's TLS passthrough listeners
// (PassthroughListeners, MergePassthrough); the organization-controller writes
// them.
package orglistener

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// NameLabel is the label Kubernetes sets on every namespace to its own name.
const NameLabel = "kubernetes.io/metadata.name"

// passthroughPrefix starts every passthrough listener name,
// console-tls-<slug>-<label>. A label is letters and digits (no hyphen), so a
// name has one owner also when slugs have hyphens; "console" (the Sovereign's
// console host in every Organization's zone) is never passed through.
const passthroughPrefix = "console-tls-"

var hostLabel = regexp.MustCompile(`^[a-z0-9]{1,63}$`)

// IsPassthroughListenerOf reports whether name is a passthrough listener of the
// Organization slug.
func IsPassthroughListenerOf(name, slug string) bool {
	label, ok := strings.CutPrefix(name, passthroughPrefix+slug+"-")
	return ok && hostLabel.MatchString(label)
}

// PassthroughListeners returns one TLS passthrough listener per valid label,
// sorted, for host <label>.<zone> of the Organization slug, in the shape
// PRD-050 ran (docs/ledger/plan/spikes/PRD-050.md, answer 8): on the port of
// the Organization's HTTPS listener, admitting TLSRoutes from the namespaces of
// AllowedRoutes. The back end terminates TLS and verifies the client itself.
func PassthroughListeners(slug, zone string, labels []string, port int64, consoleRouteNamespace string) []map[string]any {
	valid := map[string]bool{}
	for _, l := range labels {
		valid[l] = l != "console" && hostLabel.MatchString(l)
	}
	var out []map[string]any
	for _, l := range slices.Sorted(maps.Keys(valid)) {
		if !valid[l] {
			continue
		}
		admit := AllowedRoutes(slug, consoleRouteNamespace)
		admit["kinds"] = []any{map[string]any{"kind": "TLSRoute"}}
		out = append(out, map[string]any{"name": passthroughPrefix + slug + "-" + l, "hostname": l + "." + zone,
			"port": port, "protocol": "TLS", "tls": map[string]any{"mode": "Passthrough"}, "allowedRoutes": admit})
	}
	return out
}

// MergePassthrough returns listeners with the passthrough listeners of the
// Organization slug made equal to want: a missing one is appended, one that
// differs in a field want sets is replaced, one want does not hold is removed,
// every other listener is kept. A field want does not set (one the API server
// defaults) is no difference, so a second pass changes nothing.
func MergePassthrough(listeners []any, slug string, want []map[string]any) ([]any, bool) {
	missing := map[string]map[string]any{}
	for _, w := range want {
		missing[w["name"].(string)] = w
	}
	out, changed := make([]any, 0, len(listeners)+len(want)), false
	for _, l := range listeners {
		m, _ := l.(map[string]any)
		n, _ := m["name"].(string)
		if w, mine := missing[n]; mine {
			delete(missing, n)
			if !satisfies(m, w) {
				l, changed = w, true
			}
		} else if IsPassthroughListenerOf(n, slug) {
			changed = true
			continue
		}
		out = append(out, l)
	}
	for _, w := range want {
		if _, ok := missing[w["name"].(string)]; ok {
			out, changed = append(out, w), true
		}
	}
	return out, changed
}

// satisfies reports whether live carries every field of want with its value.
func satisfies(live, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		l, ok := live.(map[string]any)
		for k, wv := range w {
			if !ok || !satisfies(l[k], wv) {
				return false
			}
		}
		return ok
	case []any:
		l, ok := live.([]any)
		if !ok || len(l) != len(w) {
			return false
		}
		for i := range w {
			if !satisfies(l[i], w[i]) {
				return false
			}
		}
		return true
	}
	return fmt.Sprintf("%v", live) == fmt.Sprintf("%v", want)
}

// AllowedRoutes is the allowedRoutes of both listeners of the pair. HTTPRoutes
// are admitted only from two namespaces, selected by NameLabel:
//
//   - orgNamespace, the Organization's host namespace (spec.slug). Its
//     Applications' routes are synced there out of its vCluster, and its
//     platform stack (oidc-gate of bp-agenity) writes its route there.
//   - consoleRouteNamespace, where the route of console.<label>.<parent> is
//     written. That host is served by the Sovereign's own console.
//
// A route in any other namespace, another Organization's included, attaches
// to no listener of the pair. The order of the values is fixed: the
// organization-controller compares a live listener with this value position
// by position.
func AllowedRoutes(orgNamespace, consoleRouteNamespace string) map[string]any {
	return map[string]any{
		"namespaces": map[string]any{
			"from": "Selector",
			"selector": map[string]any{
				"matchExpressions": []any{
					map[string]any{
						"key":      NameLabel,
						"operator": "In",
						"values":   []any{orgNamespace, consoleRouteNamespace},
					},
				},
			},
		},
	}
}
