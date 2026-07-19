# bp-openclaw — workspace controller + per-user pod

Catalyst Blueprint for OpenClaw: a multi-tenant **workspace controller**
deployment plus per-user **runtime pods** spawned on demand. Implements
locked decision **[A]** of epic [#795](https://github.com/openova-io/openova/issues/795)
and consumes the per-user `newapi-key-{user-uuid}` Secrets rendered by the
unified-rbac user-create hook ([ADR-0003](../../docs/adr/0003-rbac-newapi-user-create-hook.md)).

---

## Architecture

```
                       openclaw.<org>.<pool>            (Cilium Gateway API HTTPRoute on a Sovereign;
                                                         optional Traefik Ingress off-Sovereign, #4272)
                              │
                              ▼
              ┌──────────────────────────────────┐
              │  Controller Deployment (HA-able) │
              │  - validates Org Keycloak JWT    │
              │  - spawns / proxies / reaps      │
              │    per-user pods                 │
              └─────┬───────────────────┬────────┘
                    │  K8s API           │  WebSocket / SSE proxy
                    ▼                    ▼
        ┌─────────────────────┐    ┌──────────────────────┐
        │  newapi-key-{uuid}  │    │  Per-user runtime    │
        │  Secret (per ADR-3) │    │  Pod (one per active │
        │  - api-key          │◀───│  session). Reads     │
        │  - base-url         │    │  NEWAPI_BASE_URL +   │
        └─────────────────────┘    │  NEWAPI_KEY env. NO  │
                                   │  Keycloak code, NO   │
                                   │  key-mgmt code.      │
                                   └──────────────────────┘
```

**Why this shape (and not the rejected alternative).** A "shared OpenClaw
service forwarding the Org-vcluster JWT to NewAPI" was explicitly rejected
in #795: it would force NewAPI to trust a Keycloak realm it has no
ownership of (cross-realm OIDC trust = identity sprawl). The workspace-
controller pattern keeps NewAPI talking only to its own keys (one per Organization
end-user), and the controller is the *only* component that ever sees a
JWT from the Organization's Keycloak realm.

This pattern generalises to bp-opencode / bp-aider / bp-cursor-server
later — the chart's structure (controller + per-user pod + identity-
blind runtime image) is intentionally reusable.

---

## What this chart contains

| File | Purpose |
|---|---|
| `Chart.yaml` | Metadata; `catalyst.openova.io/no-upstream: "true"` (no upstream Helm chart) |
| `values.yaml` | All operator-tunable values; assertions in `_helpers.tpl` fail render with helpful messages when required values are missing |
| `templates/_helpers.tpl` | Naming / labels / required-value assertions |
| `templates/serviceaccount.yaml` | Controller ServiceAccount (release ns) |
| `templates/controller-rbac.yaml` | Namespaced `Role` + `RoleBinding` in **tenant** ns. `create` verbs split into separate rules WITHOUT `resourceNames` per `feedback_rbac_create_no_resourcenames.md` |
| `templates/controller-deployment.yaml` | Multi-tenant controller pod |
| `templates/controller-service.yaml` | ClusterIP Service for the controller |
| `templates/httproute.yaml` | **(Sovereign exposure)** Cilium Gateway API `HTTPRoute` attaching `openclaw.<org>.<pool>` to the dedicated `cilium-gateway-console` (wildcard `*.<pool>` TLS listener ⇒ no per-host cert). Default `httpRoute.enabled: false`; the org-gitops overlay sets enabled + hostnames (#4272) |
| `templates/controller-ingress.yaml` | **(off-Sovereign only)** Traefik `networking.k8s.io/v1` Ingress with cert-manager auto-issue. Default `ingress.enabled: false` — a Sovereign runs Cilium Gateway, not traefik, so this Ingress is INERT there and its per-host cert never issues (#4272) |
| `templates/per-user-pod-template.yaml` | ConfigMap holding the pod-spec the controller renders per session |
| `templates/networkpolicy.yaml` | Controller K8s `NetworkPolicy` (Pod-selectable hops). Egress now includes the public-host `:443` JWKS hairpin the `/readyz` handler needs (#4272). The per-user pod's NetworkPolicy is rendered by the controller at session-start (see "Per-user pod NetworkPolicy" below) |
| `templates/cilium-ingress-networkpolicy.yaml` | Controller `CiliumNetworkPolicy` `fromEntities:[ingress,host,remote-node]` → `:8080` — admits the Cilium Gateway Envoy (`ingress`) + kubelet readiness/liveness probe (`host`/`remote-node`), reserved entities no K8s `NetworkPolicy` selector can express. Gated on `cilium.io/v2` + `networkPolicy.ingress.allowGatewayEntity` (#4272) |
| `controller/` | Source for the multi-tenant **controller** container image (separate OCI artifact `openclaw-controller`, built by `.github/workflows/openclaw-controller.yaml`). Validates the Organization end-user's Keycloak JWT (OIDC discovery + JWKS RS256), spawns one identity-blind runtime pod per session from the mounted pod-template ConfigMap, reverse-proxies the user to it, and reaps idle pods. |
| `runtime/` | Source for the per-user runtime container image (separate OCI artifact, built by `.github/workflows/openclaw-runtime.yaml`) |
| `tests/render-toggles.sh` | Helm-template integration test exercised by the blueprint-release CI workflow |

---

## Required overlay values

The chart fails to render if any of these are unset (see
`_helpers.tpl :: assertRequired`):

| Value | Example |
|---|---|
| `oidc.issuerURL` | `https://keycloak.acme.<parent-domain>/realms/org-acme` |
| `oidc.clientId` | `openclaw` |
| `oidc.clientSecret.name` | `openclaw-oidc-client-secret` (Secret with key `OIDC_CLIENT_SECRET`) |
| `llm.baseURL` | `https://api.acme.<parent-domain>/v1` (per-tenant NewAPI OpenAI-compatible endpoint) |
| `llm.apiKey.name` | `openclaw-newapi-controller-token` (Secret with key `NEWAPI_KEY`) |
| `llm.defaultModel` | `qwen3.6` (NewAPI maps this to a backing channel — e.g. a partner-hosted Qwen) |
| `tenant.namespace` | `org-acme` |
| `controller.image.tag` | SHA-pinned tag (Inviolable Principle 4) |
| `perUserPod.image.tag` | SHA-pinned tag (Inviolable Principle 4) |
| `ingress.host` | `openclaw.acme.<parent-domain>` |

Legacy `keycloak.*` / `newapi.*` keys remain accepted for back-compat
(see umbrella epic #915).

---

## Runtime image contract

Per locked decision [A] of #795, the per-user runtime image reads **only
two env vars**:

| Env | Source |
|---|---|
| `NEWAPI_BASE_URL` | `secretKeyRef: name=newapi-key-{uuid}, key=base-url` |
| `NEWAPI_KEY` | `secretKeyRef: name=newapi-key-{uuid}, key=api-key` |

It carries **no Keycloak code, no key-management code, no knowledge of
the Organization model**. Identity-blind by construction.

The `runtime/` directory in this Blueprint ships a minimal reference
implementation that satisfies the contract: a Go binary exposing an
HTTP `/healthz` and a `/v1/chat/completions` proxy that forwards to
NewAPI with the injected key. Operators may override
`perUserPod.image.repository` to point at any other image satisfying the
same env-vars contract — e.g. a fork of upstream OpenClaw, an
OpenAI-compatible coding-CLI image, etc.

### Why a stub instead of the upstream OpenClaw

Upstream `OpenClaw` (https://openclaw.ai) targets messaging-platform
integration (WhatsApp / Telegram / Slack) — a different shape from the
"per-user agentic workspace" that #795 calls for. There's no upstream
Helm chart, and the upstream container expects a wholly different
configuration surface (no `NEWAPI_BASE_URL` env). Rather than fork the
upstream and graft a NewAPI driver onto it (significant work, owned by
a different ticket), this Blueprint ships a contract-minimal runtime:
identity-blind, two env vars, OpenAI-compatible proxy. Future work can
swap the runtime image without changing this chart.

---

## RBAC posture

The controller's `Role` lives in the **tenant** namespace (where the
per-user pods and Secrets live), not the release namespace. The
`RoleBinding` subject is the controller's ServiceAccount in the release
namespace.

`create` verbs are **split into their own rules** with no
`resourceNames`. This is mandatory: the K8s authorizer rejects `create`
combined with `resourceNames` (you can't constrain a not-yet-existing
resource by name). Label-based ownership (`catalyst.openova.io/openclaw-user`)
is enforced at the controller, not in RBAC. See
`feedback_rbac_create_no_resourcenames.md` for the full incident report.

---

## Per-user pod NetworkPolicy

This chart's `networkpolicy.yaml` covers the **controller** pod only.
Each per-user pod gets its own NetworkPolicy applied by the controller
at session-start, restricting egress to:

- NewAPI (operator's customer-facing hostname or in-cluster Service)
- DNS (kube-system :53)

The per-user pod NetworkPolicy is rendered by the controller from a
template baked into its container image; it cannot be a static chart
template because the egress target (specifically the NewAPI hostname)
is read from the per-user `newapi-key-{uuid}` Secret, which doesn't
exist at chart-render time.

---

## Build + publish

The chart is built and published by the existing event-driven CI
workflow `.github/workflows/blueprint-release.yaml` whenever
`platform/openclaw/chart/**` changes on `main`. Output:

```
oci://ghcr.io/openova-io/bp-openclaw:<semver>
```

Two sister workflows build the container images, both event-driven (per
Inviolable Principle 1 — no `schedule:` cron; `workflow_dispatch` is for
re-runs only):

| Workflow | Trigger | Output |
|---|---|---|
| `openclaw-controller.yaml` | push to `platform/openclaw/controller/**` | `ghcr.io/openova-io/openova/openclaw-controller:<sha>` |
| `openclaw-runtime.yaml` | push to `platform/openclaw/runtime/**` | `ghcr.io/openova-io/openova/openclaw-runtime:<sha>` |

The controller workflow additionally bumps the chart in lockstep:
after publishing the SHA-pinned image it rewrites
`chart/values.yaml` (`controller.image.tag` → the new SHA,
`perUserPod.image.tag` → the latest published runtime SHA), bumps
`chart/Chart.yaml` + `blueprint.yaml` `version`, bumps the catalog-seed
`bp-openclaw` version pins, and dispatches `blueprint-release`. This keeps
the fresh-Org chart pin pointing at an OCI artifact whose image tags
actually exist on GHCR — never the placeholder (issue #4249) and never
a same-version mutable-tag overwrite (issue #4257).

---

## Related

- Epic [#795](https://github.com/openova-io/openova/issues/795) — SME-tenant turnkey experience
- ADR-0003 — RBAC ↔ NewAPI user-create hook
- bp-newapi (`platform/newapi/`) — Sovereign-level metered LLM gateway
- bp-keycloak (`platform/keycloak/`) — Org-vcluster realm
