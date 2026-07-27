# §854 NodePort audit — 2026-07-26/27 (mothership + Sovereign hw290)

Durable record of the full three-cluster enumeration, so the question stops being
re-litigated from memory. Method: `kubectl get svc -A -o json` filtered to
`.spec.type=="NodePort"`, run against every live cluster.

## Result — the product is clean

| cluster | total Services | NodePort Services |
|---|---|---|
| **Sovereign hw290 region-a** (`me-east-215`) | 224 | **0** |
| **Sovereign hw290 region-b** (`me-east-215-b-1`) | 159 | **0** |
| mothership (`console.openova.io`, our ops cluster) | — | 5 |

**383 Services across a live, converged, 2-region Sovereign — zero NodePorts.**
§854 holds in the artifact customers receive. Independently corroborated by UAT
row 240, which walked the gateway serving path first-hand on both regions and
confirmed the DIRECT path (cilium LB-IPAM VIP / hostPort on hostNetwork
cilium-envoy) with no nodePort in the chain.

## The mothership's five, itemised

```json
[{"ns":"cinova","name":"catalog-svc","ports":[30341]},
 {"ns":"iogrid","name":"cm-acme-http-solver-sh4np","ports":[31866]},
 {"ns":"iogrid","name":"proxy-gateway-socks5","ports":[31080]},
 {"ns":"ping-marketing","name":"cm-acme-http-solver-85625","ports":[30753]},
 {"ns":"ping-marketing","name":"cm-acme-http-solver-slc4n","ports":[32043]}]
```

None belong to OpenOva. `cinova`, `iogrid` and `ping-marketing` are separate
products sharing the ops cluster.

The three `cm-acme-http-solver-*` entries are generated **by cert-manager** at
challenge time — it creates HTTP-01 solver Services as `type: NodePort` by
default. No chart declares them; they disappear when their challenge completes.
The durable fix for those is `spec.acme.solvers[].http01.ingress.serviceType:
ClusterIP` on the owning Issuer, in those products' own repos.

**OpenOva's own issuers cannot generate one**: `platform/cert-manager-dynadot-webhook`
and `platform/cert-manager-powerdns-webhook` declare **dns01 webhook solvers only**
— zero `http01`.

## `cinova/catalog-svc` — an orphan with no source

Traced exhaustively; there is no Helm chart or manifest to convert:

```
managedFields:     []            ← no field manager
ownerReferences:   none          ← no controller/release owns it
annotations:       {}            ← no kubectl last-applied, no helm metadata
labels:            app=cinova-metro, pod-template-hash=7b8488b969
created:           2026-03-22T11:39:37Z
endpoints:         0 ready       ← routes nothing
ingress refs:      none
```

Empty `managedFields` + no ownerRefs + a `pod-template-hash` selector is the
signature of a one-off `kubectl expose --type=NodePort` against a ReplicaSet that
no longer exists. Searched and **not found** in: this repo, `openova-private`
(`clusters/contabo-mkt/apps/cinova/` has configmap/cronjobs/ingress/secrets and
no Service), and the `cinova` repo itself.

It is provably dead — zero endpoints, a selector pinned to a stale
pod-template-hash, no ingress routing to it. Deleting it is therefore safe *in
principle*, but it is another product's live object under a never-touch rule and
the deletion is irreversible (nothing would recreate it), so it needs its owner.
A ClusterIP replacement was already drafted during the #5088 work
(`docs/sessions/2026-07-15/5088-replacements/30-cinova-catalog-svc-clusterip.yaml`)
and never applied — the same conclusion was evidently reached then.

## Repo state

Exactly **three** non-comment `type: NodePort` lines exist monorepo-wide, all in
`platform/kyverno-policies/chart/tests/forbid-nodeport-service/resource-services.yaml`
— the negative-test fixtures whose `kyverno-test.yaml:39-44` asserts `result: fail`
against them. **They must stay NodePort**: converting them deletes the Enforce
policy's only proof that it blocks anything. PR #5386 added a positive assertion so
that deletion can no longer happen silently (verified failing when simulated).

The only other grep hit is the §854 prohibition **comment** at
`clusters/_template/bootstrap-kit/01-cilium.yaml:389`.

## Disposition

- Product: clean, nothing to do.
- Guard: CI `check-no-nodeports` + Kyverno Enforce (#5088), now tamper-evident (#5386).
- `iogrid/proxy-gateway-socks5` → `iogrid#844`.
- `cinova/catalog-svc` → owner applies the drafted ClusterIP replacement, or deletes
  the dead orphan. Tracked on #5348.
- cert-manager solvers → `serviceType: ClusterIP` on the iogrid / ping-marketing Issuers.

Refs #5348 #5088 #4765 #5386

---

## Re-confirmation — 2026-07-26T21:04:15Z

Re-ran the literal audit command against all three clusters. Result unchanged from the original enumeration above.

```
kubectl get svc -A -o json | jq '.items[] | select(.spec.type=="NodePort")'
```

| cluster | Services scanned | NodePort Services |
|---|---|---|
| Sovereign hw290 region-a (`me-east-215`) | 234 | **0** — no output |
| Sovereign hw290 region-b (`me-east-215-b-1`) | 159 | **0** — no output |
| mothership (ops cluster) | — | 5, all other products |

```json
{"ns":"cinova","name":"catalog-svc","ports":[30341]}
{"ns":"iogrid","name":"cm-acme-http-solver-sh4np","ports":[31866]}
{"ns":"iogrid","name":"proxy-gateway-socks5","ports":[31080]}
{"ns":"ping-marketing","name":"cm-acme-http-solver-85625","ports":[30753]}
{"ns":"ping-marketing","name":"cm-acme-http-solver-slc4n","ports":[32043]}
```

**393 Services across a live, converged, 2-region Sovereign — zero NodePorts.** The mothership's five belong to `cinova`, `iogrid` and `ping-marketing`, three of them cert-manager HTTP-01 solver Services that cert-manager generates as NodePort by default and that no chart declares. Disposition for each is unchanged and recorded above.

---

## Re-confirmation 2026-07-27T05:22:04Z — POST registry-pivot

Re-run after the sovereignty cutover completed `registry-pivot` (step 04) and
`helmrepository-patches` (step 06), which rewrote registry routing and Helm
sources. This is the first audit against the pivoted state, not a repeat of the
pre-pivot one — region-A's service count moved 235 -> 200 across the pivot.

```
region-a: services=200  type=NodePort=0  LB-with-allocated-nodePort=0
region-b: services=159  type=NodePort=0  LB-with-allocated-nodePort=0
```

Zero `Service type=NodePort` on either region. Also zero `type=LoadBalancer`
services carrying an auto-allocated `nodePort`, which is the subtler §854
violation — a LB Service silently allocates nodePorts unless the platform
prevents it, and targeting those is explicitly forbidden.

The repo-side half remains untestable-by-construction: no chart under
`platform/`, `products/` or `core/` declares `type: NodePort`. The only match is
a comment in `platform/powerdns/chart/Chart.yaml:71` forbidding them. There is
nothing to convert.
