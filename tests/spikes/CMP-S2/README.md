# Lab test CMP-S2: sensor side-car to NATS

This folder and `.github/workflows/spike-cmp-s2.yaml` live only on branch
`spike/CMP-S2`. The branch is never merged; it is the evidence of the test.

Question:

1. Does a plain publish from the runtime sensor's side-car (Falco's
   Falcosidekick, NATS output) to a fixed subject under `catalyst.>` land in
   the JetStream stream `CATALYST_ORG`?
2. What exactly is the payload?
3. Does the sensor's periodic metrics event pass through the side-car?

How it runs, all inside one GitHub Actions job:

- a kind cluster;
- `platform/nats-jetstream/chart` as release `nats-jetstream` in `nats-system`
  (images from docker.io; `nats-values.yaml`);
- the stream `CATALYST_ORG` created by the platform's own function
  `events.EnsureCatalystOrgStream` (`probe ensure`);
- `platform/falco/chart` as release `falco` in `falco`, with the side-car on
  and its NATS output set to subject `catalyst.security.runtime_detected.v1`,
  minimum priority `warning`, and the metrics event on (`falco-values.yaml`);
- a bare Pod in namespace `cmp-gate-<run id>` that runs `cat /etc/shadow`
  with the sensor's own image (the gate check's shape);
- an ordered ephemeral consumer reading the subject back (`probe read`);
- phase B, a control: the side-car's minimum priority raised to `critical`
  the way the chart sets it (an environment variable), to see whether the
  filter acts at all (`falco-values-phase-b.yaml`);
- phase C: the side-car also reads a small config file that names
  `nats.minimumpriority: warning` (`falco-values-phase-c.yaml`,
  `sidekick-config-warning.yaml`);
- phase D: the same file with `informational`
  (`sidekick-config-informational.yaml`), and the environment variable set to
  `informational` too (`falco-values-phase-d.yaml`).

Each phase counts only messages the stream stored after the phase began.

The job uploads every captured message and log as artifact `cmp-s2-evidence`.
