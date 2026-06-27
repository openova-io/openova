package handler

import "strings"

// defaultedParameters returns the value to stamp into
// `Application.spec.parameters` for a newly-created Application CR.
//
// THE BUG IT FIXES (#4283 / #4282 Root-B validation half): the two
// Application-CR producers (newApplicationUnstructured + the
// create-instance seed path newApplicationCRFromSeed) used to set
// `spec.parameters` ONLY when the caller supplied a non-empty parameters
// map. Console- and funnel-created instances (e.g. the auto-created
// backing-service postgres `shared-pg-d`/`-e`) carry NO explicit
// parameters, so the CR was emitted with the `parameters` key entirely
// absent. Once that CR round-trips through the per-Org IaC Git YAML and
// back, the absent key materialises as an explicit `parameters: null`,
// and the application-controller's configSchema validation
// (core/controllers/pkg/validate.Parameters) rejects it with
//
//	parameters do not match Blueprint configSchema: #: expected object, but got null
//
// before anything else reconciles → phase=Failed.
//
// THE FIX: every produced Application CR now ALWAYS carries a non-null
// `spec.parameters` OBJECT. When the caller supplies explicit parameters
// we use them verbatim (a defensive copy). Otherwise we emit at least an
// empty object `{}` — which validates against any configSchema whose
// required fields all default (bp-postgres' configSchema has no top-level
// `required` and every property defaults, so `{}` is valid and the
// CNPG-cluster defaults — singleton, 5Gi, pg16 — apply).
//
// For bp-postgres specifically we additionally seed `topology.mode` from
// the chosen placement topology, mirroring the host-shared bootstrap
// template (platform/postgres/chart/templates/application-cr.yaml) which
// always stamps `parameters.topology.mode`. The bp-postgres configSchema
// `topology.mode` enum is the NARROW data-plane set `[singleton,
// active-hot-standby]` (NOT the broad placement vocabulary), so we map
// the canonical placement posture down to a schema-valid mode:
//   - active-hot-standby / active-active / active-passive (any HA / multi
//     posture) → active-hot-standby (the only HA mode the engine renders)
//   - singleton / single-region / empty / unknown → singleton
//
// For bp-agenity specifically we additionally stamp `sovereignFqdn` from the
// Sovereign's own FQDN (#4556 Item 2). The agenity chart derives the
// openova-MCP catalyst-api URL as `https://console.<.Values.sovereignFqdn>`,
// falling back to `console.openova.io` (the OpenOva MOTHERSHIP) when
// sovereignFqdn is empty (chart statefulset.yaml:230-231). The BSS-door
// GitOps overlay (organization_gitops.go orgTenantBPAgenity) already stamps
// sovereignFqdn, but the Application-CR install path here did NOT — so a
// per-Org agenity installed via POST /applications (or the create-instance
// seed path) left it empty and every spawned agent's MCP forwarded
// create_application calls to the MOTHERSHIP instead of this Sovereign.
// We stamp it whether or not the caller supplied other parameters (the User
// never sets it themselves); a caller that DID pin it wins (we never
// overwrite a non-empty explicit value).
//
// blueprint may carry the `bp-` prefix or not; topology is the canonical
// (or legacy-dialect) placement token already chosen by the caller.
// sovereignFQDN is the Sovereign's own FQDN (e.g. "omantel.biz"); empty on
// the mothership / Catalyst-Zero, where the agenity install is not a
// production path — leaving sovereignFqdn unset keeps the chart's existing
// fail-closed default behaviour.
func defaultedParameters(blueprint, topology, sovereignFQDN string, explicit map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(explicit)+1)
	for k, v := range explicit {
		out[k] = v
	}

	if isAgenityBlueprint(blueprint) {
		stampAgenitySovereignFqdn(out, sovereignFQDN)
	}

	if len(out) > 0 {
		return out
	}

	if isPostgresBlueprint(blueprint) {
		out["topology"] = map[string]interface{}{
			"mode": postgresConfigSchemaMode(topology),
		}
	}
	return out
}

// stampAgenitySovereignFqdn sets `parameters.sovereignFqdn` to the Sovereign's
// own FQDN (#4556 Item 2) unless the caller already pinned a non-empty value.
// No-op when sovereignFQDN is empty (the mothership case) so the chart keeps
// its existing fail-closed default rather than rendering a bogus host.
func stampAgenitySovereignFqdn(params map[string]interface{}, sovereignFQDN string) {
	fqdn := strings.TrimSpace(sovereignFQDN)
	if fqdn == "" {
		return
	}
	if existing, ok := params["sovereignFqdn"].(string); ok && strings.TrimSpace(existing) != "" {
		return
	}
	params["sovereignFqdn"] = fqdn
}

// isAgenityBlueprint reports whether the blueprint id refers to bp-agenity
// (with or without the `bp-` prefix).
func isAgenityBlueprint(blueprint string) bool {
	b := strings.TrimSpace(strings.ToLower(blueprint))
	b = strings.TrimPrefix(b, "bp-")
	return b == "agenity"
}

// isPostgresBlueprint reports whether the blueprint id refers to
// bp-postgres (with or without the `bp-` prefix).
func isPostgresBlueprint(blueprint string) bool {
	b := strings.TrimSpace(strings.ToLower(blueprint))
	b = strings.TrimPrefix(b, "bp-")
	return b == "postgres"
}

// postgresConfigSchemaMode folds the canonical placement topology onto the
// bp-postgres configSchema `topology.mode` enum [singleton,
// active-hot-standby]. Any HA / multi-region posture maps to
// active-hot-standby (the only HA shape the chart renders); everything
// else (singleton / single-region / empty / unknown) maps to singleton.
func postgresConfigSchemaMode(topology string) string {
	switch canonicalizeTopology(topology) {
	case "active-hot-standby", "active-active", "active-passive":
		return "active-hot-standby"
	default:
		return "singleton"
	}
}
