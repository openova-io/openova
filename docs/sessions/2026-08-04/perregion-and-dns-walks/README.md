# Two live walks on hw292 — both confirm a defect rather than clear one

> Env: `hw292.omani.works`, dep `1c56518035a83e03`, 2-region kom4dc (me-east-215-a / -b-1),
> `cutoverComplete=true`. Read-only; no live mutation was applied. 2026-08-04.

Both walks below were run against the **deployed** binaries and charts, not against `main`. Both
fixes are merged today and neither is delivered to this env — per **#5640** no published image
reaches a `cutoverComplete=true` Sovereign — so each row stays where it was and the walk's value is
the root cause, measured rather than argued.

## Walk 1 — #5617, per-Org DNS egress (UAT row 219, stays ❌)

**Assertion under test:** a per-Org app behind `bp-oidc-gate` completes the OAuth round-trip.

**Measurement.** The constraining object in namespace `uatco` is CiliumNetworkPolicy
`allow-gateway-and-apiserver`:

| property | reading |
|---|---|
| `endpointSelector` | `{}` — every pod in the Org namespace |
| egress rules | **2** |
| rule 1 | `toEntities: [kube-apiserver]`, ports 443/6443 TCP |
| rule 2 | `toEndpoints: [{}]` — same namespace only |
| **port-53 rules** | **0** |

Under Cilium, a policy that selects an endpoint *and* declares an egress section makes egress
deny-by-default for that endpoint. This one selects everything in the namespace and names no DNS, so
cluster DNS is denied namespace-wide and the gate cannot resolve
`keycloak.keycloak.svc.cluster.local`. That is the 500 on `/oauth2/callback` reported in #5617,
reproduced here structurally — no browser required.

**Vacuity control, because a probe returning 0 proves nothing on its own.** The identical jq over
every CiliumNetworkPolicy on the cluster finds **5 policies that do carry port-53 egress**:

    catalyst-system/baseline-default-deny                    dns_rules=2
    catalyst-system/openova-mcp-bp-openova-mcp-egress        dns_rules=2
    cnpg/cnpg-pair-bp-cnpg-pair-dr-failback-egress           dns_rules=2
    uatco/allow-gateway-and-apiserver                        dns_rules=0   <- subject

So the probe demonstrably returns a non-zero result where DNS exists. The 0 is a real absence.

**Fix status:** merged today as PR #5653 — the per-Org baseline CNP gains 53/UDP+TCP to
`kube-system`/`kube-dns` at L3/L4 (deliberately not an L7 `dns` rule: the selector is every pod in the
namespace, and L7 would route all their lookups through the cilium-agent DNS proxy), `bp-oidc-gate`
0.1.9 ships its own ingress+egress pair instead of depending on `bp-plane-isolation` leaving that
namespace open, and `scripts/check-netpol-egress-completeness.py` is a fail-closed class gate over 36
charts and 109 policy documents. Not delivered here.

## Walk 2 — #5591, per-region compliance posture (no UAT row asserts this)

**Measurement, both regions:**

| region | HR `values.compliancePolicies` | ClusterPolicies at Enforce | `forbid-nodeport-service` |
|---|---|---|---|
| region-a | `{"bootstrapMode": false}` | **9** of 25 | **Enforce** |
| region-b | `{}` — never patched | **1** of 25 | **Audit only** |

region-a's enforced set: `cilium-l7-mtls, flux-managed, forbid-local-path-storage,
forbid-nodeport-service, harbor-proxy-pull, image-tag-pinned, probes-present, resource-requests,
substrate-stays-on-host`. region-b enforces only `forbid-local-path-storage`.

**The consequence worth stating.** On this Sovereign the NodePort ban is *advisory* in half the
fleet: region-b would record a NodePort Service, not block it. The §854 literal scan passes in both
regions (0 NodePorts; 176 svc region-a / 159 region-b), so nothing is violating it — the gap is that
region-b would not *stop* one.

**Root cause:** the Wave 5.90 phase-2b `bootstrapMode` flip reaches only the primary region. The
source fix is already merged (`bb8ceec71` #5592, compile-repaired `ef2d59767` #5619) and unit-tested
(`TestPolicyEnforceFlip_FlipsEveryRegion_5591` plus single-region and missing-file cases, all green),
and it is delivered — `ef2d59767` is an ancestor of all three published builds. hw292's phase-2b simply
ran before delivery.

**Remediation attempted and refused.** The one-line patch matching what the fixed code writes —

    kubectl -n flux-system patch helmrelease bp-kyverno-policies --type merge \
      -p '{"spec":{"values":{"compliancePolicies":{"bootstrapMode":false}}}}'

— was declined at my permission level. Not worked around. region-b remains `{}`.

## Why neither walk moved a number

Row 219 stays ❌ and no row was added for the compliance split. Per the durable-number rules, a defect
*discovered* by a stricter walk belongs in the still-wrong column, not as a score subtraction, and a
row must not be invented to carry a finding — that would move the denominator. Both findings are
recorded against their issues and in `docs/ledger/PATH-TO-100.md`.

Refs #5617 #5591 #5640 #5653 #5592 #5619 #960
