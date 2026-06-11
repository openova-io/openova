# Compliance policy fixtures (slice K, EPIC-1 #1096)

YAML fixtures for offline `kyverno apply` validation. Each policy gets two
files: a passing case and a failing case, both shaped as the workload kinds
the policy targets.

The fixtures here are illustrative — slice S1 ships the full per-policy
test matrix once the score aggregator is in place, since identical
fixtures feed both the Kyverno policy assertion and the synthetic
PolicyReport row the aggregator joins on.

## How to run

```bash
# Render the chart with a single policy enabled, then run kyverno apply.
cd platform/kyverno-policies/chart
helm template . --set compliancePolicies.multiReplica.enabled=true \
  > /tmp/multi-replica.yaml

# Install the kyverno CLI per https://kyverno.io/docs/kyverno-cli/install/
kyverno apply /tmp/multi-replica.yaml \
  --resource tests/fixtures/multi-replica/pass-deployment.yaml
kyverno apply /tmp/multi-replica.yaml \
  --resource tests/fixtures/multi-replica/fail-deployment.yaml
```

The pass case should produce no violations; the fail case should produce
exactly one (per workload).

## Coverage

| Policy | Pass fixture | Fail fixture |
|---|---|---|
| multi-replica-drainability | `multi-replica/pass-deployment.yaml` | `multi-replica/fail-deployment.yaml` |
| probes-present | `probes-present/pass-deployment.yaml` | `probes-present/fail-deployment.yaml` |
| resource-requests | `resource-requests/pass-deployment.yaml` | `resource-requests/fail-deployment.yaml` |
| image-tag-pinned | `image-tag-pinned/pass-deployment.yaml` | `image-tag-pinned/fail-deployment.yaml` |

The remaining 15 policies' fixtures are deferred — slice S1 ships the
authoritative matrix.

## MUTATE policy fixtures (Refs #3268)

`stamp-cilium-enforced-label` is a MUTATE policy, so its fixtures are
"needs-mutate" (the label is added) vs "already-stamped" (no-op / idempotent),
rather than pass/fail validate cases.

| Policy | Needs-mutate fixture | Idempotent fixture |
|---|---|---|
| stamp-cilium-enforced-label | `cilium-enforced-label-mutate/needs-mutate-deployment.yaml` | `cilium-enforced-label-mutate/already-stamped-deployment.yaml` |

```bash
helm template . --set compliancePolicies.bootstrapMode=false \
  --set compliancePolicies.ciliumEnforcedLabelMutate.enabled=true \
  --show-only templates/mutate/00-stamp-cilium-enforced-label.yaml > /tmp/mutate.yaml

# needs-mutate → emits a patched resource with policy.cilium.io/enforced: "true"
kyverno apply /tmp/mutate.yaml \
  --resource tests/fixtures/cilium-enforced-label-mutate/needs-mutate-deployment.yaml
# already-stamped → no mutation (resource unchanged)
kyverno apply /tmp/mutate.yaml \
  --resource tests/fixtures/cilium-enforced-label-mutate/already-stamped-deployment.yaml
```

The end-to-end mutate-before-validate proof (admission-time ordering against
the REAL 09/11 Enforce baselines on a kind + kyverno v1.18 cluster) is captured
in the PR body — `kyverno apply` exercises only the policy logic offline, not
the apiserver's webhook phase ordering.
