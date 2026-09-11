# bp-chargeback — chart design

**What this is**: the install path for the chargeback application (ADR-0014,
EPIC #6723) — a standalone application that meters cloud usage per customer,
rates it against a price book and produces statements. One binary (Go service +
embedded UI), its own CNPG database, its own API and screens. It is NOT a
control-plane component of Catalyst; it is a Blueprint like `bp-agenity` or
`bp-openova-mcp`, and its OpenOva integration is an adapter, not a dependency.

## Composable structure (how it plugs into the console)

OpenOva is composed of applications; the sovereign console's left menu binds to
them the same way `bp-agenity` does. `bp-chargeback` declares
`spec.consoleUI.{sidebarEntry,sidebarLabel,sidebarRoute,sidebarIcon}` in its
`blueprint.yaml`; the catalyst-api projects that into
`GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries`, the sovereign console
renders it, and Settings → **Menu** lets the sovereign-admin re-map it
(PR #6724). No console code change is needed to add or move the entry.

| Concern | Where it is decided |
|---|---|
| Composability + left-menu binding | ADR-0014 D9/D9a + `Blueprint.spec.consoleUI` + Settings → Menu |
| Entity types per deployment mode | ADR-0014 D2a |
| Billing source-of-truth per customer type (incl. dual-registered) | ADR-0014 D2b |
| Customer intake / import without duplication | ADR-0014 D8a |
| Collection modes (event-driven vs pulled change log vs sampled) | ADR-0014 D3a |
| UI surface per decision, per profile | ADR-0014 D9a |
| Deployment profiles `sovereign` / `operator-central` | ADR-0014 D10 |

## Entity model (no new platform entity type)

Inside the app the billed party is a `Customer` (an app-internal object, like
posts in WordPress — NOT a Catalyst CRD). Per ADR-0014 D2/D2a:

| Deployment mode | What a `Customer` is |
|---|---|
| Sovereign BSS (adapter on) | synced 1:1 from an Organization slug; plus any external cloud project attached as a cost source |
| Standalone tenant (operator-central) | onboarded directly (create / bulk import / self-service invite, D8a); no Organization exists |
| The Sovereign's own footprint | the `platform` customer (case 3) |

Billing source-of-truth per customer type is D2b: an OpenOva-Sovereign-only
customer bills from its own instance; a National-Cloud-only customer bills from
the central instance; a dual-registered customer is two roles (seller on its own
Sovereign, buyer on the operator's central instance) that exchange statements,
never identity — no duplication across instances.

## What the chart renders

- **Deployment** (2 replicas, Guaranteed QoS, distroless numeric-nonroot uid,
  read-only rootfs, `/healthz`+`/readyz` probes) serving the API + embedded UI.
- **CNPG `Cluster`** (private, gated on the CRD) + placeholder-DSN Secret +
  post-install sync Job — the pattern-A database wiring from `bp-newapi`.
- **HTTPRoute** `chargeback.<sovereign-fqdn>` on the Cilium gateway (fail-closed:
  no resolvable host ⇒ no route), + the two CiliumNetworkPolicies with the
  mandatory DNS carve-out.
- **ServiceAccount** — zero RBAC by default. When `adapter.enabled=true`
  (ADR-0014 D5, the `sovereign` profile) a read-only **ClusterRole** is rendered
  (`orgs.openova.io` + `namespaces`/`pods`/`persistentvolumeclaims`,
  get/list/watch — the least-privilege set the collector needs), the SA token is
  mounted, and `ADAPTER_ENABLED` + `BILLING_HOOK_*` env are wired. The credential
  Secrets named by `costSources[].credentialRef` are granted by per-Organization
  Roles the org-gitops emitter writes on the named Secret (`resourceNames get`),
  never a cluster-wide secrets read.
- **Platform enforcement** (`platformApi.url`, DESIGN.md §9.6, #6867) — when
  set, `PLATFORM_API_URL` is wired and a **projected ServiceAccount token**
  volume (`serviceAccountToken`, `expirationSeconds: 3600`, `audience` only
  when `platformApi.tokenAudience` names one) is mounted read-only at
  `/var/run/secrets/platform-api/token` with `PLATFORM_API_BEARER_FILE`
  pointing at it (`PLATFORM_API_TOKEN_FILE` through chart 0.1.32 — a path,
  not a secret, but the Kyverno `secret-not-in-env` policy keys on the NAME;
  the binary reads the old name as a deprecated alias for one release). The
  binary re-reads that file on every suspend/resume call
  to the sovereign-admin API's `/api/v1/internal/organizations/{slug}/…`
  routes, which verify it with a TokenReview against
  `system:serviceaccount:<namespace>:<sa>`. Empty url ⇒ none of this renders
  and the Enforcer is a Nop. `adapter.billingHook.callbackSecret` names the
  Secret whose `BILLING_HOOK_CALLBACK_SECRET` key signs the billing
  service's payment callbacks — a Secret name, never a literal.

## Deployment profiles

`config.profile` selects `sovereign` (adapter available; syncs Organizations,
runs the platform collector) or `operator-central` (the operator's own central
instance for all its customers; adapter off, customers onboarded directly).
Same chart, same image — the difference is configuration. The engine is
API-first; the UI is one client, so an operator may embed it behind its own
portal instead (ADR-0014 D10).

## Operational notes

- Migrations are embedded Go, applied at startup; the CNPG cluster is backed up
  by barman (Kyverno backup policy exempts `cnpg.io/cluster` PVCs).
- Secrets (per-customer AK/SK) are envelope-encrypted with `APP_ENCRYPTION_KEY`
  and never returned by the API or logged.
- Cutover-safe: image carries the `global.imageRegistry` pivot seam; kit slot
  **13f** ships the HelmRelease.
- **Sovereign compliance** (`bp-kyverno-policies`, hw307 2026-09-11: 7 fails
  in namespace `chargeback`). Each seam is the policy's own accepted shape,
  read from `platform/kyverno-policies/chart/templates/baseline/`:
  - `prometheus-scrape` — pod-template annotations `prometheus.io/scrape: "true"`,
    `prometheus.io/port` (the `http` port), `prometheus.io/path: /metrics`
    (`metrics.*`); the binary serves the exposition itself.
  - `otel-injected` — pod-template annotation
    `instrumentation.opentelemetry.io/inject-go: "opentelemetry/default"`
    (`otel.instrumentation`), naming the Instrumentation CR
    `bp-opentelemetry-operator` renders. The operator's Go injection is an
    eBPF sidecar that also needs `otel-go-auto-target-exe`, which this chart
    never sets — so the webhook injects nothing and the pod keeps its
    non-root, read-only posture.
  - `topology-spread` — `topologySpreadConstraints` on `kubernetes.io/hostname`,
    `maxSkew 1`, `ScheduleAnyway` (`topologySpread.*`); a single-node Sovereign
    still schedules both replicas.
  - `resource-requests` / `resource-limits` — `cnpg.cluster.resources` now
    defaults to 250m/512Mi requests and 1/1Gi limits; CNPG copies them onto
    the instance Pod's postgres container (the Pod the policy evaluates).
  - `secret-not-in-env` — no env whose NAME matches
    `(?i)(PASSWORD|TOKEN|KEY|SECRET)` carries a literal `value:`:
    `PLATFORM_API_BEARER_FILE` (was `PLATFORM_API_TOKEN_FILE`), the app-key
    Job's `TARGET_NAME`/`TARGET_FIELD` (were `SECRET_NAME`/`SECRET_KEY`), the
    DSN-sync Job's `SOURCE_NAME`/`DEST_NAME` (were `SOURCE_SECRET`/`DEST_SECRET`).
    Real secrets stay `secretKeyRef`s.
  - Gate: `tests/kyverno-policies.sh` renders the chart (platformApi on and
    off, callback secret set), stamps the two `helm.toolkit.fluxcd.io/*`
    labels Flux's helm-controller adds at apply time, and runs every
    ClusterPolicy of `bp-kyverno-policies` (canonical actions,
    `bootstrapMode=false`) against the render with the kyverno CLI pinned to
    the Sovereign's Kyverno appVersion. Any `fail` or `error` fails the gate;
    so does an evaluation of zero resources or a policy set missing one of the
    five policies above (vacuity guard).

See [ADR-0014](../../../docs/adr/0014-chargeback-usage-ledger-and-split-deployment.md)
for the full decision record and [`../README.md`](../README.md) for the service
layout.
