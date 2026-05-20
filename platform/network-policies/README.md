# bp-network-policies

**Status**: Phase-0 scaffold (#1095 slice H8). Activated by EPIC-5 (#1100).
**Updated**: 2026-05-08

Default-deny CiliumClusterwideNetworkPolicy baseline + a small set of allow-templates
that turn the cluster zero-trust by default while keeping the Catalyst control plane
operational.

This Blueprint is **not** in the bootstrap-kit. Installing it on a running cluster
will sever every flow it doesn't explicitly allow — operators must install
deliberately, per-Sovereign, with the allow-list adjusted for their workloads.
EPIC-5 ships the wiring that turns it on as part of the zero-trust roll-out.

## What it ships

| Template | Effect |
|---|---|
| `default-deny.yaml` | CiliumClusterwideNetworkPolicy `00-default-deny` denies all ingress + egress at namespace selector level. Order 0 (highest priority for layer-1 catch-all). |
| `allow-system-namespaces.yaml` | Allows full ingress/egress for the Catalyst control-plane namespaces (`kube-system`, `flux-system`, `cilium`, `cert-manager`, `catalyst`, `openova-system`, plus `monitoring` and `ingress` namespaces) so the platform itself stays up. |
| `allow-egress-dns.yaml` | Allows egress to CoreDNS in `kube-system` from every namespace. Without this, every Pod fails on DNS lookup. |

**Pod-to-Pod within the SAME namespace** is intentionally NOT handled here.
Cilium `CiliumClusterwideNetworkPolicy` cannot express "same namespace as the
source Pod" without enumerating every namespace explicitly — that's a per-tenant
concern. The `organization-controller` (slice C1 of #1095) renders a
per-namespace `CiliumNetworkPolicy` (CNP, namespace-scoped) at Organization
creation time, with implicit same-namespace allow semantics.

Per-Application policies (allow `app-X` → `app-Y` cross-namespace, allow Org `acme`
egress to the public internet on port 443, etc.) are authored by the
application-controller (slice C4 in #1095) at install time from
`Blueprint.spec.networking.egress` declarations — they are **not** part of this
Blueprint.

## Activation contract

```yaml
# values.yaml override (or per-Sovereign overlay)
enabled: true
allowSystemNamespaces:
  - kube-system
  - flux-system
  - cilium
  - cert-manager
  - catalyst
  - openova-system
  - monitoring
  - ingress
```

When `enabled: false` (the default), no policies render — installing this chart
is a no-op until the operator opts in.

## Verification

After installing with `enabled: true`:

```bash
# Two Pods in the same namespace — should reach each other
kubectl run -n acme test-a --image=busybox -- sleep 3600
kubectl run -n acme test-b --image=busybox -- sleep 3600
kubectl exec -n acme test-a -- wget -qO- test-b.acme.svc:80   # OK

# Two Pods in different namespaces — should NOT reach each other
kubectl run -n bank test-c --image=busybox -- sleep 3600
kubectl exec -n acme test-a -- wget -qO- test-c.bank.svc:80   # blocked
```

Cilium Hubble (turned on in EPIC-5) shows the deny events with the matching policy.

## Why a separate Blueprint, not bp-cilium templates

`bp-cilium` is foundational infra installed on every cluster on day 0; default-deny
breaks every workload that hasn't been allowlisted. Shipping the policy as a
separate, opt-in Blueprint preserves the safety boundary: bp-cilium is always-on,
bp-network-policies is operator-on after the allowlist is sized.

## References

- docs/ARCHITECTURE.md §3.9 row 8 + §8 (EPIC-5 Networking)
- ADR-0001 §2 (zero-trust)
- platform/cilium/README.md
