# CMP-S2: sensor side-car to NATS (capture, payload, heartbeat)

Result record of lab test CMP-S2, run on 2026-11-13.

Outcome: **YES**. Capture is yes and heartbeat is yes. CMP-101 goes ahead with one correction to its chart step, described under "What follows".

## Question

1. **Capture.** The runtime sensor's side-car is Falcosidekick, the side-car of Falco, with its NATS output on. Does a plain publish from it to a fixed subject under `catalyst.>` land in the JetStream stream `CATALYST_ORG`?
2. **Payload.** What exactly is the payload?
3. **Heartbeat.** Does the sensor's periodic metrics event pass through the side-car?

## Evidence

- Branch `spike/CMP-S2` of openova-io/openova. It is never merged.
- The workflow is `.github/workflows/spike-cmp-s2.yaml`. Its scripts are under `tests/spikes/CMP-S2/`.
- Each run uploads the artifact `cmp-s2-evidence`. It holds every captured message, the logs and the chart renders.

| Run | Commit | What it measured |
|---|---|---|
| [43362096399](https://github.com/openova-io/openova/actions/runs/43362096399) | e3535bdf4 | First capture. The metrics event reached the stream although the side-car's minimum priority was `warning`. Its phase B is not valid: it matched messages stored before the phase began. |
| [43364077335](https://github.com/openova-io/openova/actions/runs/43364077335) | 1c060e367 | Per-phase windows. Control at `critical`. A config-file variant. Its phase D is not valid: the environment variable overrode the file. |
| [43365083252](https://github.com/openova-io/openova/actions/runs/43365083252) | f3e6842f5 | Clean phases A to D. |
| [43366070202](https://github.com/openova-io/openova/actions/runs/43366070202) | 763b74a46 | Phases A to D again, plus delivery cases E1 and E2. This is the main evidence. |

## What was run

All of it runs in one GitHub Actions job on `ubuntu-24.04` (kernel 7.0.0-1017-azure, BTF present). Nothing runs outside the job.

**Versions**

- Cluster: kind v0.25.0, node image kindest/node:v1.30.6, containerd 1.7.18, one node.
- Tools: Helm v3.18.4, kubectl v1.35.0, Go 1.23.12.
- NATS: the platform chart `platform/nats-jetstream/chart` (bp-nats-jetstream 1.3.5, nats subchart 1.2.0, nack 0.34.0). It runs as release `nats-jetstream` in `nats-system`, the same place as on a Sovereign. NATS server is 2.10.16 with a 3-node JetStream cluster.
- Sensor: the platform chart `platform/falco/chart` (bp-falco 1.0.2, falco subchart 8.0.2). It runs as release `falco` in `falco`. Falco is 0.43.1 with the modern eBPF probe and the container plugin 0.6.3. Rules are falco-rules:5.
- Side-car: the falcosidekick subchart 0.12.1, image falcosidekick 2.32.0, one replica.

**Changes to the charts for CI.** Images come from docker.io instead of the Sovereign-local Harbor proxy. Image names and tags are unchanged. The metrics interval is 30 s instead of 1 h. The interval changes only how often the event comes, not the event itself.

**Settings under test.** These are the settings CMP-101 step 1 asks for:

- Side-car on.
- NATS output to `nats://nats-jetstream.nats-system.svc.cluster.local:4222`. This is the API's default NATS address.
- Subject template set to the fixed string `catalyst.security.runtime_detected.v1`.
- NATS minimum priority `warning`.
- The sensor's metrics event on: `metrics.enabled`, `output_rule: true`.

**Steps**

1. Create the stream with the platform's own function, `events.ConnectNATS` and `EnsureCatalystOrgStream` from `core/services/shared/events`, through a small probe.
2. Trigger a detection. The trigger has the shape of gate check CMP-G16:
   - a bare Pod in namespace `cmp-gate-<run id>`;
   - the sensor's own image, read from DaemonSet `falco`;
   - command `cat /etc/shadow`;
   - CPU and memory requests, `restartPolicy: Never`.
3. Read the stream back with an ordered ephemeral consumer. It filters on the subject and starts at now minus 7 days.

**Phases.** Each phase counts only messages that the stream stored after the phase began.

- **A.** The settings above.
- **B (control).** Minimum priority `critical`, set as the chart sets it: an environment variable from the chart's Secret.
- **C.** As A, and the side-car also reads a config file (`-c`, a ConfigMap) that names `nats.minimumpriority: warning`.
- **D.** Config file and environment variable both `informational`.
- **E1.** Stream deleted while the side-car publishes.
- **E2.** NATS StatefulSet scaled to 0 while the side-car publishes.

## Answer 1: capture is yes

- The stream as the platform's function creates it: `CATALYST_ORG`, subjects `catalyst.>`, limits retention, file storage, max age 14 days, 1 replica, duplicate window 2 min.
- The side-car publishes with plain core NATS: one connect, publish and close per event. There is no JetStream acknowledgement. The stream captured every event the side-car published.
- **Received equals stored.** In every phase, the side-car received the same events the stream stored. This holds in runs 3 and 4, by rule. Phase C's metrics events are the only exception, explained under "Answer 3".
- **Speed.** From event time to stored in the stream: median 12 ms, maximum 31 ms, over 43 messages (run 4). Run 3 gave median 9 ms, maximum 50 ms, over 42 messages.
- From the trigger container's start to its first stored message: 0.09 to 0.98 s, over 7 triggers in runs 2 to 4. `startedAt` has a resolution of 1 s.
- Gate check CMP-G16 allows 120 s.
- **The trigger.** The sensor image has `cat` (busybox). Every trigger produced exactly one `Read sensitive file untrusted` event (Warning) for that Pod, and it landed in every phase.
- **Message size.** 1,381 to 2,709 bytes.
- **Headers.** None. Every message has an empty NATS header.

## Answer 2: the payload

All 43 messages of run 4 are one JSON object with the same 9 top-level keys. Every key is always present.

| Key | Type and values seen |
|---|---|
| `uuid` | string. The side-car adds it; it is new for each message. |
| `output` | string. Falco's formatted line, which includes the command line. |
| `priority` | string, capitalised: `Critical`, `Warning`, `Informational`. |
| `rule` | string |
| `time` | string, RFC 3339 UTC. 9 fraction digits for detections, 6 for the metrics event. All 43 parse. |
| `output_fields` | object. The keys depend on the rule. Values are string, integer or null for detections, and string, integer or float for the metrics event. No arrays or objects were seen. |
| `source` | `syscall` for detections, `internal` for the metrics event. |
| `tags` | list of strings. It **always contains an empty string `""`**, which the side-car adds from its empty custom-tag setting. The metrics event's tags are exactly `[""]`. |
| `hostname` | the node name |

**A captured detection** (run 4, stream sequence 4, abbreviated):

```json
{"uuid":"…","output":"…: Warning Sensitive file opened for reading by non-trusted program | file=/etc/shadow … command=cat /etc/shadow …",
 "priority":"Warning","rule":"Read sensitive file untrusted","time":"2026-11-13T04:25:13.311063318Z",
 "output_fields":{"container.id":"b448b29355f7","container.image.repository":"docker.io/falcosecurity/falco",
  "container.image.tag":"0.43.1","container.name":"reader","evt.time":1794543913311063318,"evt.type":"openat",
  "fd.name":"/etc/shadow","k8s.ns.name":"cmp-gate-43365083252","k8s.pod.name":"shadow-reader",
  "proc.aname[2]":null,"proc.aname[3]":null,"proc.aname[4]":null,"proc.cmdline":"cat /etc/shadow",
  "proc.exepath":"/usr/bin/busybox","proc.name":"cat","proc.pname":"containerd-shim","proc.tty":0,
  "user.loginuid":-1,"user.name":"root","user.uid":0},
 "source":"syscall","tags":["","T1555","container","filesystem","host","maturity_stable","mitre_credential_access"],
 "hostname":"cmp-s2-control-plane"}
```

**Notes on the fields**

- **Fields CMP-101 keeps.** All of these are present in this detection: rule, priority, time, `k8s.ns.name`, `k8s.pod.name`, `container.name`, `container.image.repository`, `container.image.tag`, `user.name`, `proc.name`, `evt.type`, `fd.name`, tags.
- **The output fields vary by rule.** The other rule seen (below) has no `fd.name`.
- **`k8s.*` can be null.** These fields and `container.*` are null for processes outside a pod.
- **The image field includes the registry.** `container.image.repository` is, for example, `docker.io/falcosecurity/falco`.
- **`evt.time` is too large for float64.** It is a 19-digit integer in nanoseconds, above 2^53. A decoder that reads numbers as float64 changes its value.
- **The metrics event.** Rule `Falco internal: metrics snapshot`, output `Falco metrics snapshot`, priority `Informational`, source `internal`, tags `[""]`. It has 50 to 63 output fields of counters and gauges, for example `falco.version`, `falco.cpu_usage_perc`, `scap.n_evts`, `scap.n_drops`.

**Draft schema.** The job writes a draft JSON Schema derived from the 39 detection messages of run 4 (`runtime_event.schema.draft.json` in the artifact):

- the 9 keys above are all required;
- `time` has format date-time;
- `tags` items are strings;
- `output_fields` values are string, integer, number or null.

## Answer 3: heartbeat is yes, because the side-car's filter does nothing

| Phase (run 4; run 3 is the same) | Minimum priority set by | Warning detection stored | Metrics events received by side-car in window | Metrics events stored in window |
|---|---|---|---|---|
| A | environment `warning` | yes | 1 | 1 |
| B (control) | environment `critical` | yes | 1 | 1 |
| C | environment `warning` + config file `warning` | yes | 3 (75 s window) | **0** |
| D | environment `informational` + config file `informational` | yes | 1 | 1 |

**What the table shows**

- **The setting has no effect as the chart sets it.** The chart passes every side-car setting as an environment variable. With only that variable, the side-car's NATS minimum priority does nothing. At `critical`, a Warning detection and the Informational metrics event both passed (phase B).
- **Why.** A reading of the upstream source explains it. In falcosidekick 2.32.0, `config.go` gives the NATS output defaults for `HostPort` and `SubjectTemplate` only. The configuration library then ignores an environment variable for a key it has no default for. Versions 2.33.0, 2.34.0 and 2.35.1 have the same NATS defaults. They were read, not run.
- **A config file makes the filter work.** It does so once the file names `nats.minimumpriority` (phase C). The environment variable then wins over the file; run 2 showed this.
- **With the filter working at `warning`, the heartbeat is lost.** The metrics event is Informational, so it is dropped (phase C: 3 received, 0 stored).
- **So the metrics event passes the side-car when its NATS minimum priority is unset or `informational` or lower.**

## Delivery: at most once, and silent (run 4)

- **E1, stream deleted.** The side-car received the trigger's 4 events. It logged `NATS - Publish OK` 6 times and no error. After the stream was recreated, none of those events was in it. A publish that no stream captures is lost, and the side-car reports success.
- **E2, NATS down.** The side-car received the trigger's 4 events and published none. It logged 9 errors (`dial tcp …:4222: i/o timeout`, `nats: no servers available for connection`). It did not retry. After NATS came back, none of those events was in the stream. The stream itself survived the restart, with its file storage.

## Static readings (from `helm template`, not run on a Sovereign)

Live-shaped render of `platform/falco/chart`, with only the side-car settings added:

- **Side-car image.** It is `docker.io/falcosecurity/falcosidekick:2.32.0`. That is not the Harbor proxy path that the sensor's own images use.
- **Side-car requests.** It has no resource requests.
- **Probes.** It has liveness and readiness probes, and 1 replica.
- **Config checksum.** The pod template carries a checksum of the side-car's config, so a change to the Secret rolls the pod.

Network:

- **nats-system allow-list.** The per-component allow-list for `nats-system` in `platform/plane-isolation/chart` does not name `falco`.
- **Cluster-wide policy.** `platform/network-policies/chart` has a cluster-wide policy, `10-allow-system-namespaces`, that admits all traffic for pods in both `nats-system` and `falco`.
- **Cilium.** Cilium allows a flow that any one policy allows, so the publish should be allowed.
- **Not measured.** The kind cluster has no Cilium.

## Noise seen in kind (not a product finding)

- Every Pod start produced 5 `Drop and execute new binary in container` events (Critical). They come from `mount` and `umount` run from an upper overlay layer.
- 3 of the 5 were attributed to the new Pod.
- The cause is that a kind node is itself a container. Real nodes are not expected to do this.

## What follows for the plan

1. **CMP-101 goes ahead** with the subject, the address and the stream above.
2. **Pin the schema file** from the payload in "Answer 2".
3. **Correct CMP-101 step 1.** Setting the side-car's NATS minimum priority, as the chart sets it, has no effect. Do not rely on it.
   - **Recommended:** leave the side-car unfiltered. The consumer then drops detections below the runtime minimum priority (`warning` by default) and takes the metrics event (`source` = `internal`, rule `Falco internal: metrics snapshot`) as the heartbeat.
   - **Cost:** detections at Notice, which is the sensor's own floor, also enter the stream.
   - **If filtering at the side-car is wanted:** it needs a config file that names `nats.minimumpriority`, at `informational` or lower. At `warning`, "sensor reporting" would never be true.
4. **What the consumer must handle:**
   - drop the empty tag;
   - read integers as integers, not float64;
   - treat every kept field as optional and nullable;
   - expect the output fields to differ by rule.
5. **The stream must exist before the side-car publishes**, because a publish with no stream is lost and the side-car still reports success (E1). Today `CATALYST_ORG` exists only after some service calls `EnsureCatalystOrgStream` or starts a subscriber. CMP-101's consumer, or the declared stream object, must create it.
6. **Delivery is at most once, as requested for contract C3.** E1 and E2 show it.
7. **The side-car on a Sovereign needs two chart values.** Route its image through the Harbor proxy, and give it resource requests. Otherwise the admission policies that already forced the sensor's images through the proxy will likely refuse it.
8. **Gate check CMP-G16** should match the rule `Read sensitive file untrusted` together with the Pod. "Any finding whose subject is that Pod" passes on noise in kind. Timing is far inside the 120 s.

## What CI could not measure

- **The network path from `falco` to `nats-system`** on a Sovereign, with Cilium and the platform's policies. It needs a gate environment after a train: the CMP-G16 check itself, or one publish from a Pod in `falco` to `nats-jetstream.nats-system:4222`.
- **Admission of the side-car** by the Sovereign's policies: image path and requests.
- **Rule noise and event volume** on real nodes and their kernels. CI used kernel 7.0 in kind.
- **The 1 h interval itself.** CI used 30 s.

## Evidence

Outcome: **YES**. The experiment is the branch `spike/CMP-S2` of this repository (never merged). Runs:

- https://github.com/openova-io/openova/actions/runs/43366070202
- https://github.com/openova-io/openova/actions/runs/43365083252
- https://github.com/openova-io/openova/actions/runs/43364077335
- https://github.com/openova-io/openova/actions/runs/43362096399

## What later tasks must take into account

The test ran fully in CI: four runs on branch spike/CMP-S2, five-push limit not reached (four used), branch left in place.

The outcome is YES: capture yes, heartbeat yes. But the heartbeat passes only because Falcosidekick's NATS minimum priority does nothing when set the way the chart sets it, as an environment variable. A control run at `critical` still published Warning and Informational events. Upstream source (falcosidekick 2.32.0 to 2.35.1) gives the NATS output no MinimumPriority default, so the environment variable is ignored. The filter works only when a config file names the key; the environment variable then wins over the file. At `warning` it drops the heartbeat (3 received, 0 stored).

So CMP-101 step 1 should not set or rely on the side-car's minimum priority. The consumer must filter by the runtime minimum priority and treat `source: internal` / rule `Falco internal: metrics snapshot` as the heartbeat. If a side-car filter is wanted, it needs a config file at `informational` or lower.

Other points later packages must take into account:
- **Lost events are reported as sent.** A publish with no stream is lost while the side-car logs success (E1). With NATS down, the side-car logs errors and does not retry (E2). CATALYST_ORG must exist before the side-car publishes.
- **Payload pins.** Every message has 9 top-level keys and no NATS headers. `tags` always contains an empty string. `evt.time` is a 19-digit integer that float64 changes. Output fields vary by rule and can be null.
- **Side-car admission on a Sovereign.** The chart renders the side-car image from docker.io, not the Harbor proxy, and with no resource requests. Kyverno on a Sovereign will likely refuse it, so CMP-101 must set both.
- **CMP-G16 must match by rule.** Kind produced 5 Critical "Drop and execute new binary in container" events per Pod start. CMP-G16 should match the rule `Read sensitive file untrusted` plus the Pod.

Not measurable in CI:
- The falco to nats-system path under Cilium and the platform's policies. A static reading says the cluster-wide allow admits it. It needs a gate environment after a train.
- Admission of the side-car on a Sovereign.
- Real-node rule noise and kernels.
- The 1 h metrics interval; CI used 30 s.
