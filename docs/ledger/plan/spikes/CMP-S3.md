# CMP-S3 result: scanner report labels, publication date, benchmark names, report volume

## Question

On the pinned scanner (trivy-operator 0.30.1), the spike asks five things:

1. Which labels name the owner on a report for a Deployment's Pods?
2. Is `publishedDate` present on the findings?
3. Which benchmark reports exist by default?
4. Is there a benchmark profile for the reference Kubernetes distribution (k3s)?
5. How many report objects and bytes does a fresh install hold?

## What was run

Everything ran in GitHub Actions in openova-io/openova, on branch `spike/CMP-S3`. The branch is never merged. The workflow is `.github/workflows/spike-cmp-s3.yaml`. The scripts are in `tests/spikes/CMP-S3/`. There were three runs (pushes), and all three passed.

- **Runner:** ubuntu-24.04, 4 vCPU, 15 GiB RAM.
- **Cluster:** single-node k3s v1.31.4+k3s1. This is the version Sovereigns run. It used the Sovereign server flags: embedded etcd (`--cluster-init`), traefik, servicelb and local-storage disabled, watch cache 50, etcd quota 1 GiB, max-pods 50. One difference from a Sovereign: flannel instead of Cilium. The scanner does not use the CNI.
- **Tools:** kubectl v1.31.4, helm v3.16.3, etcdctl 3.5.17.
- **Scanner:** the platform's own chart `platform/trivy/chart`, which is bp-trivy 1.0.6. It wraps trivy-operator chart 0.32.1 (operator 0.30.1) with the platform's values. That includes the scanner image tag 0.70.0, which the platform sets in place of the operator chart's own 0.69.3. The release is named `trivy` in namespace `trivy-system`, as in bootstrap slot 30. Only the image registry was changed. The mothership Harbor proxy was swapped for the vendor's public copy of the same images (mirror.gcr.io/aquasec).
- **Test workloads** (namespace `cmp-s3`, old public images so findings exist):
  - Deployment `web`: 2 replicas, with containers `nginx` (nginx:1.21.6) and `sidecar` (busybox:1.36.1)
  - StatefulSet `db` (redis:6.2.6)
  - DaemonSet `agent` (alpine:3.15.0)
  - CronJob `tick` (busybox:1.36.1)
  - bare Pod `solo` (alpine:3.15.0)
  - two Services
- **Phases:**
  - A: install, then wait until the scans settle.
  - R: roll `web` to nginx:1.22.1.
  - B: ask for every benchmark name, including `k3s-cis-1.24`, with a 2-minute cron.
  - D1: scanner tag 0.69.3.
  - D2: scan job memory limit raised to 1 GiB.
  - Run 3, E1: the pinned chart in client-server mode.
  - Run 3, E2: the newest upstream chart 0.38.1 (operator 0.36.1, scanner 0.76.1) with the platform's values.
- **Measurement:**
  - `measure.py` uses only `kubectl get` and `kubectl get --raw`. The byte figures include managed fields.
  - `etcd_bytes.py` reads `etcdctl get /registry/aquasecurity.github.io/ --prefix` and `etcdctl endpoint status`.

## Measured values

### 1. Owner labels (runs 1 and 2; same in run 3 E2 on operator 0.36.1)

For a Deployment's Pods, every report names the **ReplicaSet**, not the Deployment and not the Pod. Example from run 2, phase A:

- `trivy-operator.resource.kind: ReplicaSet`
- `trivy-operator.resource.name: web-949bd45cc` (the ReplicaSet)
- `trivy-operator.resource.namespace: cmp-s3`
- `trivy-operator.container.name: nginx`. This label is on vulnerability, exposed-secret and SBOM reports, which come one per container. Config-audit reports have no container label.
- Also the labels `resource-spec-hash` and `app.kubernetes.io/managed-by: trivy-operator`, and the annotation `trivy-operator.aquasecurity.github.io/report-ttl: 24h0m0s`.
- Owner reference: `ReplicaSet/web-949bd45cc`, controller true.
- Report names: `replicaset-<replicaset>-<container>`. Config-audit reports are named `replicaset-<replicaset>`.
- To find the Deployment, follow the ReplicaSet's controller owner reference: ReplicaSet/web-949bd45cc, then Deployment/web.

Other kinds name themselves: StatefulSet `db`, DaemonSet `agent`, Pod `solo`, and CronJob `tick` (not its Jobs). Each Service gets its own config-audit report.

What happens on a rollout:

- In 3 of 4 rollouts, the previous ReplicaSet's vulnerability report was gone at the next read. In the two timed cases (run 1 R, run 2 R) that was within 10 seconds.
- In 1 of 4 rollouts (run 2 D1), the previous ReplicaSet's vulnerability and exposed-secret reports stayed at least 23 minutes, until the run ended.
- SBOM reports were never deleted. After three rollouts, the `nginx` container had four SBOM reports, one per ReplicaSet (run 2 D).

### 2. publishedDate: present

Run 2, phase A: 7 reports with 2,344 findings.

- The key is present on all 2,344 findings.
- The value is non-empty on 2,255 findings (96.2 %) and an empty string on 89.
- By severity, findings with a date:

| Severity | With a date |
|---|---|
| Critical | 60 of 60 |
| High | 757 of 791 |
| Medium | 913 of 930 |
| Low | 508 of 518 |
| Unknown | 17 of 45 |

- ID prefixes of the 89 findings without a date: CVE 51, DLA 23, TEMP 12, GO 3. 27 of the 89 have a fixed version.
- `lastModifiedDate` is filled on exactly the same findings.
- Format: RFC 3339 UTC, for example `2022-04-03T21:15:08Z`.
- The CRD lists `publishedDate` as a required string. "No date" is therefore an empty string, not a missing key.

Run 1 gave the same numbers. On operator 0.36.1 (run 3 E2), 2,030 of 2,119 findings had a date.

### 3. Benchmark reports by default

- The four names are `k8s-cis-1.23`, `k8s-nsa-1.0`, `k8s-pss-baseline-0.1` and `k8s-pss-restricted-0.1`. Each is the object's `metadata.name`, equal to `spec.compliance.id`.
- The platform values do not set the list, so the chart default applies. The chart also ships `eks-cis-1.4` and `rke2-cis-1.24`. The newest chart, 0.38.1, has the same four defaults.
- Schedule: cron `0 */6 * * *`, report type `summary`. A fresh install has no results until the next 00/06/12/18 UTC tick. All four were still empty 36 minutes after install (run 1) and 23 minutes after install (run 2). With a 2-minute cron, all six had results within 60 seconds.
- Result shape: `status.summary` holds `passCount` and `failCount`. `status.summaryReport.controlCheck[]` holds `id`, `name`, `severity` and `totalFail`.
- A control with no automated check has no `totalFail`, and it **is counted in passCount**:
  - k8s-cis-1.23: 116 controls, 8 without checks, 98 with totalFail 0, 10 failing. passCount is 106 = 98 + 8.
  - eks-cis-1.4: passCount is 44 = 20 + 24 controls without checks.

### 4. Profile for k3s: none

- Chart 0.32.1 has no k3s template. Adding `k3s-cis-1.24` to the list renders nothing (runs 1 and 2).
- From the public upstream source, not from a run: the operator's built-in spec set has no k3s spec either.
- On k3s, `k8s-cis-1.23` and `rke2-cis-1.24` gave identical results: 10 fail, 106 pass.
- 87 of the 116 CIS controls are node and control-plane checks (AVD-KCV). All 87 reported 0 failures, and the node assessment held zero findings. The files those checks read (`/etc/kubernetes/manifests`, `admin.conf`, `pki`) do not exist on the k3s node. Those 87 passes are not evidence that anything was checked.
- Only 21 CIS controls are workload and role checks (10 fail, 11 pass). NSA has 16 such checks (7 fail), 7 node checks (all 0) and 4 without checks.
- The two pod-security profiles consist only of workload checks: 11 and 17 controls, each with 1 failing.

### 5. Report objects and bytes (CI cluster, not a Sovereign)

Run 2, phase A, after the scans settled. Bytes are etcd value bytes.

| Kind | Objects | Bytes | Average | Largest |
|---|---|---|---|---|
| vulnerabilityreports | 7 | 1,148,823 | 164,117 | 530,438 |
| sbomreports | 6 | 875,713 | 145,952 | 340,188 |
| clusterrbacassessmentreports | 74 | 162,088 | 2,190 | 5,329 |
| configauditreports | 14 | 86,456 | 6,175 | 15,286 |
| clustercompliancereports | 4 | 46,137 | 11,534 | 30,530 |
| rbacassessmentreports | 9 | 21,299 | 2,366 | 2,906 |
| exposedsecretreports | 7 | 13,573 | 1,939 | 2,012 |
| clustervulnerabilityreports | 1 | 7,776 | 7,776 | 7,776 |
| clustersbomreports | 1 | 6,619 | 6,619 | 6,619 |
| clusterinfraassessmentreports | 1 | 1,510 | 1,510 | 1,510 |
| Total | 124 | 2,369,994 | | |

- The largest vulnerability report is nginx:1.21.6, with 1,084 findings. That is about 490 bytes per finding.
- After results, the benchmark reports average 22,830 bytes (largest 45,773).
- The etcd database grew from 868,352 to 8,261,632 bytes (5,136,384 in use).
- Run 1 gave the same 124 objects and 2,370,030 bytes.
- The JSON read through the API (2,352,664 bytes) is within 0.8 % of the etcd value bytes. A read-only kubectl read is therefore enough to get the byte figure.
- After the four rollouts of run 2 phase D, the totals were 142 objects and 4,411,385 bytes. SBOM reports alone were 12 objects and 2.14 MB. The etcd database was 13.8 MB.

What sets the count:

- Per container: one vulnerability report, one exposed-secret report and one SBOM report.
- Per workload and per Service: one config-audit report.
- One report per Role, one per ClusterRole (74 here) and one per node.
- One report per benchmark.
- One cluster SBOM report and one cluster vulnerability report.

### Other facts found on the way

- **The second container of a two-container workload never gets a report.** 8 scan jobs of `web` ran, over 3 runs and 5 setups: the pinned setup, scanner 0.69.3, a 1 GiB limit, client-server mode, and operator 0.36.1 with scanner 0.76.1. None produced reports for both containers.
  - In standalone mode the error is `unable to initialize fs cache: cache may be in use by another process: timeout`.
  - In client-server mode, the other container failed on a temp-file error.
- **Memory:** the database-download init container was OOMKilled at the chart's 512 MiB limit. This happened in run 1 (one job, two attempts) and in run 3 E2 (two jobs), but not in run 2.
- **Namespace exclusion does nothing:** the platform values put `excludeNamespaces` under `operator:`, where the chart does not read it. The rendered `OPERATOR_EXCLUDE_NAMESPACES` is empty, and kube-system was scanned.
- **Timing:** the first vulnerability report came 396 to 461 seconds after install. The scan queue settled about 18 minutes after install, for 9 targets at concurrency 2.
- **Severity shift on upgrade.** Operator 0.36.1 with scanner 0.76.1, on the same images:

| Image | High (pinned → 0.36.1) | Unknown (pinned → 0.36.1) |
|---|---|---|
| nginx:1.21.6 | 308 → 156 | 18 → 537 |
| redis:6.2.6 | 263 → 121 | 12 → 402 |

  For redis, the pinned figures come from the pinned version in the same hour (run 3 E1). Critical counts and total finding counts did not change.
- **Scanner version:** the scanner itself reports that 0.76.2 is current. The pin is 0.70.0.

## Outcome

**yes.** `publishedDate` is present on the pinned version (96 % non-empty). The owner labels, the benchmark names and the per-object sizes are measured. CMP-091 and CMP-121 take the names from this record. The scanner pin does not need to move before CMP-091 for lack of a date.

## What follows for the plan

**CMP-091**
- The subject in the labels is the ReplicaSet. Map it to the Deployment through the cached ReplicaSet's controller owner reference.
- Use only reports of the Deployment's current ReplicaSet, because a stale one was seen once.
- An empty `publishedDate` means "no date". This applies to about 4 % of findings.
- On the pinned version, a multi-container workload never has a report for every container. So "passes only when every container has a report" leaves every such workload not-measured. None of the five setups tried removes this. The architect must decide how the check treats it.

**CMP-121**
- Native framework names: `k8s-cis-1.23`, `k8s-nsa-1.0`, `k8s-pss-baseline-0.1`, `k8s-pss-restricted-0.1`.
- There is no k3s profile.
- A control without `totalFail` must count as not assessed, not as pass.
- On k3s, the 87 node-check CIS controls must also count as not assessed.
- Benchmark results appear only at the next 6-hour tick. CMP-G20 must allow for that, or the chart values must set a shorter cron.

**CMP-088**
- Fix the `excludeNamespaces` key.
- Check the 512 MiB scan job limit.
- Decide on SBOM generation. SBOM reports are the second-largest kind and are never cleaned up per revision.
- Moving to 0.36.1 shifts about half of the high findings on these images to unknown. CMP-G13 sums will move on that upgrade.

**CMP-S4** takes the per-object sizes above.

## What is left

- **The count and bytes on a fresh Sovereign.** A CI job cannot hold the full platform. This needs one read on a gate environment after a train, with a read-only kubeconfig:

  `python3 tests/spikes/CMP-S3/measure.py --phase gate --out gate.json`

  The script uses only `kubectl get`, and its byte figure matches etcd within 1 %.
- **Whether old SBOM reports and stale reports leave after the 24-hour report TTL.** This needs a run longer than 24 hours.

## Evidence

- Run 1: https://github.com/openova-io/openova/actions/runs/43364114324 (commit 7e0d8ab90)
- Run 2: https://github.com/openova-io/openova/actions/runs/43371029517 (commit e956762af)
- Run 3: https://github.com/openova-io/openova/actions/runs/43377350774 (commit 5ac89dfb3)

Each run uploads the artifact `spike-cmp-s3-out`, which holds the measure, etcd, timeline, scan-failure and operator-log files.

## Evidence

Outcome: **yes**. The experiment is the branch `spike/CMP-S3` of this repository (never merged). Runs:

- https://github.com/openova-io/openova/actions/runs/43364114324
- https://github.com/openova-io/openova/actions/runs/43371029517
- https://github.com/openova-io/openova/actions/runs/43377350774

## What later tasks must take into account

Answered in CI: all four naming questions and per-object volume, on k3s v1.31.4+k3s1 with the platform's own bp-trivy 1.0.6 chart and values (only the image registry was swapped for the vendor's public copy). Outcome is yes: publishedDate is present (96 % non-empty; an empty string means no date).

Not measured in CI:
- The report count and bytes on a fresh Sovereign. A CI job cannot hold the full platform. It needs one read-only run of tests/spikes/CMP-S3/measure.py against a gate environment after a train. kubectl get is enough, because the API's JSON bytes match etcd within 0.8 %.
- Whether stale ReplicaSet reports and SBOM reports leave after the 24-hour report TTL. That needs a run longer than 24 hours.

What later packages must take into account:
- CMP-091:
  - Reports name the ReplicaSet (trivy-operator.resource.kind/name/namespace, plus container.name), never the Deployment. Map them through the ReplicaSet's owner reference, and ignore non-current ReplicaSets (one stale case was seen).
  - On the pinned operator, a two-container workload never gets a report for both containers: 0 of 8 scan jobs, across 5 setups including scanner 0.69.3, a 1 GiB limit, client-server mode and operator 0.36.1. Under the every-container rule, such workloads stay not-measured. This needs an architect decision.
- CMP-121:
  - Default benchmark names are k8s-cis-1.23, k8s-nsa-1.0, k8s-pss-baseline-0.1 and k8s-pss-restricted-0.1. There is no k3s profile.
  - The operator counts controls without checks as pass, so the adapter must treat them as not assessed.
  - On k3s, the 87 node-check CIS controls pass without evidence, because the kubeadm files they read do not exist.
  - Benchmark results appear only at the next 6-hour cron tick.
- CMP-088:
  - The platform's excludeNamespaces sits under operator:, where the chart does not read it, so kube-system is scanned.
  - The 512 MiB scan limit OOMKilled the database download in 2 of 3 runs.
  - SBOM reports are large and are never cleaned up per revision.
  - Operator 0.36.1 with scanner 0.76.1 moves about half of the high findings on the same images to unknown, which shifts CMP-G13 sums.
