# bp-openova-mcp — chart design

**What this is**: the install path for the OpenOva MCP server (#3988) — the
RBAC-scoped-per-user THIN facade that renders the OpenOva product surface as
MCP tools with UI=MCP parity. One binary, per-context instances
(#3988 §4.3 topology table):

| Context | Instance | `mode` value | Realm trust | Scope | Surface |
|---|---|---|---|---|---|
| Sovereign panel | bootstrap-kit slot **13d** | `sovereign` | Sovereign session issuer (`auth.expectedIssuer`) + handover-signer RS256 pubkey | full Sovereign | full superset |
| Organization panel | per-Org install | `organization` | same signer, org-context pin + `organization.orgSlug` scope pin | that Org only | Org subset |

It is **NOT a central MCP**: every instance is per-Sovereign or per-Org, and
the tool surface is filtered per caller by the two-layer RBAC in
`products/openova-mcp/internal/tools` (layer 1: `tools/list` filtered by
(context, tier); layer 2: per-call re-auth with parity-403).

## Place in the Catalyst model

- **Pillar 4** (docs/DOD.md D32/D33): the per-Org Agenity workspace reaches
  this MCP to `create_application` etc., scoped to exactly what the User can
  do in the console. The Sandbox concept this replaces was removed by the
  founder 2026-06-30; bootstrap-kit slot 19a (bp-sandbox) is retired in the
  same change that adds this chart (#5114).
- **Per-Org install door (#5206)**: a standalone `mode: organization`
  instance installs through the SAME per-Org Application-CR door bp-agenity's
  embedded stdio child already uses (`POST /applications` / the
  create-instance seed path) — there is no separate Org-create-time
  auto-provisioning trigger. `products/catalyst/bootstrap/api/internal/
  handler/application_parameters.go`'s `stampOpenovaMCPOrgParameters` stamps
  `mode=organization`, `sovereignFqdn`, `organization.tenantHost`,
  `httpRoute.hostnames=[mcp.<slug>.<pool>]`,
  `httpRoute.parentRef.name=cilium-gateway-console`, and (best-effort)
  `auth.rs256PubkeySecret` at the same in-namespace `agenity-mcp-bearer`
  Secret bp-agenity's stdio child consumes — mirroring the proven
  `stampAgenity*` pattern field-for-field. An Org without bp-agenity (yet)
  installed still gets a working instance: the optional pubkey secretKeyRef
  resolves absent and the binary falls back to the #5175 whoami-delegation
  identity path (see the pinnedCtx hardening note below).
- **Thin facade (#3988 §3, load-bearing)**: every data tool forwards the
  caller's bearer to the LIVE catalyst-api; the endpoint's own authz is the
  final word. The chart enforces this architecturally: the ServiceAccount
  carries **zero RBAC** and `automountServiceAccountToken: false` — the pod
  physically holds no Kubernetes credential to exceed the endpoint with.

## Transport reality (wired to the binary, not the design doc)

The binary (`cmd/openova-mcp`) serves **stdio** by default (the agenity
in-pod child contract, #4010/#4097) and the **streamable-HTTP transport**
when `OPENOVA_MCP_LISTEN` is set: `POST /mcp` (one JSON-RPC message per
request, bearer via `Authorization: Bearer`), `GET /healthz` + `/readyz`.
Server-initiated SSE streams (`GET /mcp`) are the documented #3988 follow-up
(design doc O4) — the endpoint returns 405 and spec-compliant clients fall
back to request/response POSTs. Both transports run the SAME dispatch path,
so RBAC semantics cannot diverge per transport.

## Resources rendered

- `Deployment` — 1 stateless replica, distroless image
  `ghcr.io/openova-io/openova-mcp:<appVersion>` (build-openova-mcp.yaml;
  plain image repo per the #4706 chart-repo-collision lesson), Guaranteed
  QoS 1:1 (per-Org `plan-limits` LimitRange, #4362), read-only rootfs,
  non-root. Image helper honors the cutover step-07 `global.imageRegistry`
  pivot seam (#4885/#4892).
- `Service` — ClusterIP only. **No NodePort ever (§854 hard ban).**
- `HTTPRoute` — Gateway API, **never a Traefik Ingress**. Sovereign mode:
  `mcp.<sovereign-fqdn>` on `kube-system/cilium-gateway`; per-Org installs
  override to `cilium-gateway-console` (#4054). Fail-closed: no hostname →
  no route (Inviolable #4).
- `ServiceAccount` — zero RBAC, no token mount (see above).
- `CiliumNetworkPolicy` ×2 — gateway-entity ingress admit (the reserved
  `ingress` identity no K8s NetworkPolicy can express, #3374/#4180) +
  egress (cluster/world:443 + **load-bearing DNS carve-out**, #4604). Both
  Capabilities-gated on cilium.io/v2 so kind CI renders.
- `ConfigMap` (optional, `chepherd.attach.enabled`) — the mcpServers stanza
  for chepherd's `CHEPHERD_EXTRA_MCP_JSON` seam.

## Recorded deviations from #3988

1. **§4.4 `dependsOn bp-chepherd`** — no bp-chepherd blueprint/slot exists
   in this kit (chepherd ships bundled inside the agenity image). The
   chepherd attach is an OPTIONAL values-gated ConfigMap and the chart is
   standalone-capable in sovereign mode. When a bp-chepherd Blueprint
   lands, flip `blueprint.yaml` `depends` + `chepherd.attach.enabled`.
2. **§4.2 per-realm JWKS** — the binary verifies against a single RS256
   pubkey (the Sovereign handover signer that mints catalyst-api session
   bearers, #4114/#4276) plus the `auth.expectedIssuer` exact-issuer pin
   and the `organization.orgSlug` scope pin. Full per-realm JWKS caching is
   the documented follow-up.
3. **§4.5 tool catalog** — the shipped slice is whoami + Org-scoped
   reads + `create_application` (MinTier=Admin). The ~26/~70 catalog +
   the registry-coverage parity test are #3988 DoD-5 follow-ups.

## Verify-key reality (sharp edge)

`auth.rs256PubkeySecret` defaults to Secret `catalyst-handover-jwt-public`,
key `public.jwk` (#5167) — the JWK mirror a Sovereign actually seeds in
`catalyst-system` (where the sovereign-mode slot lands); the ≥0.2.3 binary
parses an RSA JWK natively alongside PKIX/PKCS1 PEM. The aspirational PEM
Secret `catalyst-handover-jwt` / key `handover-jwt-public.pem` referenced by
pre-#5167 defaults is NEVER created on a Sovereign — do not point the chart
back at it. The secretKeyRef renders `optional: true` so an install where
the Secret is absent (e.g. a per-Org install whose namespace has not
installed bp-agenity — see the #5206 per-Org install door above, which
points this same field at the in-namespace `agenity-mcp-bearer` /
`pubkeyPem` Secret instead) still schedules and the binary falls back to
the #5175 whoami-delegation identity path.
