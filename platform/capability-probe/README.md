# bp-capability-probe

A consumer of capabilities that exists to prove them: an idle Deployment of the `capability-probe`
image (`core/cmd/capability-probe`) that mounts each bound binding as files, and a function test that
runs the contract's probe on each.

**Role in Catalyst.** An `application` Blueprint, `unlisted`. It `requires` each capability that has
a probe, every entry `optional: true`; the install API binds the ones asked for and writes
`spec.parameters.bindings.<alias>` ([ADR-0020](../../docs/adr/0020-service-model-classes-and-capability-contracts.md), S4, S7 and S11;
[`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md)). The gate record reads its `functionTest`.

## What it renders, for each bound alias

- A `SecretProviderClass` `<release>-<alias>` of provider `openbao`, role `workload`, with one object
  per key of the contract (`platform/_capabilities/<capability>.yaml`), each read from the binding's
  path. `secretObjects` is never set: no key becomes a Secret object.
- A read-only CSI volume of it at `/bindings/<alias>` in the idle Deployment, which is ready when
  `capability-probe --keys-only` finds every key present and well formed.
- The function test: `capability-probe --probe <probe> --binding-dir /bindings/<alias>`. For
  `object-storage` v1 the probe puts an object with the binding's own key, gets it back, compares
  it and deletes it. Its last line is `FUNCTION-TEST pass bp-capability-probe ...`, or
  `FUNCTION-TEST fail bp-capability-probe binding <alias> <reason>`.

With no bound alias (the chart's defaults) the chart renders its ServiceAccount alone: no workload
and no failure, because there is nothing to probe. The Deployment is one replica that serves no
traffic and rolls with strategy `Recreate`, so a roll never needs room for a second Pod.
`chart/ci/nodeport-render-values.yaml` binds one alias, so `scripts/check-no-nodeports.sh` scans
the workload render as well as the default one.

## Configuration

| Value | Default | Meaning |
|---|---|---|
| `image.tag` | `""` | Pinned by the promote job of `.github/workflows/build-capability-probe.yaml`; a bound alias needs it |
| `bindings` | `{}` | Written by the install API only |
| `openbao.addr`, `openbao.role` | the kit's OpenBao, `workload` | What the CSI driver reads with |

## Operational notes

- A probe Pod writes one object of a few bytes and removes it, the exact version it wrote, so a
  versioned bucket keeps nothing either.
- A new contract with a probe adds its keys to `capabilityProbe.contracts` (`chart/templates/_helpers.tpl`)
  and an optional `requires` entry here; `chart/tests/render.sh` compares the keys with the contract file.
- Tests: `chart/tests/render.sh`; the probe's own tests run against a real S3 server in
  `build-capability-probe.yaml`.
