# ADR-0003: RBAC ↔ NewAPI User-Create Hook Contract

| | |
|---|---|
| **Status** | Accepted — 2026-05-04 |
| **Authors** | hatiyildiz, Claude (Opus 4.7) |
| **Date** | 2026-05-04 |
| **Supersedes** | — |
| **Superseded by** | — |
| **Related** | [ADR-0001](0001-catalyst-control-plane-architecture.md), [ADR-0002](0002-post-handover-sovereignty-cutover.md), #795, #796, #798, #799, #802, #803 |

---

> **Status (2026-08-26) — errata (ADR body is immutable; notes-only):**
> - **Consolidated links:** every `../INVIOLABLE-PRINCIPLES.md` reference below points to a file folded into [`../PRINCIPLES.md`](../PRINCIPLES.md) under the lean-doc strategy — read each "Inviolable Principle N" link as pointing there.
> - **Terminology predates the GLOSSARY lock:** this ADR uses "tenant" / "SME tenant" throughout; the canonical term is **Organization** (`docs/GLOSSARY.md`). The historical body is left verbatim.

## 1. Status

Accepted 2026-05-04. This ADR extends [ADR-0001](0001-catalyst-control-plane-architecture.md); it does not contradict it. It is the **contract** to which the SME-tier turnkey-experience epic ([#795](https://github.com/openova-io/openova/issues/795)) binds. Downstream tickets that consume this contract:

- [#798](https://github.com/openova-io/openova/issues/798) — NewAPI metering integration
- [#799](https://github.com/openova-io/openova/issues/799) — `bp-newapi` maturation + first-otech deployment (provides the `catalyst-newapi-admin-token` Secret)
- [#802](https://github.com/openova-io/openova/issues/802) — Unified RBAC SME-tier (the service that **owns** this hook)
- [#803](https://github.com/openova-io/openova/issues/803) — `bp-openclaw` workspace controller (consumes the `newapi-key-{user-uuid}` Secret rendered by step 3)

Future changes to the hook's contract require a follow-up ADR; opaque consumer rewrites do not.

## 2. Context

### 2.1 The user-create flow

A Sovereign Marketplace (SME) admin signs into the unified RBAC console (delivered by [#802](https://github.com/openova-io/openova/issues/802)) at `console.<sme-domain>` and creates a user — name, email, role assignments. From the admin's mental model, this is **one action**. From the platform's perspective, the user must materialise across three independent systems before the new user can sign in:

| # | System | Where | Why it must exist |
|---|---|---|---|
| 1 | SME-vcluster Keycloak | Inside the SME tenant's vCluster (per [Inviolable Principle 7](../INVIOLABLE-PRINCIPLES.md), tenancy is K8s-native) | Identity. The user logs into OpenClaw / WordPress / unified RBAC console via OIDC against this realm. |
| 2 | NewAPI admin API | OTECH control plane, in-cluster only (`http://newapi.newapi.svc`) | Per-user LLM gateway record + per-user API key. NewAPI meters usage by API key; the key's lineage to the SME user is what makes billing reconcile. |
| 3 | K8s `Secret` in the SME tenant namespace | Tenant ns of the OTECH cluster, name `newapi-key-{sme-user-uuid}` | Credential delivery. The OpenClaw per-user pod ([#803](https://github.com/openova-io/openova/issues/803)) mounts this Secret as `NEWAPI_KEY` env. Per [Inviolable Principle 7](../INVIOLABLE-PRINCIPLES.md), Secret-as-truth is the K8s-native delivery mechanism. |

### 2.2 Locked decisions inherited from [#795](https://github.com/openova-io/openova/issues/795)

These are settled by the parent epic and **not relitigated** here:

- **[A]** OpenClaw deployment shape = workspace controller (multi-tenant, SME-vcluster Keycloak OIDC) + per-user pod with `NEWAPI_BASE_URL` + `NEWAPI_KEY` env injected from `newapi-key-{user-uuid}` Secret.
- **[B]** The user-create hook fires from the **unified-rbac service in the OTECH control plane**, never from inside the SME vCluster. This is critical: NewAPI's admin API is exposed only at `http://newapi.newapi.svc` (no ingress, no cross-cluster trust). Any hook that originates inside the SME vCluster would force NewAPI's admin surface to be exposed across the vCluster boundary, dragging SME-realm trust onto NewAPI and violating the principle that the OTECH control plane holds cross-cluster credentials.
- **[Q-mine-3]** Event bus = NATS JetStream. Subject conventions per [`core/services/shared/events/topics.go`](../../core/services/shared/events/topics.go).
- **[Q-mine-4]** One ledger. `sme_billing.credit_ledger` is authoritative; NewAPI's Postgres ledger is a thin in-flight cache only — billing materialises from NATS events, not from NewAPI's database.

### 2.3 Constraints from [ADR-0001](0001-catalyst-control-plane-architecture.md)

- **Inviolable Principle 1** — event-driven, never polling. The reconciliation loop uses a NATS subject that the unified-rbac controller publishes to itself; no `time.Tick`, no Kubernetes `CronJob`.
- **Inviolable Principle 3** — no `exec.Command("kubectl", ...)`, no shell-out. The K8s `Secret` apply uses `client-go` server-side apply.
- **Inviolable Principle 7** — tenancy is K8s-native; per-user credentials live in K8s Secrets in the tenant namespace, labelled for ownership and selectability.
- **§6 NATS event emission** — `sme.user.events` (existing topic, see `topics.go`) is the canonical channel for user-lifecycle events. Billing, audit, notification, and the unified RBAC UI all subscribe.

### 2.4 Why three distinct systems instead of one

A single transactional API across Keycloak + NewAPI + the K8s api-server is not achievable: three different storage engines (Postgres, Postgres, etcd) with no shared 2-PC coordinator. Reaching for distributed-transaction primitives (XA, sagas-with-compensation libraries) is overkill for a 3-step orchestration that runs at human-action cadence (not transaction-per-second). Idempotent steps + persisted state machine + reconciliation loop is the right shape — exactly the pattern Crossplane uses for cloud-resource provisioning, applied to a much smaller surface.

## 3. Decision

The user-create hook is a **3-step orchestration owned by the unified-rbac service in the OTECH control plane**. Each step is independently idempotent; the orchestration retries transient errors and is recoverable from any partial state. State is persisted in unified-rbac's Postgres so a pod restart never strands a half-provisioned user.

### 3.1 Step 1 — Keycloak user create

| | |
|---|---|
| **Method** | `POST` |
| **URL** | `{SME_VCLUSTER_KEYCLOAK_URL}/admin/realms/{realm}/users` |
| **Auth** | `Authorization: Bearer <kc_sa_token>` — service-account token already provisioned for unified-rbac to manage SME realms (rotated by the existing `bp-keycloak` ExternalSecret pipeline). |
| **Idempotency header** | `X-Idempotency-Key: <sme_user_uuid>` |
| **Content-Type** | `application/json` |

Body schema (precise JSON shape — agents writing client code must match this exactly):

```json
{
  "username": "<email>",
  "email": "<email>",
  "emailVerified": true,
  "enabled": true,
  "attributes": {
    "sme_tenant_id":  ["<sme_tenant_id>"],
    "sme_user_uuid":  ["<sme_user_uuid>"]
  },
  "groups": ["sme-users"]
}
```

Success response: `201 Created`, with `Location: /admin/realms/{realm}/users/{kc_user_id}` header. `kc_user_id` is captured for step 2.

Idempotent retry path: on `409 Conflict` (user with this username already exists), follow up with `GET /admin/realms/{realm}/users?username=<email>&exact=true` and capture the existing `id` field. Treat as success.

### 3.2 Step 2 — NewAPI user create

| | |
|---|---|
| **Method** | `POST` |
| **URL** | `http://newapi.newapi.svc/api/v1/admin/users` (in-cluster Service DNS — never an Ingress hostname) |
| **Auth** | `Authorization: Bearer <CATALYST_NEWAPI_ADMIN_TOKEN>` — value from the `catalyst-newapi-admin-token` ExternalSecret rendered by [#799](https://github.com/openova-io/openova/issues/799). |
| **Idempotency header** | `X-Idempotency-Key: <sme_user_uuid>` |
| **Content-Type** | `application/json` |

Body schema:

```json
{
  "external_id": "<sme_user_uuid>",
  "email":       "<email>",
  "tenant_id":   "<sme_tenant_id>",
  "tier":        "default",
  "metadata": {
    "kc_user_id": "<from step 1>",
    "kc_realm":   "<sme_vcluster_realm>"
  }
}
```

Success response: `201 Created` with body `{"user_id": "<newapi_user_id>", "api_key": "<per_user_api_key>", "created_at": "..."}`.

Idempotent retry path: on `409 Conflict` (`external_id` already exists), follow up with `GET /api/v1/admin/users?external_id=<sme_user_uuid>` and capture `{user_id, api_key}` from the existing record.

**Decision on duplicate `external_id`: NewAPI returns the *existing* `api_key` — it does not rotate.** Rotation on conflict would orphan the K8s Secret rendered by step 3 and force a Secret-update path through every retry, multiplying failure modes. Return-existing keeps the (NewAPI api_key) ↔ (K8s Secret) binding 1:1 for the lifetime of the user. Explicit key rotation has its own admin endpoint (`POST /api/v1/admin/users/{user_id}/keys/rotate`, out of scope for this ADR — surfaced as the "regenerate key" action in unified RBAC UI in [#802](https://github.com/openova-io/openova/issues/802)).

### 3.3 Step 3 — K8s Secret apply

| | |
|---|---|
| **Method** | server-side apply (`Apply` verb, field manager `unified-rbac`) |
| **API client** | `k8s.io/client-go` against the OTECH cluster's api-server using unified-rbac's pod ServiceAccount token. **Never `exec.Command("kubectl", ...)` — see [Inviolable Principle 3](../INVIOLABLE-PRINCIPLES.md).** |
| **RBAC** | A namespaced `Role` bound to the unified-rbac ServiceAccount in **each** SME tenant namespace, scoped to `secrets:get,list,watch,create,update,patch` on names matching `newapi-key-*`. Provisioned at SME tenant onboarding, not at user-create time. |

Resource:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: newapi-key-<sme_user_uuid>
  namespace: <sme_tenant_namespace>
  labels:
    catalyst.openova.io/sme-tenant:    <sme_tenant_id>
    catalyst.openova.io/sme-user-uuid: <sme_user_uuid>
    catalyst.openova.io/managed-by:    unified-rbac
type: Opaque
stringData:
  api-key:  <newapi_api_key>
  base-url: https://newapi.<otech_fqdn>
```

The `base-url` field is the **customer-facing** NewAPI hostname (egress through the OTECH ingress with TLS), not the in-cluster Service URL. The OpenClaw per-user pod ([#803](https://github.com/openova-io/openova/issues/803)) consumes both keys as env vars.

Idempotency: server-side apply with a stable field manager is naturally idempotent — re-applies converge. Conflicts with another field manager are surfaced as terminal errors (would indicate a different controller is mutating the same Secret, which is a configuration bug, not transient).

### 3.4 Reconciliation state machine

State persisted in unified-rbac's Postgres:

```sql
CREATE TABLE user_provision_state (
  sme_user_uuid    UUID PRIMARY KEY,
  sme_tenant_id    TEXT NOT NULL,
  email            TEXT NOT NULL,
  state            TEXT NOT NULL,          -- pending | kc_created | newapi_created | secret_applied | done | failed
  kc_user_id       TEXT,
  newapi_user_id   TEXT,
  retry_count      INT  NOT NULL DEFAULT 0,
  last_error       TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

States and transitions:

```
                                     ┌── 5 transient failures ──▶ failed
                                     │
pending ──▶ kc_created ──▶ newapi_created ──▶ secret_applied ──▶ done
   │             │                │                  │
   └─ retry ◀────┴────────────────┴──────────────────┘  (idempotent re-run from current state)
```

- On reconcile of `pending`: re-run from step 1.
- On reconcile of `kc_created`: re-run from step 2 (step 1 idempotent if executed again).
- On reconcile of `newapi_created`: re-run from step 3.
- On reconcile of `secret_applied`: mark `done`, emit the NATS event (§3.6), and stop.
- On 5 consecutive failures of the same step: mark `failed`, surface in the unified RBAC console for the SME admin to retry or delete.

### 3.5 Reconciliation trigger — event-driven, not cron

A goroutine inside the unified-rbac controller publishes a heartbeat envelope on NATS subject `unified-rbac.reconcile-pending` every 30 s; a consumer in the same service (durable consumer, max-deliver=1, ack-wait=20 s) handles the heartbeat by querying `user_provision_state` for rows in any state other than `done` or `failed` whose `updated_at` is older than the per-state retry interval, and re-runs each.

This is a NATS heartbeat — **not** a Kubernetes `CronJob`, which would violate the architecture's event-driven posture (per [Inviolable Principle 1](../INVIOLABLE-PRINCIPLES.md) and [ADR-0001 §6](0001-catalyst-control-plane-architecture.md)). The advantage of self-publishing on NATS: cross-replica consistency is automatic (whichever unified-rbac replica the consumer message lands on processes the batch), and the heartbeat itself is observable in NATS metrics for ops.

### 3.6 NATS event emission

On the `secret_applied → done` state transition, unified-rbac publishes one envelope on subject `sme.user.events` (the canonical topic per [`topics.go`](../../core/services/shared/events/topics.go)):

```json
{
  "id":        "<uuid>",
  "type":      "sme.user.created",
  "source":    "unified-rbac",
  "timestamp": "2026-05-04T12:34:56Z",
  "tenant_id": "<sme_tenant_id>",
  "data": {
    "sme_user_uuid":  "<uuid>",
    "email":          "<email>",
    "kc_user_id":     "<kc-uuid>",
    "newapi_user_id": "<newapi-uuid>",
    "secret_name":    "newapi-key-<uuid>"
  }
}
```

Subscribers (existing or to-be-built):

- **billing** — opens a customer record in `sme_billing.credit_ledger` with the operator's plan starting balance. Per [Q-mine-4](https://github.com/openova-io/openova/issues/795), the credit ledger is authoritative; NewAPI's own ledger is a thin in-flight cache.
- **notification** — sends the welcome email + magic-link first-login.
- **audit** — appends to the long-retention audit stream.
- **unified RBAC UI** — receives an SSE-relayed update so the admin's console reflects the user as "ready" (state=done) without a refresh.

### 3.7 Rollback contract — delete user

When the SME admin deletes a user, the inverse orchestration runs:

```
1. Delete K8s Secret newapi-key-<uuid>     (server-side delete; idempotent)
2. Revoke NewAPI api_key                    (POST /api/v1/admin/users/<id>/disable; best-effort)
3. Delete Keycloak user                     (DELETE /admin/realms/<realm>/users/<kc_id>; idempotent)
4. Mark user_provision_state.state=deleted  (audit row preserved)
5. Emit sme.user.deleted on sme.user.events
```

Each step is idempotent. Partial rollback (e.g., Secret deleted but Keycloak revoke failed) is recoverable on the next reconcile pass — the reconciliation loop handles the inverse direction the same way it handles forward.

### 3.8 Errors taxonomy

| Class | Examples | Strategy |
|---|---|---|
| **Transient** | `5xx` from any of the 3 APIs; network/DNS failure; timeout | Exponential backoff: 1 s, 2 s, 4 s, 8 s, 16 s. Max 5 retries before promoting to `failed`. |
| **Conflict / already-exists** | `409 Conflict` from Keycloak; `409` from NewAPI; K8s `AlreadyExists` (for non-SSA paths) | Treat as success; capture the existing resource ID via the documented GET; advance state machine. |
| **Terminal** | `401`/`403` (auth misconfigured — service-account or admin token broken); `4xx` other than `409` (malformed payload — programming bug); K8s namespace not found | Mark `failed`; surface to UI with structured error. No retry. Surfaces as an actionable alert: a real human (the SME admin or OTECH operator) needs to fix configuration. |

A failed state must always carry a structured `last_error` field that:

1. Names the step (`kc_create`, `newapi_create`, `secret_apply`).
2. Names the error class (`transient`, `conflict`, `terminal`).
3. Quotes the upstream HTTP status + first 256 bytes of response body.

## 4. Consequences

### 4.1 Positive

- **Single mental model for the SME admin.** Create user once; three systems materialise atomically (eventually, with reconciliation guarantees). The complexity of cross-system orchestration stays inside unified-rbac, not in the admin UI's UX.
- **NewAPI admin API stays in the OTECH control plane.** No ingress, no Spire mTLS bridge across the vCluster boundary, no SME-realm trust dragged onto NewAPI. This is the architectural property that makes NewAPI safe to centralise across SME tenants.
- **One service holds all cross-cluster credentials.** unified-rbac is the only component that needs to authenticate against (a) the SME-vcluster Keycloak admin API, (b) the NewAPI admin API, and (c) the OTECH cluster's api-server. Concentrating that trust in one well-audited service is preferable to distributing it.
- **Recovery from partial failure is automatic.** Pod restart, Postgres failover, NATS broker failover — none of these can leave a half-provisioned user permanently. The reconciler will pick up where the failed pod left off.
- **NATS event publication makes downstream subscriptions cheap.** Billing, audit, notification, and unified RBAC UI all subscribe to `sme.user.events` instead of bespoke webhooks.

### 4.2 Negative

- **unified-rbac becomes the trust boundary for SME user provisioning.** Its compromise compromises every SME tenant's NewAPI keys, every SME tenant's Keycloak user creation. Mitigations baked into the design:
  - Minimum-RBAC namespaced `Role` per SME tenant namespace (not a cluster-scoped role).
  - The pod runs non-root with read-only root filesystem.
  - Every Secret apply emits an audit row to the long-retention stream — a compromised unified-rbac can be detected via anomalous apply rates.
  - The Keycloak service-account token and `CATALYST_NEWAPI_ADMIN_TOKEN` are rotated by the existing ExternalSecret pipeline, not embedded in code or images.
- **Three-system orchestration adds ~50 LoC of state-machine code.** Not zero cost, but well-bounded and testable.
- **Reconciliation latency is bounded by the 30-second heartbeat interval.** A user created during a transient outage may take up to ~30 s after the outage clears to materialise everywhere. Acceptable for human-cadence admin actions; not acceptable if this contract is ever extended to programmatic high-rate creation (out of scope for SME tier).

### 4.3 Operational

- A new Postgres table (`user_provision_state`) in the unified-rbac database. ~6 columns; no schema-evolution risk.
- A new NATS subject (`unified-rbac.reconcile-pending`); private to unified-rbac, not consumed elsewhere.
- A new field manager (`unified-rbac`) on the K8s api-server's server-side-apply records — no operational concern, just a label that aids `kubectl get -o yaml` readability.

## 5. Alternatives Considered

### 5.1 Hook fires from inside the SME vCluster

**Rejected per [B] of [#795](https://github.com/openova-io/openova/issues/795).** Forcing the hook to originate inside the SME vCluster would expose NewAPI's admin API across the vCluster boundary — either via a NetworkPolicy hole, an exposed Service, or a tunnelled Spire mTLS bridge. Each of these drags SME-realm trust onto NewAPI, violates the principle that the OTECH control plane holds cross-cluster credentials, and increases the attack surface against NewAPI's admin endpoints. unified-rbac in the OTECH control plane is the correct trust boundary.

### 5.2 Single transactional API across the three systems

**Rejected.** Three independent storage engines (Keycloak's Postgres, NewAPI's Postgres, K8s etcd) cannot participate in a 2-phase commit, and Saga-with-compensation libraries are overkill for a 3-step human-cadence flow. Idempotent steps + persisted state machine + reconciliation loop is the right shape — it's exactly the pattern Crossplane uses for cloud-resource provisioning, applied to a smaller surface.

### 5.3 NewAPI rotates `api_key` on duplicate `external_id`

**Rejected.** Rotation on conflict orphans the K8s Secret rendered by step 3 and forces a Secret-update path through every retry. Return-existing keeps (NewAPI key) ↔ (K8s Secret) bound 1:1 for the user's lifetime. Operator-initiated rotation has a separate admin endpoint and is surfaced as a deliberate action in the unified RBAC UI.

### 5.4 Direct `kubectl` shell-out for the Secret apply

**Rejected per [Inviolable Principle 3](../INVIOLABLE-PRINCIPLES.md).** `exec.Command("kubectl", ...)` from a Go service is forbidden — it opaque-wraps the K8s api-server, hides errors, prevents structured handling, and creates dependency on a `kubectl` binary in the container image. `client-go` server-side apply is the canonical path.

### 5.5 Kubernetes `CronJob` reconciler

**Rejected per [Inviolable Principle 1](../INVIOLABLE-PRINCIPLES.md) and [ADR-0001 §6](0001-catalyst-control-plane-architecture.md).** A CronJob is a polling pattern; it's also operationally noisy (one Job pod per tick, per replica). The NATS-heartbeat-to-self pattern is event-driven, observable in standard NATS metrics, and naturally cross-replica-consistent.

### 5.6 Synchronous "wait for everything before returning to the admin"

**Rejected.** Holding the admin's HTTP request open until all three systems acknowledge (worst case: ~10 s on a cold path with retries) makes the UX brittle to any single-system slowness. Better: return `202 Accepted` immediately with the `sme_user_uuid`, let the admin UI subscribe to SSE for the `sme.user.created` event, and render the user as "provisioning..." until the event arrives. The state machine documented here makes that UX safe — even on disconnect, the user will land in `done` state asynchronously.

---

## 6. Implementation pointers

| Concern | Where |
|---|---|
| Hook owner service | unified-rbac in OTECH control plane (delivered + extended in [#802](https://github.com/openova-io/openova/issues/802)) |
| Postgres state table | `user_provision_state` in unified-rbac's CNPG database |
| NATS subject for reconcile heartbeat | `unified-rbac.reconcile-pending` (private to unified-rbac) |
| NATS subject for user events | `sme.user.events` (existing, see [`topics.go`](../../core/services/shared/events/topics.go)) |
| Event envelope | [`core/services/shared/events/events.go`](../../core/services/shared/events/events.go) `Event` struct |
| K8s Secret name pattern | `newapi-key-<sme_user_uuid>` in the SME tenant namespace |
| K8s Secret labels | `catalyst.openova.io/sme-tenant`, `catalyst.openova.io/sme-user-uuid`, `catalyst.openova.io/managed-by=unified-rbac` |
| NewAPI admin token | `catalyst-newapi-admin-token` ExternalSecret (provisioned by [#799](https://github.com/openova-io/openova/issues/799)) |
| Keycloak SA token | existing `bp-keycloak` ExternalSecret pipeline |
| RBAC for unified-rbac in tenant ns | namespaced `Role` on `secrets` (verbs: `get,list,watch,create,update,patch`) bound at SME-tenant onboarding |

---

*Part of [OpenOva](https://openova.io). Read in conjunction with [ADR-0001](0001-catalyst-control-plane-architecture.md), [ADR-0002](0002-post-handover-sovereignty-cutover.md), and [`INVIOLABLE-PRINCIPLES.md`](../INVIOLABLE-PRINCIPLES.md).*
