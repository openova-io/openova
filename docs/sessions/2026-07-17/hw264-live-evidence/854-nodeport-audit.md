# §854 NodePort compliance audit — 2026-07-17

Read-only enumeration (`kubectl get svc -A -o json | jq 'select(.spec.type=="NodePort")'`)
across the live hw264 sovereign and the mothership. NodePorts are ABSOLUTELY
FORBIDDEN per §854 / founder verbatim 2026-07-03.

## Result

| Cluster | NodePort svc | Verdict |
|---|---|---|
| **hw264 sovereign (region-a)** | **0** | ✅ §854 UPHELD — the platform provisions with zero NodePorts; Kyverno `24-forbid-nodeport-service` fail-closes creation |
| mothership `cinova/catalog-svc` | 1 | ⛔ **cinova NEVER-TOUCH** (SME workload) — out of scope, must not modify |
| mothership `iogrid/cm-acme-http-solver-sh4np` | 1 | transient cert-manager ACME HTTP-01 solver = the **cm-acme carve-out** Kyverno explicitly exempts; cert-manager creates/reaps it per-challenge (not a durable chart NodePort) |
| mothership `iogrid/proxy-gateway-socks5` | 1 | **founder-merge-gated in iogrid#844** (#5088 remainder: "socks5 apply-on-go") — not autonomously actionable |

## Conclusion

The OpenOva **platform itself (the deliverable) is §854-clean: 0 NodePort services
on the fresh hw264 sovereign.** The 3 remaining NodePorts live only on the
mothership host and are each in a protected/gated class:

- `cinova/*` — SME never-touch (founder rule).
- `cm-acme-http-solver-*` — transient cert-manager HTTP-01 solver, the explicit
  Kyverno carve-out; cert-manager owns its lifecycle.
- `iogrid/proxy-gateway-socks5` — fix tracked in **iogrid#844**, awaiting founder merge.

None are autonomously actionable without violating cinova-never-touch or acting on
a founder-gated fix. No chart edit is warranted; §854 posture confirmed compliant
where it is the platform's responsibility.

## Provenance trace — 2026-07-17 (definitive close)

Source-grep of this repo: **ZERO `type: NodePort`** in any `platform/`, `products/`,
`core/`, or `clusters/` chart (only the Kyverno `forbid-nodeport-service` *test
fixture* references the string). The platform ships zero NodePorts and enforces
that via Kyverno #5089.

Live-service provenance (kubectl labels/ownerRefs):
- `cinova/catalog-svc` — created 2026-03-22, no Helm managed-by, no ownerRefs →
  hand/GitOps Service; manifests in `openova-private/clusters/contabo-mkt/apps/cinova/`
  (a SEPARATE repo). ⛔ cinova NEVER-TOUCH (founder). Not actionable here.
- `iogrid/cm-acme-http-solver-sh4np` — ownerRef `Challenge/proxy-iogrid-org-1-…` +
  `acme.cert-manager.io/http01-solver=true` → cert-manager auto-creates/reaps this
  transient ACME HTTP-01 solver (Kyverno cm-acme carve-out). Not a chart; cert-manager
  GCs it. (Its 2-day age hints an iogrid ACME challenge is stuck — iogrid domain, out of scope.)
- `iogrid/proxy-gateway-socks5` — created 2026-05-31, no Helm managed-by → hand/GitOps
  Service in a SEPARATE iogrid repo; the NodePort→direct fix is gated in **iogrid#844**
  (founder merge). Not actionable here.

**Conclusion:** §854 is complete + enforced on the OpenOva platform (0 NodePorts,
Kyverno guard). The 3 live residue Services are each out-of-repo and founder-fenced
(never-touch / founder-gated / cert-manager-owned) — none is a chart in a repo this
agent may edit. #5088's autonomously-actionable scope = ∅ (matches task #962).
