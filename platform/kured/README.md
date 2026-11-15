# bp-kured

Coordinated node reboots: kured reboots a node that needs it, one node at a
time and only inside the reboot span. The DaemonSet `node-patch-config` makes
every node install security updates without ever rebooting itself.

## Role in Catalyst

Control-plane plumbing on every cluster of every region, never a catalog item
(`visibility: unlisted`). A bootstrap-kit slot installs it into `kube-system`;
see [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md). The Kubernetes
version is upgraded by the system-upgrade-controller Plans, not by this chart.
Reboots and upgrades take turns by time: kured reboots only inside its span,
and the upgrade Plans carry a `spec.window` outside it.

## What it ships and how to configure it

- The upstream chart `kubereboot/kured` 6.1.0 (kured 1.23.0) under the key
  `kured:` of `values.yaml`. Its DaemonSet is named `kured` and holds the
  reboot lock in an annotation of its own (see the install hold). Defaults: one
  node at a time, from 00:30 to 22:00 UTC on every day, a check every 15
  minutes, a 15-minute drain limit (a node that cannot drain is not
  rebooted), a lock time-to-live of 1 hour and a 5-minute pause before the
  next node. kured does not reboot a node while an upgrade Job Pod of the
  system-upgrade-controller runs on it; the selector's second key,
  `upgrade.cattle.io/plan`, leaves out the controller's own Pod. The
  measurements are in
  [`docs/ledger/plan/spikes/INF-030.md`](../../docs/ledger/plan/spikes/INF-030.md).
- The install hold. kured takes its lock in the annotation
  `catalyst.openova.io/kured-install-hold` (`kured.configuration.lockAnnotation`),
  and the chart creates the DaemonSet `kured` with a lock already in it
  (`kured.dsAnnotations`, held by `catalyst-install-hold`, no time limit). So
  kured restarts no node while the bootstrap kit installs and the hand-over
  chain runs. The last step of the chain writes the ConfigMap
  `flux-system/bp-kured-install-hold` in every region; the kit slot reads it
  as values, and kured then locks in its default annotation
  `weave.works/kured-node-lock`, where nothing is held. To release a region by
  hand, create that ConfigMap with the key `values.yaml` and the value
  `{"kured":{"configuration":{"lockAnnotation":"weave.works/kured-node-lock"}}}`.
- kured does not run on control-plane nodes (`kured.affinity`). A region has
  one, and its restart takes the region's API server away, so that restart
  is a person's decision. A Sovereign with three or more control-plane nodes
  per region sets `kured.affinity` to null.
- The DaemonSet and ConfigMap `node-patch-config`. On every node, whatever its
  taints, it writes `/etc/apt/apt.conf.d/20auto-upgrades` and
  `/etc/apt/apt.conf.d/52unattended-upgrades-catalyst` when they differ from
  the ConfigMap, and checks again every hour. The second file sets
  `Unattended-Upgrade::Automatic-Reboot "false"`. Its only host mount is that
  directory.
- `nodePatchConfig.image` has no default; the kit slot sets it with the
  registry prefix of the Sovereign. The image needs a shell with `cmp`, and
  `kubectl` for the function test. Left empty, the install fails.

## Operational notes

- No data, no backup. Each cluster runs its own kured and its own lock.
- A node that still has the install hold logs `Lock already held:
  catalyst-install-hold` every period and is otherwise idle.
- A node whose Pods cannot all be evicted is not rebooted: kured gives up
  after the drain limit and tries again in the next period, evicting the
  other Pods of the node each time. A single-instance CloudNativePG primary
  is such a Pod (its PodDisruptionBudget allows no eviction).
- `helm test <release>` passes when `kured` has as many ready Pods as nodes to
  run on and the test Pod's node has both files as the ConfigMap holds them.
- `bash platform/kured/chart/tests/render.sh` checks the chart with no cluster.
