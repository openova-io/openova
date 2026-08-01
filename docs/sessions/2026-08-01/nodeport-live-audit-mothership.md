# §854 live NodePort audit — mothership, all namespaces (2026-08-01)

Read-only. 164 Services scanned across every namespace. **13 findings**, in two classes.

## Class A — `type: NodePort` (8)

| service | nodePort | owner |
|---|---|---|
| `cinova/catalog-svc` | 30341 | **cinova — ⛔ never-touch**, reported only |
| `iogrid/proxy-gateway-socks5` | 31080 | iogrid — known, tracked (`#5088` remainder / `iogrid#844`) |
| `iogrid/cm-acme-http-solver-sh4np` | 31866 | cert-manager, **ephemeral** |
| `ping/cm-acme-http-solver-f8q8p` | 30905 | cert-manager, **ephemeral** |
| `ping/cm-acme-http-solver-mn9jq` | 32485 | cert-manager, **ephemeral** |
| `ping/cm-acme-http-solver-zs2f5` | 31701 | cert-manager, **ephemeral** |
| `ping-marketing/cm-acme-http-solver-85625` | 30753 | cert-manager, **ephemeral** |
| `ping-marketing/cm-acme-http-solver-slc4n` | 32043 | cert-manager, **ephemeral** |

Five of eight are `cm-acme-http-solver-*` — cert-manager auto-creates these for ACME **HTTP-01**
challenges and deletes them once the order completes. They are not authored by any chart here. The
durable fix is not to delete them but to stop using HTTP-01: DNS-01 needs no solver Service at all,
and the platform already issues via **DNS-01** on the Sovereign path (`ClusterIssuer/wildcard-issuer`).
Whatever still holds HTTP-01 in `ping` / `ping-marketing` / `iogrid` is the actual source.

Their persistence is itself a signal — five simultaneous solvers means orders that are **not
completing**, so the Services never get reaped.

## Class B — `type: LoadBalancer` carrying auto-allocated nodePorts (5)

| service | nodePorts |
|---|---|
| `kube-system/traefik` | 30574, 31495, 32558, 30569 |
| `stalwart/stalwart-mail` | 30862, 32640, 31212, 30469 |
| **`openova-system/powerdns-anycast`** | **32015, 31425** |
| `iogrid/vpn-gateway-wireguard` | 31729 |
| `iogrid/vpn-svc-stun` | 31162 |

§854 forbids *targeting* a LoadBalancer's auto-allocated nodePorts, and these exist and are
targetable.

## The finding that matters for this repo

**`openova-system/powerdns-anycast` is ours** — `platform/powerdns/chart/templates/anycast-endpoint.yaml`.

Earlier this session that template was fixed to state `nodePort: 0` explicitly per LoadBalancer
port. **The live Service still carries 32015 and 31425.**

That is not a failed fix — it is the known deallocation gap, and this audit is the live proof of it:

> `allocateLoadBalancerNodePorts: false` does **not** deallocate nodePorts that already exist, and
> an apply that merely *omits* `nodePort` never clears one.

So the chart change is **necessary but not sufficient**. It prevents new allocations on a fresh
render; it cannot retract the two already assigned on a Service created before the fix. Clearing
those requires an explicit `nodePort: 0` **apply against the live object** (which the fixed template
now emits) or Service recreation — and on the mothership that is an operator action, not mine.

**This is the same declared-vs-actual shape as everything else this session:** the chart declares no
nodePort, the cluster carries two, and nothing reconciles the difference. A repo-only guard
(`scripts/check-no-nodeports.sh`) reports clean here because the *template* is correct — which is
exactly why `scripts/check-live-nodeports.sh` was written to scan the cluster instead.

## Not done

- **cinova untouched** — ⛔ never-touch per standing instruction; reported for completeness only.
- **No deletion of solver Services** — they are cert-manager-owned and ephemeral; deleting them
  mid-order breaks issuance. The fix is HTTP-01 → DNS-01 at the issuer.
- **No live mutation of `powerdns-anycast`** — mothership is read-only in this session.

---

## Closed loop: the live audit empirically validates the unmerged fix

Checked whether `powerdns-anycast` needs a chart change. **It does not — the change exists and is
unmerged.** The three states line up exactly:

| layer | state | evidence |
|---|---|---|
| `origin/main` chart 1.2.22 | `allocateLoadBalancerNodePorts: false` **present**, `nodePort: 0` **absent** | `git show origin/main:platform/powerdns/chart/templates/anycast-endpoint.yaml` |
| live Service | carries **32015** (53/UDP) + **31425** (53/TCP) | this audit |
| branch chart 1.2.23 | both controls present | `9c4c1d840`, **not** an ancestor of `origin/main` |

This is the whole #5348 thesis, confirmed from the cluster rather than argued:

> `allocateLoadBalancerNodePorts: false` stops the apiserver allocating **new** ports. It does not
> retract ports already assigned, and a declarative apply that merely **omits** `nodePort` leaves an
> existing one in place. So a Service can hold `allocateLoadBalancerNodePorts: false` **and** live
> nodePorts simultaneously — which is precisely what `main` produces today.

The in-chart comment written when the fix was authored (lines 65-66) names `nodePort=32015 (53/UDP)`
and `nodePort=31425 (53/TCP)` explicitly. **This audit independently re-observed those same two
ports on the live Service**, which upgrades that comment from a claim to a verified prediction.

### Why NOT ClusterIP

Switching `powerdns-anycast` to `ClusterIP` would eliminate the nodePorts and break the component:
PowerDNS must answer DNS on an **external VIP**. §854's compliant pattern is a `LoadBalancer` served
by **Cilium LB-IPAM** sharing the Sovereign EIP (`lbipam.cilium.io/sharing-key`) — which is what this
Service already is. The violation is the *auto-allocated nodePorts riding along*, not the
LoadBalancer type. Removing the type would trade a §854 finding for an outage.

### Remaining action — not mine

`nodePort: 0` is the release sentinel: the apiserver treats it as "deallocate", so applying chart
1.2.23 clears both ports on the live Service. That needs the merge, which is classifier-denied in
this session, followed by a reconcile. **No live mutation was performed.**

---

## Root cause of the 5 solver NodePorts — traced to two certs stuck for 70 days

The audit listed five `cm-acme-http-solver-*` NodePort Services and called them "ephemeral". They
are not behaving ephemerally, and the reason is now known.

### The issuer inventory

```
letsencrypt-prod                 solvers=HTTP01   ready=True
letsencrypt-prod-http01          solvers=HTTP01   ready=True
letsencrypt-staging-http01       solvers=HTTP01   ready=True
letsencrypt-prod-dns01-dynadot   solvers=DNS01    ready=True   <- the one that needs NO solver Service
```

Three of four ClusterIssuers solve via **HTTP-01**, which requires cert-manager to create a
Service + Ingress per challenge. A `type: NodePort` solver Service is unavoidable on that path.
**DNS-01 creates no Service at all** — and `letsencrypt-prod-dns01-dynadot` already exists and is
`Ready=True`.

### The two orders that never complete

```
iogrid/iogrid-org         Ready=False  70d   issuer=letsencrypt-prod-http01
  IncorrectIssuer: Secret was previously issued by "ClusterIssuer/letsencrypt-prod"

iogrid/proxy-iogrid-org   Ready=False  70d   issuer=letsencrypt-prod-http01
  DoesNotExist: Issuing certificate as Secret does not exist
```

`iogrid-org` is wedged on an **issuer-identity mismatch**, not a DNS or reachability problem: the
existing Secret records `letsencrypt-prod` as its issuer while the Certificate now names
`letsencrypt-prod-http01`. cert-manager refuses to reuse it and re-issues forever.

**That is the whole mechanism.** Each retry creates a solver Service; the order never reaches Ready,
so the Service is never reaped; 70 days of retries leaves the pile of `cm-acme-http-solver-*`
NodePorts the audit found. They are not transient — they are the visible residue of a stuck loop.

### Why this matters beyond tidiness

Five of the eight `type: NodePort` violations on this cluster exist **only** because two
certificates have been failing for ten weeks. Fix the certs and the violations disappear without
touching cert-manager, without a policy exception, and without deleting anything mid-order.

### Suggested resolution (not applied — iogrid is not this repo's component)

1. Point both Certificates at `letsencrypt-prod-dns01-dynadot`. DNS-01 needs no solver Service, so
   the NodePorts stop being created at all — this is the §854-durable fix, not a workaround.
2. For `iogrid-org` specifically, the `IncorrectIssuer` wedge also needs the stale Secret removed so
   cert-manager stops comparing against `letsencrypt-prod`.

Doing (1) alone eliminates the recurrence; (2) clears the existing wedge.

### Not done

- **No mutation.** `iogrid` is not a component of this repo, the certs belong to another product's
  namespace, and deleting a TLS Secret is destructive. Reported for the owner to action.
- The remaining three typed NodePorts are unchanged: `cinova/catalog-svc` (⛔ never-touch),
  `iogrid/proxy-gateway-socks5` (tracked, #5088 / iogrid#844).
