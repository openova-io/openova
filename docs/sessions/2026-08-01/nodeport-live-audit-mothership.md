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
