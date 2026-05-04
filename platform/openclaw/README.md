# bp-openclaw — workspace controller + per-user pod

Catalyst Blueprint for OpenClaw: a multi-tenant **workspace controller**
deployment plus per-user **runtime pods** spawned on demand. Implements
locked decision **[A]** of epic [#795](https://github.com/openova-io/openova/issues/795)
and consumes the per-user `newapi-key-{user-uuid}` Secrets rendered by the
unified-rbac user-create hook ([ADR-0003](../../docs/adr/0003-rbac-newapi-user-create-hook.md)).

---

## Architecture

```
                       openclaw.<sme-domain>            (Traefik ingress + cert-manager TLS)
                              │
                              ▼
              ┌──────────────────────────────────┐
              │  Controller Deployment (HA-able) │
              │  - validates SME Keycloak JWT    │
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
service forwarding the SME-vcluster JWT to NewAPI" was explicitly rejected
in #795: it would force NewAPI to trust a Keycloak realm it has no
ownership of (cross-realm OIDC trust = identity sprawl). The workspace-
controller pattern keeps NewAPI talking only to its own keys (one per SME
end-user), and the controller is the *only* component that ever sees a
JWT from the SME's Keycloak realm.

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
| `templates/controller-ingress.yaml` | Public hostname `openclaw.<sme-domain>` with cert-manager auto-issue |
| `templates/per-user-pod-template.yaml` | ConfigMap holding the pod-spec the controller renders per session |
| `templates/networkpolicy.yaml` | Controller NetworkPolicy. The per-user pod's NetworkPolicy is rendered by the controller at session-start (see "Per-user pod NetworkPolicy" below) |
| `runtime/` | Source for the per-user runtime container image (separate OCI artifact, built by `.github/workflows/openclaw-runtime.yaml`) |
| `tests/render-toggles.sh` | Helm-template integration test exercised by the blueprint-release CI workflow |

---

## Required overlay values

The chart fails to render if any of these are unset (see
`_helpers.tpl :: assertRequired`):

| Value | Example |
|---|---|
| `keycloak.realmURL` | `https://keycloak.acme.<otech-fqdn>/realms/acme` |
| `keycloak.clientSecretName` | `openclaw-oidc` (ExternalSecret with key `OIDC_CLIENT_SECRET`) |
| `tenant.namespace` | `sme-acme` |
| `newapi.baseURL` | `https://newapi.<otech-fqdn>` |
| `controller.image.tag` | SHA-pinned tag (Inviolable Principle 4) |
| `perUserPod.image.tag` | SHA-pinned tag (Inviolable Principle 4) |
| `ingress.host` | `openclaw.acme.<otech-fqdn>` |

---

## Runtime image contract

Per locked decision [A] of #795, the per-user runtime image reads **only
two env vars**:

| Env | Source |
|---|---|
| `NEWAPI_BASE_URL` | `secretKeyRef: name=newapi-key-{uuid}, key=base-url` |
| `NEWAPI_KEY` | `secretKeyRef: name=newapi-key-{uuid}, key=api-key` |

It carries **no Keycloak code, no key-management code, no knowledge of
the SME tenant model**. Identity-blind by construction.

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
oci://ghcr.io/openova-io/bp-openclaw:0.1.0
```

The runtime image is built by a sister workflow,
`.github/workflows/openclaw-runtime.yaml`, on push to
`platform/openclaw/runtime/**`. Output:

```
ghcr.io/openova-io/openova/openclaw-runtime:<sha>
```

Per Inviolable Principle 1, both workflows are event-driven; neither
uses `schedule:` cron. `workflow_dispatch` is provided for re-runs only.

---

## Related

- Epic [#795](https://github.com/openova-io/openova/issues/795) — SME-tenant turnkey experience
- ADR-0003 — RBAC ↔ NewAPI user-create hook
- bp-newapi (`platform/newapi/`) — Sovereign-level metered LLM gateway
- bp-keycloak (`platform/keycloak/`) — SME-vcluster realm
