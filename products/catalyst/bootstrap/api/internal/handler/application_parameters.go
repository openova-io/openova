package handler

import (
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// agenityMCPBearerSecretName is the in-namespace K8s Secret the agenity
// chart's externalsecret-mcp-bearer.yaml materialises and the StatefulSet
// projects as OPENOVA_MCP_BEARER + OPENOVA_MCP_RS256_PUBKEY_PEM. Fixed by
// contract with products/agenity/chart/values.yaml (openovaMCP.mcpBearer
// .secretName) AND the BSS-door GitOps overlay (orgTenantBPAgenity) so the
// two create doors emit byte-identical wiring.
const agenityMCPBearerSecretName = "agenity-mcp-bearer"

// agenityMCPBearerStoreRef / Kind — the region-1 ClusterSecretStore the
// agenity ExternalSecret authenticates to OpenBao through. Same store the
// anthropic + mcp-bearer ExternalSecrets already use.
const (
	agenityMCPBearerStoreRef  = "vault-region1"
	agenityMCPBearerStoreKind = "ClusterSecretStore"
)

// agenityMCPBearerRemoteKeyPrefix + the Org slug build the per-Org OpenBao
// KV-v2 path the producer (seedMCPBearer) writes and the ExternalSecret
// reads — secret/catalyst/agenity/<slug>/mcp-bearer (properties bearer +
// pubkeyPem). MUST match mcpBearerSecretPath() in sovereign_mcp_bearer_seed.go.
const agenityMCPBearerRemoteKeyPrefix = "catalyst/agenity/"

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
// For bp-agenity we ALSO stamp the `openovaMCP` mcpBearer wiring (#4610). The
// chart defaults leave the per-Org bearer ExternalSecret DISABLED
// (mcpBearer.externalSecret.enabled=false, remoteKey="", and
// rs256PubkeySecret pointed at the host `catalyst-handover-jwt` Secret which
// is the MOTHERSHIP key — absent in the Org namespace anyway). So an agenity
// installed via the Application-CR door projected NO OPENOVA_MCP_BEARER, and
// the only bearer the MCP saw was a hand-injected MOTHERSHIP-signed token the
// Sovereign's catalyst-api rejects on EVERY endpoint (whoami/organizations/
// org/applications all 401 `unauthenticated`, because catalyst-api validates
// against the Sovereign's LOCAL handover-signer key — NOT the mothership
// key). The BSS-door overlay (orgTenantBPAgenity) wires this; the install
// door did not. We mirror that overlay so BOTH doors point the chart at the
// in-namespace `agenity-mcp-bearer` Secret — fed from the per-Org OpenBao
// path the producer (seedMCPBearer) writes with a LOCAL-signer-signed bearer
// + its matching verify pubkey. orgSlug is the leading DNS label of the
// caller's Org ref (the path is secret/catalyst/agenity/<slug>/mcp-bearer);
// empty orgSlug ⇒ skip the mcpBearer stamp (we can't scope the path).
//
// For bp-agenity we ALSO stamp `openovaMCP.tenantHost` with the ORG's public
// console host (#4624, live-proven on hw220 2026-07-04). The openova-MCP
// forwards this value as `X-Tenant-Host` on the org-scoped install path
// (POST /api/v1/org/applications); catalyst-api resolves the caller's Org
// namespace from it via the tenant registry. The catalyst-api base URL is the
// SOVEREIGN console host (console.<sovereignFqdn>) — which is NOT a registered
// tenant — so when tenantHost is unset the chart's fallback derivation
// (agenity.mcpTenantHost helper: httpRoute.hostnames empty → gate host
// agenity.<sovereignFqdn> → console.<sovereignFqdn>) emits the Sovereign host
// and EVERY agent create_application call 404s `tenant-not-registered`
// (`MCP error -32010: upstream error`). The BSS-door GitOps overlay
// (orgTenantBPAgenity) already resolves correctly (its httpRoute.hostnames[0]
// = agenity.<slug>.<pool> derives console.<slug>.<pool>); this stamp closes
// the Application-CR install door. orgConsoleHost is resolved from the tenant
// registry (h.orgConsoleHostFor); empty ⇒ no stamp (mothership /
// Catalyst-Zero / unresolvable — fail-closed, the chart's existing behaviour
// applies), and an explicit caller value is never clobbered (same deference
// as stampAgenitySovereignFqdn).
//
// blueprint may carry the `bp-` prefix or not; topology is the canonical
// (or legacy-dialect) placement token already chosen by the caller.
// sovereignFQDN is the Sovereign's own FQDN (e.g. "omantel.biz"); empty on
// the mothership / Catalyst-Zero, where the agenity install is not a
// production path — leaving sovereignFqdn unset keeps the chart's existing
// fail-closed default behaviour.
func defaultedParameters(blueprint, topology, sovereignFQDN, orgSlug, orgConsoleHost string, explicit map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(explicit)+1)
	for k, v := range explicit {
		out[k] = v
	}

	if isAgenityBlueprint(blueprint) {
		stampAgenitySovereignFqdn(out, sovereignFQDN)
		stampAgenityMCPBearer(out, orgSlug)
		stampAgenityMCPTenantHost(out, orgConsoleHost)
		stampAgenityGateHost(out, orgConsoleHost)
	}

	// #5206 gap 2 — a standalone per-Org bp-openova-mcp install reuses this
	// SAME per-Org Application-CR install door (POST /applications / the
	// create-instance seed path) — the only per-Org provisioning pipeline
	// that exists today (bp-agenity's embedded stdio child arrives the same
	// way). See stampOpenovaMCPOrgParameters for the full rationale.
	if isOpenovaMCPBlueprint(blueprint) {
		stampOpenovaMCPOrgParameters(out, sovereignFQDN, orgSlug, orgConsoleHost)
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

// stampAgenityMCPBearer wires the chart's openova-MCP bearer ExternalSecret
// (#4610) so the install-door agenity gets OPENOVA_MCP_BEARER +
// OPENOVA_MCP_RS256_PUBKEY_PEM from the per-Org OpenBao path the producer
// (seedMCPBearer) writes — secret/catalyst/agenity/<slug>/mcp-bearer. This is
// the exact block the BSS-door GitOps overlay (orgTenantBPAgenity) emits, so
// both doors deliver the same LOCAL-signer-signed bearer the Sovereign's
// catalyst-api accepts. Without it the chart's `enabled:false` default leaves
// the bearer undelivered and the MCP forwards an unaccepted token → 401.
//
// No-op when:
//   - orgSlug is empty (we can't scope the OpenBao path / a forged cross-Org
//     scope would defeat the seedMCPBearer own-Org guarantee), or
//   - the caller already pinned a non-empty openovaMCP block (we never
//     clobber an explicit value — same deference as sovereignFqdn).
//
// The merge is additive: we set ONLY the wiring keys the chart needs
// (bearerSecret / rs256PubkeySecret / mcpBearer.externalSecret), preserving
// any other openovaMCP sub-keys an explicit caller supplied.
func stampAgenityMCPBearer(params map[string]interface{}, orgSlug string) {
	// The OpenBao path the producer scopes per-Org is keyed by the Org SLUG
	// (leading DNS label), matching mcpBearerSecretPath(). A dotted FQDN ref
	// (e.g. "nstar2.omani.homes") must collapse to "nstar2".
	slug := leadingDNSLabel(orgSlug)
	if slug == "" {
		return
	}

	mcp, _ := params["openovaMCP"].(map[string]interface{})
	if mcp == nil {
		mcp = map[string]interface{}{}
	}

	// bearerSecret + rs256PubkeySecret point the StatefulSet at the in-ns
	// Secret the ExternalSecret materialises (both keys live in one Secret so
	// the bearer travels with the pubkey that verifies it — #4228).
	setIfAbsentMap(mcp, "bearerSecret", map[string]interface{}{
		"name": agenityMCPBearerSecretName,
		"key":  "bearer",
	})
	setIfAbsentMap(mcp, "rs256PubkeySecret", map[string]interface{}{
		"name": agenityMCPBearerSecretName,
		"key":  "pubkeyPem",
	})

	mcpBearer, _ := mcp["mcpBearer"].(map[string]interface{})
	if mcpBearer == nil {
		mcpBearer = map[string]interface{}{}
	}
	setIfAbsentMap(mcpBearer, "externalSecret", map[string]interface{}{
		"enabled":              true,
		"secretStoreRef":       agenityMCPBearerStoreRef,
		"secretStoreKind":      agenityMCPBearerStoreKind,
		"remoteKey":            agenityMCPBearerRemoteKeyPrefix + slug + "/mcp-bearer",
		"remoteBearerProperty": "bearer",
		"remotePubkeyProperty": "pubkeyPem",
	})
	mcp["mcpBearer"] = mcpBearer

	params["openovaMCP"] = mcp
}

// stampAgenityMCPTenantHost sets `parameters.openovaMCP.tenantHost` to the
// Org's public console host (console.<slug>.<poolParentDomain>, e.g.
// console.nstar.omani.homes) so the chart's OPENOVA_MCP_TENANT_HOST env — the
// X-Tenant-Host the MCP forwards on org-scoped calls — targets the ORG host,
// not the Sovereign console host (#4624, live-proven on hw220: with
// TENANT_HOST=console.hw220.omani.works every create_application 404'd; the
// moment it was set to console.nstar.omani.homes the same call returned 201).
// The chart's `agenity.mcpTenantHost` helper prefers an explicit
// openovaMCP.tenantHost over any derivation, so this value flows straight to
// the env. OPENOVA_MCP_CATALYST_API_URL is untouched — it keeps deriving
// https://console.<sovereignFqdn> from the separately-stamped sovereignFqdn.
//
// No-op when:
//   - orgConsoleHost is empty (mothership / Catalyst-Zero / registry has no
//     row for this Org — fail-closed, the chart's existing behaviour holds), or
//   - the caller already pinned a non-empty openovaMCP.tenantHost (we never
//     clobber an explicit value — same deference as stampAgenitySovereignFqdn).
//
// The merge is additive on the openovaMCP block, preserving any other
// sub-keys (bearerSecret / mcpBearer / …) a caller or sibling stamp supplied.
func stampAgenityMCPTenantHost(params map[string]interface{}, orgConsoleHost string) {
	host := strings.ToLower(strings.TrimSpace(orgConsoleHost))
	if host == "" {
		return
	}

	mcp, _ := params["openovaMCP"].(map[string]interface{})
	if mcp == nil {
		mcp = map[string]interface{}{}
	}
	if existing, ok := mcp["tenantHost"].(string); ok && strings.TrimSpace(existing) != "" {
		return
	}
	mcp["tenantHost"] = host
	params["openovaMCP"] = mcp
}

// stampAgenityGateHost sets `parameters.httpRoute.hostnames[0]` to the Org's
// public agenity app host (agenity.<slug>.<poolParentDomain>, e.g.
// agenity.nstar.omani.homes) so the chart's `agenity.gateHostname` helper
// resolves to the ORG host instead of falling back to `agenity.<sovereignFqdn>`
// (#4739 W-3). Live-confirmed on hw220: the MCP `create_application` door never
// sets httpRoute.hostnames, so the gate/oidc-gate HTTPRoute rendered on
// `agenity.hw220.omani.works` — a host with NO DNS record — while the resolvable
// Org host `agenity.nstar.omani.homes` had no route → agenity unreachable
// externally (the API-side North Star still passed; only the human front door
// was split-brained).
//
// The Org host is the exact inverse of the mcpTenantHost derivation: swap the
// leading label of orgConsoleHost (console.<slug>.<pool>) for `agenity` →
// agenity.<slug>.<pool>. This also keeps the chart's mcpTenantHost helper's
// fallback derivation (agenity.<zone> → console.<zone>) self-consistent.
//
// No-op when:
//   - orgConsoleHost is empty (mothership / Catalyst-Zero — the chart's
//     fail-closed `agenity.<sovereignFqdn>` default holds), or
//   - the caller already pinned a non-empty httpRoute.hostnames (deference —
//     an explicit host wins, same as the sibling stamps).
func stampAgenityGateHost(params map[string]interface{}, orgConsoleHost string) {
	host := strings.ToLower(strings.TrimSpace(orgConsoleHost))
	if host == "" {
		return
	}
	// console.<slug>.<pool> → <slug>.<pool>; require a dotted host so a bare
	// label can't produce a zone-less "agenity." host.
	parts := strings.SplitN(host, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return
	}
	gateHost := "agenity." + parts[1]

	hr, _ := params["httpRoute"].(map[string]interface{})
	if hr == nil {
		hr = map[string]interface{}{}
	}
	if existing, ok := hr["hostnames"].([]interface{}); ok && len(existing) > 0 {
		return
	}
	hr["hostnames"] = []interface{}{gateHost}
	params["httpRoute"] = hr
}

// openovaMCPOrgHTTPRouteParentRef is the console-facing Cilium Gateway a
// per-Org bp-openova-mcp install must parent its HTTPRoute to (#4054 console
// isolation) — the SAME gateway bp-agenity's own chart hardcodes as ITS
// default (products/agenity/chart/values.yaml). bp-openova-mcp cannot bake
// this into its own chart defaults because ONE chart serves both the
// bootstrap-kit sovereign-mode slot 13d (kube-system/cilium-gateway is
// correct there) and a per-Org install — so the org-mode gateway override
// must come from this install-time stamp, not the chart.
const openovaMCPOrgHTTPRouteParentRef = "cilium-gateway-console"

// stampOpenovaMCPOrgParameters wires a per-Org bp-openova-mcp install
// (#5206 gap 2 — the Pillar-4 north-star Org→MCP surface) through the SAME
// per-Org Application-CR install door bp-agenity's embedded stdio child
// already uses (POST /applications / the create-instance seed path) — today
// the ONLY per-Org provisioning pipeline that exists (there is no separate
// Org-create-time auto-provisioning trigger for either Blueprint). Mirrors
// the proven stampAgenity* pattern field-for-field, adapted to
// bp-openova-mcp's OWN values shape (products/openova-mcp/chart/values.yaml):
//
//   - mode: "organization" — pins the instance's realm context
//     (internal/identity.Resolver.pinnedCtx) so THIS install can never widen
//     to Sovereign scope no matter what bearer reaches it (the #5206 gap-3
//     hardening in internal/identity/identity.go + whoami.go is what makes
//     that pin safe: an org-scoped caller is confined to its own Org, never
//     silently relabelled).
//   - sovereignFqdn — so the chart's openova-mcp.catalystApiUrl helper
//     derives https://console.<sovereignFqdn> (organization mode), giving
//     the binary a catalyst-api client to delegate the #5175 whoami-fallback
//     identity resolution to (internal/identity.Resolver.FromWhoami) even
//     before any local verify key is wired.
//   - organization.tenantHost — the Org's public console host, forwarded as
//     X-Tenant-Host (#4116/#4610) exactly like the agenity stamp, so
//     catalyst-api resolves the caller's OWN Org namespace on every
//     forwarded tool call.
//   - httpRoute.hostnames[0] = mcp.<slug>.<pool> + httpRoute.parentRef.name
//     = cilium-gateway-console — the chart's OWN default
//     (cilium-gateway/kube-system + mcp.<sovereignFqdn>) is the Sovereign
//     slot-13d shape and is the WRONG gateway/host for a per-Org install
//     (the exact #5206 gap-1 DNS-and-gateway mismatch class, applied here to
//     the per-Org door before it ships the same bug a second time).
//   - auth.rs256PubkeySecret — best-effort: points at the SAME in-namespace
//     `agenity-mcp-bearer` Secret the agenity-embedded stdio child already
//     consumes (fed by the existing per-Org OpenBao producer, seedMCPBearer
//     / #4610 — no second producer/path needed). The chart's secretKeyRef is
//     optional:true (products/openova-mcp/chart/templates/deployment.yaml),
//     so an Org that has not (also) installed bp-agenity in this namespace
//     still schedules Ready — the binary degrades to the #5175
//     whoami-delegation path instead of crash-looping on an absent Secret.
//
// Deference (same as every stampAgenity* sibling): every field is set only
// when the caller did not already pin a non-empty value; nothing here is
// forced onto an explicit install request.
func stampOpenovaMCPOrgParameters(params map[string]interface{}, sovereignFQDN, orgSlug, orgConsoleHost string) {
	setIfAbsentString(params, "mode", "organization")
	stampOpenovaMCPSovereignFqdn(params, sovereignFQDN)
	stampOpenovaMCPTenantHost(params, orgConsoleHost)
	stampOpenovaMCPGateHost(params, orgConsoleHost)
	stampOpenovaMCPBearer(params, orgSlug)
}

// stampOpenovaMCPSovereignFqdn sets `parameters.sovereignFqdn` — see
// stampOpenovaMCPOrgParameters. No-op when sovereignFQDN is empty (the
// mothership case) so the chart's existing fail-closed default holds.
func stampOpenovaMCPSovereignFqdn(params map[string]interface{}, sovereignFQDN string) {
	fqdn := strings.TrimSpace(sovereignFQDN)
	if fqdn == "" {
		return
	}
	setIfAbsentString(params, "sovereignFqdn", fqdn)
}

// stampOpenovaMCPTenantHost sets `parameters.organization.tenantHost` to the
// Org's public console host — see stampOpenovaMCPOrgParameters. No-op when
// orgConsoleHost is empty (mothership / unregistered tenant — fail-closed).
func stampOpenovaMCPTenantHost(params map[string]interface{}, orgConsoleHost string) {
	host := strings.ToLower(strings.TrimSpace(orgConsoleHost))
	if host == "" {
		return
	}
	org, _ := params["organization"].(map[string]interface{})
	if org == nil {
		org = map[string]interface{}{}
	}
	setIfAbsentString(org, "tenantHost", host)
	params["organization"] = org
}

// stampOpenovaMCPGateHost sets `parameters.httpRoute.hostnames[0]` to the
// Org's public MCP host (mcp.<slug>.<pool>, the exact inverse derivation of
// stampAgenityGateHost's agenity.<slug>.<pool>) and
// `parameters.httpRoute.parentRef.name` to the console gateway — see
// stampOpenovaMCPOrgParameters. The parentRef stamp is independent of the
// hostnames deference: even a caller who pinned their own hostnames still
// needs the console-gateway parentRef corrected (the chart default parents
// the Sovereign-wide gateway, which has no listener path a per-Org install
// can rely on being routed the same way). No-op (whole function) when
// orgConsoleHost is empty (mothership / unregistered tenant — fail-closed,
// chart defaults hold).
func stampOpenovaMCPGateHost(params map[string]interface{}, orgConsoleHost string) {
	host := strings.ToLower(strings.TrimSpace(orgConsoleHost))
	if host == "" {
		return
	}
	// console.<slug>.<pool> → <slug>.<pool>; require a dotted host so a bare
	// label can't produce a zone-less "mcp." host.
	parts := strings.SplitN(host, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return
	}
	gateHost := "mcp." + parts[1]

	hr, _ := params["httpRoute"].(map[string]interface{})
	if hr == nil {
		hr = map[string]interface{}{}
	}
	if existing, ok := hr["hostnames"].([]interface{}); !ok || len(existing) == 0 {
		hr["hostnames"] = []interface{}{gateHost}
	}
	setIfAbsentMap(hr, "parentRef", map[string]interface{}{
		"name": openovaMCPOrgHTTPRouteParentRef,
	})
	params["httpRoute"] = hr
}

// stampOpenovaMCPBearer wires `parameters.auth.rs256PubkeySecret` at the SAME
// in-namespace Secret the agenity-embedded stdio child consumes — see
// stampOpenovaMCPOrgParameters. No-op when orgSlug is empty (we can't scope
// the OpenBao-fed Secret name any tighter than "the agenity convention", but
// we at least require SOME Org identity) or the caller already pinned a
// non-empty auth.rs256PubkeySecret.
func stampOpenovaMCPBearer(params map[string]interface{}, orgSlug string) {
	slug := leadingDNSLabel(orgSlug)
	if slug == "" {
		return
	}
	auth, _ := params["auth"].(map[string]interface{})
	if auth == nil {
		auth = map[string]interface{}{}
	}
	setIfAbsentMap(auth, "rs256PubkeySecret", map[string]interface{}{
		"name": agenityMCPBearerSecretName,
		"key":  "pubkeyPem",
	})
	params["auth"] = auth
}

// orgConsoleHostFor resolves the Org's public console host
// (console.<slug>.<poolParentDomain>) for an Org ref from the tenant
// registry — the SAME table the org-scoped install path resolves
// X-Tenant-Host against, so the stamped tenantHost is by construction a
// registered tenant. Returns "" when the registry is unwired (mothership /
// CI) or holds no row for this Org (fail-closed — the caller skips the
// stamp).
func (h *Handler) orgConsoleHostFor(orgRef string) string {
	if h == nil || h.tenantRegistry == nil {
		return ""
	}
	return orgConsoleHostFromRegistrations(h.tenantRegistry.List(), orgRef)
}

// orgConsoleHostFromRegistrations is the pure resolver behind
// orgConsoleHostFor. The Org ref arrives in several dialects depending on
// the door: the real Org namespace (`org-<uuid>` or the slug — the org-scoped
// install door forces this), a bare slug ("nstar"), or the dotted Org zone
// ("nstar.omani.homes"). Matching, in preference order:
//
//  1. registration.OrganizationNamespace == orgNamespace(ref) — the exact
//     namespace binding (covers the org-door's forced namespace and a
//     slug-named namespace);
//  2. the Org slug of the registration's Host (console.<slug>.<pool> →
//     <slug>, via orgSlugFromHost) == the ref's leading DNS label (covers
//     the sovereign-admin door's dotted Org-zone refs).
//
// Only tenant_kind=org rows participate. Ties resolve to the
// lexicographically-smallest host so the result is deterministic regardless
// of registry iteration order.
func orgConsoleHostFromRegistrations(regs []store.TenantRegistration, orgRef string) string {
	ref := strings.TrimSpace(orgRef)
	if ref == "" {
		return ""
	}
	ns := orgNamespace(ref)
	slug := leadingDNSLabel(ref)
	nsMatch, slugMatch := "", ""
	for _, reg := range regs {
		if reg.TenantKind != store.TenantKindOrg {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(reg.Host))
		if host == "" {
			continue
		}
		if regNS := strings.ToLower(strings.TrimSpace(reg.OrganizationNamespace)); regNS != "" && regNS == ns {
			if nsMatch == "" || host < nsMatch {
				nsMatch = host
			}
			continue
		}
		if slug != "" && orgSlugFromHost(host) == slug {
			if slugMatch == "" || host < slugMatch {
				slugMatch = host
			}
		}
	}
	if nsMatch != "" {
		return nsMatch
	}
	return slugMatch
}

// setIfAbsentMap sets key=val only when the destination has no non-empty
// value at key — so an explicit caller value is never clobbered. Treats an
// existing non-empty map as authoritative.
func setIfAbsentMap(dst map[string]interface{}, key string, val map[string]interface{}) {
	if existing, ok := dst[key].(map[string]interface{}); ok && len(existing) > 0 {
		return
	}
	dst[key] = val
}

// setIfAbsentString sets key=val only when the destination has no non-empty
// string value at key — the scalar-valued twin of setIfAbsentMap.
func setIfAbsentString(dst map[string]interface{}, key, val string) {
	if existing, ok := dst[key].(string); ok && strings.TrimSpace(existing) != "" {
		return
	}
	dst[key] = val
}

// leadingDNSLabel returns the first DNS label (everything before the first
// dot) of an Org ref, lower-cased + trimmed. "nstar2.omani.homes" → "nstar2";
// "acme" → "acme"; "" → "". Matches the per-Org OpenBao path slug the
// producer (seedMCPBearer / mcpBearerSecretPath) scopes by.
func leadingDNSLabel(ref string) string {
	r := strings.ToLower(strings.TrimSpace(ref))
	if i := strings.IndexByte(r, '.'); i >= 0 {
		r = r[:i]
	}
	return r
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

// isOpenovaMCPBlueprint reports whether the blueprint id refers to
// bp-openova-mcp (with or without the `bp-` prefix) — #5206 gap 2.
func isOpenovaMCPBlueprint(blueprint string) bool {
	b := strings.TrimSpace(strings.ToLower(blueprint))
	b = strings.TrimPrefix(b, "bp-")
	return b == "openova-mcp"
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
