# bp-network-policies

The cluster-wide network policy set of a Sovereign: one chart, one dialect
(`CiliumClusterwideNetworkPolicy`), every rule written by hand. It is a
control-plane Blueprint, installed by bootstrap-kit slot 26
(`clusters/_template/bootstrap-kit/26-network-policies.yaml`). Its place in the
model is the zero-trust networking of [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md)
and ADR-0001 §2.

Every object selects pods by the label `io.kubernetes.pod.namespace`, so no
object waits for a namespace to exist. Organization namespaces are not handled
here: the organization controller renders their namespaced policies.

## What it renders

| Object | Template | When |
|---|---|---|
| `baseline-deny-metadata` | `templates/baseline-deny-metadata.yaml` | `enabled` is true. No pod reaches the instance metadata address `169.254.169.254`, except a pod labelled `network.openova.io/metadata-access: "true"`. It puts no pod under default deny. |
| `baseline-default-deny` | `templates/baseline-default-deny.yaml` | `enabled` is true and `enforcedNamespaces` is not empty. The pods of those namespaces are under default deny: nothing enters them, and only DNS to the cluster resolver leaves them. |
| `allow-<namespace>` | `templates/allow/<namespace>.yaml` | `enabled` is true and the namespace is in `enforcedNamespaces`. What the pods of that namespace may send and receive. |

The rule template of an allow file, the rules it must keep and the standing
rule for later namespaces and flows are in
[`chart/templates/allow/_README.tpl`](chart/templates/allow/_README.tpl). The
helper `network-policies.allow` (`chart/templates/allow/_helpers.tpl`) builds
each allow object and stops the render on a rule that breaks the template.

## Configuration

| Value | Default | Meaning |
|---|---|---|
| `enabled` | `true` | `false` renders nothing at all. The kit slot sets it to `false` today. |
| `enforcedNamespaces` | `[]` | The namespaces under default deny. Each entry is a namespace name; another value stops the render. The list grows ring by ring, with the allow file of each namespace. |

With the default values the chart renders `baseline-deny-metadata` only.

## Proof

- `chart/tests/render.sh`: what each value renders, every allow file of the
  folder, the refusals of the helper, and a self-test that proves each rule
  can fail.
- `chart/tests/function-test-render.sh` and `chart/templates/tests/function-test.yaml`:
  the `helm test` Pod, which proves on a running cluster that the metadata
  address is denied.
- Gate check NET-G06 (`tests/acceptance/sovereign/internal/suite/check_policy_dials.go`)
  dials every row of `tests/acceptance/sovereign/policy-dials.yaml` from a pod
  in every region and compares what happened with what the row expects.

## Operational notes

Pods on the host network are not subject to pod policy. The chart is a
singleton per cluster and holds no state; a multi-region Sovereign installs the
same values in every region.
