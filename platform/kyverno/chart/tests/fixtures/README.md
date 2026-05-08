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
cd platform/kyverno/chart
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
