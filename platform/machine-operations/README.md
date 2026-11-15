# bp-machine-operations

Machine operations on the machines a Sovereign runs on, starting with an
inventory of the cluster's own Nodes and with scheduled scans of them.

## Role in Catalyst

Control-plane plumbing, not an Application a User installs. The kinds of the
API group `machines.openova.io` belong to this Blueprint: they arrive with its
chart and are absent where it is not installed. They are not platform
entities. See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) for the
place of a Blueprint in the model.

## What this version ships

The chart holds five custom resource definitions, in `chart/crds/`, and the
templates of the inventory controller:

| Kind | Written by | Meaning |
|---|---|---|
| `MachineSource` | chart values or Git | One place machines are read from. Type `nodes` lists the Kubernetes Nodes of the cluster. |
| `Machine` | the inventory controller only | One machine a source saw. An observation, never committed to Git. The operations controller adds `status.sbom`. |
| `ScanPolicy` | chart values or Git | Which machines are scanned, with which profiles (`inventory`, `hardening`) and on which cron schedule. |
| `HardeningBaseline` | chart values or Git | The hardening content and, per operating-system release, the profile a machine is evaluated against. |
| `MachineRun` | the operations controller only | The record of one scan: its targets, the outcome of each and the files it stored. Never committed to Git. |

The inventory controller is the binary `machine-controller` of the
catalyst-api module (`products/catalyst/bootstrap/api/cmd/machine-controller`),
built into the catalyst-api image as `/usr/local/bin/machine-controller`. Its
code is in `products/catalyst/bootstrap/api/internal/machines/`. The chart runs
it as the Deployment `machine-controller-inventory` in the release namespace:
one replica, the command
`/usr/local/bin/machine-controller --controllers=inventory`, and the service
account of the same name. Its ClusterRole reads Nodes and MachineSources,
writes Machines and the status of MachineSources, and grants no verb on
Secrets. A Role in the release namespace covers its leader-election Lease. The
chart also renders the built-in source `nodes` (type `nodes`) in namespace
`catalyst-infra`.

## How the inventory works

- A Machine is identified by its namespace, its source and the source's own
  identifier. A Machine of class `node` is named after its Node.
- An enumeration that fails, or that cannot read every scope, marks nothing.
- After a complete enumeration, an `Active` Machine that the source did not
  list becomes `Stale`. The time since it was last seen is never the test.
- If that would mark more than `spec.stale.holdPercent` (default 20) of the
  source's active Machines, and at least `spec.stale.holdMinimum` (default 10),
  nothing is marked: `status.held` and the condition `StaleHold` are set. Set
  `spec.stale.releaseHoldFor` to `status.held.enumerationId` to apply the sweep.
- A Machine that a source lists again becomes `Active` and keeps `firstSeen`.
- A Machine that is stale for `spec.stale.retireAfter` (default `720h`)
  becomes `Retired`; after a further `spec.stale.purgeAfter` (default `2160h`)
  it is deleted.
- Each Machine carries exactly five labels, written by the controller:
  `machines.openova.io/class`, `ownership`, `os-family`, `phase` and `source`.

## How a scan runs

The operations controller is the same binary with `--controllers=operations`
(code: `products/catalyst/bootstrap/api/internal/machines/operations/`). This
chart does not run it yet; its Deployment comes with a later version.

- At each time of a ScanPolicy's `spec.schedule` (five-field cron, UTC) the
  controller creates one MachineRun. It waits for that exact time; there is no
  loop with a fixed period.
- The targets are the selected Machines of class `node` that are `Active` and
  not quarantined. A target of a scheduled run starts at its own stable offset
  inside `spec.jitter`, at most `spec.maxParallel` at a time.
- One Job of the image `machine-runner` (`products/machine-runner/`) scans a
  target on its Node, one container per profile. The Job writes no object. A
  Job without a parsable result document is `Failed` with `no-result`; a
  document that does not say `completed` is `Incomplete`, and nothing is written.
- `inventory` sets `Machine.status.sbom`. `hardening` writes the `PolicyReport`
  `machine-<machine>-hardening`: one result per rule, the worst 200 first. A
  release without a profile in the baseline is `skip`, never `pass`. A finished
  MachineRun is deleted after `--runs-retention` (default `720h`).

## Configuration

| Value | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Switches the chart on. At `false` it renders nothing; only the definitions are installed. |
| `image` | `""` | The catalyst-api image, as a full reference. Required when `enabled` is true: the render fails while it is empty. |
| `sources.nodes.enabled` | `true` | Renders the built-in source `nodes` in `catalyst-infra`. |

`chart/tests/render.sh` checks what the chart renders. Run it with
`--self-test` to see that each of its rules can fail.

## Operations

No bootstrap-kit slot installs this chart yet. The namespace `catalyst-infra`
must exist before the chart is switched on. The definitions are installed
from `chart/crds/`, so the slot that installs it must set `install.crds` and
`upgrade.crds` to `CreateReplace`: only then does a later version that adds a
field reach a running Sovereign.
