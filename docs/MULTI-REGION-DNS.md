# Multi-Region DNS — health-checked failover with PowerDNS lua-records

**Status:** Authoritative. **Updated:** 2026-04-29 (Reconcile Pass 1).

This document is the canonical reference for **how Catalyst routes traffic across regions**. Geographic redundancy in OpenOva is realized at the **authoritative DNS** layer, not at the K8s controller layer. PowerDNS lua-records (`ifurlup`, `ifportup`, `pickclosest`, `pickrandom`, `pickwhashed`) provide everything Catalyst needs:

- **Geo-aware response selection** — answer the closest healthy backend for the resolver's source IP / ECS subnet.
- **Health-checked failover** — drop a backend from the response set when a TCP/HTTP probe fails, restore it when the probe recovers.
- **Latency-aware routing** — combine `ifurlup` (health) with `pickclosest` (geo) for active-active steering.
- **Same operational layer Catalyst already runs** — PowerDNS is bp-powerdns, deployed by the bootstrap kit on every Sovereign's `mgt` cluster. No separate operator, no extra CRDs, no extra reconciliation loop.

This subsumes the role previously assigned to k8gb. The k8gb component has been removed from `componentGroups.ts`, the umbrella chart, and the wizard; lua-records cover every failover scenario k8gb covered without the dedicated GSLB controller.

---

## 1. Why PowerDNS lua-records (and why not k8gb)

| Concern | k8gb (removed) | PowerDNS lua-records (current) |
|---|---|---|
| Authoritative DNS | CoreDNS plugin, separate zone | PowerDNS authoritative — same zones used for `external-dns`, ACME, etc. |
| Operator footprint | k8gb controller + CRDs (`Gslb`, `GslbHttpRoute`) + per-cluster CoreDNS pod set | None — declarative LUA records in the existing PowerDNS zone |
| Health-check primitive | k8gb-managed liveness probes | PowerDNS `ifurlup` / `ifportup` (HTTP / TCP probes from PowerDNS pods) |
| Geo selection | EdgeDNS witness + custom logic | `pickclosest` (geo by source IP), `pickrandom` (RR), `pickwhashed` (sticky weighted) |
| DNSSEC | Layered on top, separate signer | Native — PowerDNS signs the lua-record's computed answer with the zone's KSK/ZSK |
| Operational surface | k8gb pods + CoreDNS pods + custom CRDs | Existing PowerDNS deployment + dnsdist rate-limit shield |
| Cluster-coordination | Required (gslb endpoints sync between clusters) | Not required — authoritative DNS is the source of truth |

The architectural cost difference is large enough that the deletion is the right move per [INVIOLABLE-PRINCIPLES.md](INVIOLABLE-PRINCIPLES.md) #2 ("never compromise from quality — pick the unified primitive, not the dual-shape design") and #4 ("never hardcode — health probes, weights, geo policy are configuration in the lua-record body, not code in a controller").

---

## 2. Failover patterns (the lua-record cookbook)

Every Catalyst Sovereign zone is hosted on PowerDNS. The records below sit alongside ordinary A/AAAA/CNAME records that `external-dns` writes via the PowerDNS REST API. Lua-record syntax follows the [upstream PowerDNS documentation](https://doc.powerdns.com/authoritative/lua-records/index.html).

> **Note on examples.** Backend IPv4 addresses (`5.161.42.18`, `95.217.189.42`) and the FQDN `primary.example.com` below are placeholders — they illustrate the lua-record shape only. The canonical 6-record set per Sovereign zone is written by **pool-domain-manager** (PDM, `core/pool-domain-manager/`) on `/v1/commit`; lua-records (geo / health-check policy) are written by the **catalyst-dns** controller (Catalyst control-plane sidecar) from each Application's Placement spec — see [`docs/PLATFORM-POWERDNS.md`](PLATFORM-POWERDNS.md) §"In-cluster consumers".

### 2.1 Active-active across two regions, health-checked

```
foo.acme.com.  IN  LUA  A "ifurlup('https://primary.example.com/healthz', {'5.161.42.18', '95.217.189.42'}, {selector='all'})"
```

- PowerDNS HTTP-probes `https://primary.example.com/healthz` from each PowerDNS pod every 5s (default; configurable via `interval` option).
- `selector='all'` returns **every** healthy backend — the resolver's stub then picks one (typical client behaviour: rotate, retry on failure).
- When the probe to a backend fails three times in a row (default `failOnIncerror=true`, 3 fails to drop), that backend is removed from the answer set within the next TTL window.
- When the probe recovers, the backend is restored automatically.

### 2.2 Geo-aware active-active (`pickclosest`)

```
api.acme.com.  IN  LUA  A "pickclosest({'5.161.42.18', '95.217.189.42'})"
```

- PowerDNS uses ECS (EDNS Client Subnet) when present, falling back to the resolver's source IP.
- The closer regional LB by GeoIP wins.
- Combine with `ifurlup` for health-aware closeness:

```
api.acme.com.  IN  LUA  A "
  ifurlup('https://primary.example.com/healthz', {
    {'5.161.42.18', '95.217.189.42'}
  }, {selector='pickclosest'})
"
```

### 2.3 Active-passive (primary → DR)

```
api.acme.com.  IN  LUA  A "ifurlup('https://primary.example.com/healthz', {'5.161.42.18', '95.217.189.42'}, {selector='pickfirst'})"
```

- `pickfirst` returns the first healthy backend in the list.
- When `5.161.42.18` (primary) is healthy → answer is `5.161.42.18`.
- When primary fails the probe → answer flips to `95.217.189.42` (DR) within one TTL window.
- When primary recovers → answer flips back to primary on the next probe success.

### 2.4 TCP-only / non-HTTP services (`ifportup`)

For services that don't expose an HTTP `/healthz` (e.g. SMTP, IMAP, custom TCP):

```
mail.acme.com.  IN  LUA  A "ifportup(587, {'5.161.42.18', '95.217.189.42'})"
```

- PowerDNS attempts a TCP connect to port 587 on each backend.
- Connect-fail → drop from the response set; connect-success → include.

### 2.5 Weighted round-robin (`pickwhashed`)

For canary releases or traffic-shifting:

```
api.acme.com.  IN  LUA  A "pickwhashed({{80, '5.161.42.18'}, {20, '95.217.189.42'}})"
```

- 80% of distinct client IPs are pinned to `5.161.42.18`, 20% to `95.217.189.42` (consistent hash on source IP — the same client gets the same answer until the weight changes).

---

## 3. Catalyst integration points

### 3.1 Where lua-records are written

Lua-records are part of each Sovereign's PowerDNS zone, alongside the canonical 6-record set ([`PLATFORM-POWERDNS.md`](PLATFORM-POWERDNS.md) §"Per-Sovereign zone model"). The 6-record set is written once at provisioning by **pool-domain-manager** (PDM `/v1/commit`); ongoing A/AAAA/CNAME records are written by **external-dns**; LUA records are written by the **catalyst-dns** controller (sidecar to the Catalyst control plane on the `mgt` cluster):

```
PDM         ──► PowerDNS REST API ──► canonical 6-record set (one-shot at provision)
external-dns ──► PowerDNS REST API ──► A/AAAA/CNAME records (per-region LB IPs)
catalyst-dns ──► PowerDNS REST API ──► LUA records (geo / health-check policy)
```

This separation matters: `external-dns` knows about a single K8s Service or Ingress; it has no concept of multi-region health policy. The catalyst-dns controller reads the Application's **Placement** field from the per-Org Gitea repo, sees `placement: active-active` (or `active-hotstandby`, etc.), and synthesizes the corresponding lua-record body.

### 3.2 Application Placement → lua-record selector mapping

| Application Placement | lua-record idiom |
|---|---|
| `single-region` | Plain A record(s) — no lua-record needed |
| `active-active` | `ifurlup(..., {selector='all'})` (or `selector='pickclosest'` for geo-affinity) |
| `active-hotstandby` | `ifurlup(..., {selector='pickfirst'})` — primary first, DR second |
| `active-passive-warm` | `ifurlup(..., {selector='pickfirst'})` + longer TTL (manual operator promotion is the contract; the LUA only flips when the probe fails enough times) |
| `weighted-canary` | `pickwhashed({{w1, ip1}, {w2, ip2}})` — adjust weights via Catalyst console (re-emits the lua-record body with new weights) |

### 3.3 Probe target

Every Catalyst Application Blueprint MUST expose `/healthz` on its public endpoint. The catalyst-dns controller defaults to `https://<app-fqdn>/healthz` as the probe target, configurable per-Application via `spec.healthCheck.path` in the Blueprint instance.

DNS pods are inside the Sovereign — they probe **outbound** to the regional LB IPs over the public internet (or via the Cilium Cluster Mesh + WireGuard back-channel for cross-region private probes). The probe direction is intentional: DNS pods are the source of truth on whether a regional LB is reachable from the same place the public internet would reach it.

### 3.4 Split-brain protection (failover-controller)

Lua-records are necessary but not sufficient for split-brain protection during a network partition. The [failover-controller](../platform/failover-controller/README.md) layers a **lease-based witness** on top:

- During healthy operation, each regional cluster renews a lease in a cloud witness (Cloudflare KV or similar — out of band from the Sovereign's own infra).
- The PowerDNS lua-record probes are the *primary* failover signal (sub-minute response).
- The lease becomes the *tie-breaker* for stateful promotion (OpenBao DR, CNPG primary promotion) — only the cluster holding a valid lease is allowed to take over write authority.
- See [`SRE.md`](SRE.md) §2.4 for the witness protocol; this doc covers only the DNS-routing half.

---

## 4. When to add a second Sovereign region (the HA upgrade path)

A single-region Sovereign is the SME default ([`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §9.2). For corporate / regulated tier (and for any Sovereign that signs an SLA strict enough that single-region downtime would breach it), the upgrade path is:

1. **Sovereign provisioned in Region A** (e.g. `hz-fsn-rtz-prod`) — single LB IP, plain A records.
2. **Operator decides to add Region B** via the Catalyst admin UI: Admin → Infrastructure → Add Region (see [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §8).
3. Crossplane provisions Region B's clusters (rtz + dmz) with **the same building blocks** as Region A.
4. Region B's PowerDNS replicas join the Sovereign's authoritative NS set via SOA NOTIFY + AXFR (PowerDNS-native zone replication; no external sync layer needed).
5. **catalyst-dns rewrites every Application's lua-record from `single-region` → `active-active`** (or whichever Placement the Application opts into). Old plain A records are replaced with `ifurlup(...)` lua-records pointing at both regional LBs.
6. The cloud witness (failover-controller) starts arbitrating leases across the two clusters.

The cluster name **never changes** during this upgrade — Region A's cluster is still `hz-fsn-rtz-prod`, Region B is now `hz-hel-rtz-prod`, and neither is "primary" or "DR". This is the explicit design from [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md) §1.3 — failover is a routing event, not a renaming event.

### 4.1 Triggers for adding a second region

| Trigger | Recommendation |
|---|---|
| SLA target ≥ 99.95% uptime | Mandatory second region — single-region cannot meet this |
| Compliance requirement (DORA, NIS2, GDPR data residency split) | Mandatory — typically one region per data-residency boundary |
| Application's Placement set to `active-active` / `active-hotstandby` / `active-passive-warm` | Mandatory — these placements require ≥ 2 regions to honour |
| Latency-sensitive global traffic (regional users far from Region A) | Strongly recommended — `pickclosest` lua-records cut median RTT |
| Cost-sensitive single-tenant Sovereign on a low-tier SLA | Defer — pay for it when a workload demands it |

---

## 5. Operational checks

### 5.1 Verify a lua-record is healthy

```
dig +short api.acme.com @ns1.openova.io
# Expected: an A record from the healthy regional LB set.
```

```
dig +short api.acme.com @ns1.openova.io \
  +subnet=80.81.82.0/24
# Expected: with a EU client subnet, pickclosest returns the EU regional LB.
```

### 5.2 Force a probe-failure simulation (chaos-engineering)

The [Litmus](../platform/litmus/README.md) chaos suite includes a scenario that black-holes a regional LB's probe target. After ~1 TTL window:

```
dig +short api.acme.com @ns1.openova.io
# Expected: the affected backend IP is absent from the response.
```

When the probe target is restored, the IP returns automatically — no operator action.

### 5.3 Read PowerDNS probe state

```
kubectl exec -n openova-system deploy/powerdns -- pdns_control bind-list-record api.acme.com
```

PowerDNS exposes the current probe status (last probe timestamp, last result, current selection set) — useful when investigating "why is the answer set what it is?" during an incident.

---

## 6. References

- [PowerDNS Lua Records — upstream documentation](https://doc.powerdns.com/authoritative/lua-records/index.html) — every selector, every option.
- [`PLATFORM-POWERDNS.md`](PLATFORM-POWERDNS.md) — the bp-powerdns deployment, DNSSEC posture, REST API contract.
- [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §7-§8 — multi-region topology + add-region workflow.
- [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md) §1.3 + §7 — building-block naming, no "primary"/"DR" labels.
- [`SRE.md`](SRE.md) §2 — multi-region strategy, split-brain protection, data-replication patterns.
- [`SECURITY.md`](SECURITY.md) §5 — OpenBao independent-Raft-per-region (DNS failover doesn't touch secret authority).
- Issue [#171](https://github.com/openova-io/openova/issues/171) — the change that retired k8gb in favour of PowerDNS lua-records.

---

*Part of [OpenOva Catalyst](https://openova.io). Read [Inviolable Principles](INVIOLABLE-PRINCIPLES.md) before any changes.*
