# §854 NodePort disposition — 2026-07-20 (hw281 cycle)

Live enumeration proving §854 (NodePorts ABSOLUTELY FORBIDDEN) compliance on the OpenOva platform.
Command: `kubectl get svc -A -o json | jq '[.items[]|select(.spec.type=="NodePort")|{ns,name,ports:[.spec.ports[].nodePort]}]'`.

## Sovereign — the platform, where §854 applies: hw281 (dep `6db2745323dff4aa`, 2-region)
- region-a (`me-east-215-a`): **0** NodePort Services
- region-b (`me-east-215-b`): **0** NodePort Services

The gateway is served DIRECT (cilium LB-IPAM / shared-EIP / Local-ETP + hostPort on the hostNetwork
cilium-envoy pods), never a nodePort. **UAT row 240 ✅** (stamped this cycle, commit c478a509a).

## Mothership (Catalyst-Zero, contabo) — 3 live NodePorts, ALL founder-gated (NOT platform/chart-sourced)
| ns/name | nodePort | disposition |
|---|---|---|
| `cinova/catalog-svc` | 30341 | ⛔ cinova = suspended SME app, founder **NEVER-touch** |
| `iogrid/cm-acme-http-solver-sh4np` | 31866 | ephemeral cert-manager ACME HTTP01 solver (transient; iogrid founder repo; symptom tracked in iogrid#844) |
| `iogrid/proxy-gateway-socks5` | 31080 | iogrid = founder's **separate repo**, not this monorepo |

## Chart-sourced NodePorts in the openova monorepo: ZERO
`grep -rn 'type: NodePort' platform/ products/ core/ clusters/` returns only
`platform/kyverno-policies/chart/tests/forbid-nodeport-service/` — the **test fixtures that prove the
§854 forbid-NodePort Kyverno policy rejects NodePort Services**. Converting them would sabotage the
enforcement. The §854 CI gate + Kyverno policy hold.

## Conclusion
§854 is **compliant on the platform**: 0 chart-sourced NodePorts, 0 Sovereign NodePorts (hw281 both
regions). The only live NodePorts are on the mothership and are founder-gated (cinova never-touch +
iogrid founder-repo). Remainder tracked in **#5088** (`status/parked`) + **iogrid#844** (founder repo).
No openova chart change is possible or appropriate — the correct action is exactly the current parked state.
