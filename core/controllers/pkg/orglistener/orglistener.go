// Package orglistener holds the route admission of an Organization's listener
// pair on the console Gateway, `console-https-<label>` and
// `console-http-<label>` for `*.<label>.<parent>` (NET-191). The pair has two
// writers: the organization-controller
// (core/controllers/organization/internal/controller/tenant_console_tls.go)
// and the catalyst-api
// (products/catalyst/bootstrap/api/internal/handler/org_console_tls.go). Both
// take the admission from here, so they write one value and never rewrite each
// other's listener.
package orglistener

// NameLabel is the label Kubernetes sets on every namespace to its own name.
const NameLabel = "kubernetes.io/metadata.name"

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
