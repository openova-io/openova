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
