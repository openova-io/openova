
# CMP-S4: etcd load from report objects

## Question

Is etcd load from report objects a problem? The trigger set for this test is 20,000 report objects or 200 MB of reports in one region. Above it, the policy engine's report server would be installed on the platform's PostgreSQL operator before CMP-091. Below it, reports stay in etcd and the number is measured again at each train.

Report objects are every object in the API groups `aquasecurity.github.io` (the scanner), `wgpolicyk8s.io`, `reports.kyverno.io` and `openreports.io` (the policy engine). Bytes are the stored value bytes in etcd, read from etcd itself. MB means 10^6 bytes.

## Where it ran

Everything ran in GitHub Actions on branch `spike/CMP-S4` of openova-io/openova. The branch is never merged. It holds three commits: 4e9f1ddb5, c9997e9e9 and 2915cdebf. Files:

- `.github/workflows/spike-cmp-s4.yaml`
- `tests/spikes/CMP-S4/`: `run.sh`, `etcd_stats.py`, `probe.py`, `load.py`, `workloads.py`, `inventory.py`, `s4lib.py`, `postrender.sh`

No gate environment, cloud account or secret was used.

- **Run 1** (43366737256): the load job. It measured the scanner family only. The platform's policy-engine values issue the engine's webhook certificates through cert-manager, which the job had not installed, so the engine did not start.
- **Run 2** (43377178202): the load job with cert-manager added, plus an inventory job.
- **Run 3** (43379178779): the inventory job alone, with a rendering fix.

## What was run

**Runner.** ubuntu-24.04 with 4 vCPU and 16 GB. A Sovereign control-plane node (m7n.large.8) has 2 vCPU and the same 16 GB.

**Cluster.** One k3s v1.31.4+k3s1 node with embedded etcd 3.5.16. It used the server flags the Huawei provisioner passes:
- `--cluster-init`, `--disable-network-policy`, traefik, servicelb and local-storage disabled
- `max-pods=220`
- `default-watch-cache-size=200`
- `watch-cache-sizes=secrets#500,configmaps#500`
- `--etcd-snapshot-retention=5`

Flannel replaced Cilium. There is no etcd quota flag, so etcd logged its default quota of 2,147,483,648 bytes.

**Tools.** helm 3.16.3, kubectl 1.31.4, etcdctl 3.5.17.

**Report producers, with the platform's own charts and values:**
- cert-manager v1.16.5 (the version bp-cert-manager pins).
- bp-kyverno 1.3.9: kyverno chart 3.8.0, engine v1.18.0.
- bp-kyverno-policies 1.0.53: 24 ClusterPolicies, all Ready, all in Audit (chart default).
- bp-trivy 1.0.6: trivy-operator chart 0.32.1, operator 0.30.1, trivy 0.70.0.
- Flux v2.4.0 CRDs only (the policies match Flux kinds).

**What differs from the platform:**
- A post-renderer replaced the Harbor proxy prefix with the upstream registry.
- The engine's webhook-gate hook was switched off.
- Scan concurrency was 3 in run 1 and 6 in run 2, instead of 2.

**Workloads.** 21 components with 24 containers, using images that platform charts pin today. They include keycloak, postgresql, valkey, harbor-core, harbor trivy-adapter, grafana, loki, otel-collector, cnpg, openbao, stalwart, vcluster, falco, alloy, velero, oauth2-proxy, reloader, alpine/k8s and nginx. Each component got its own namespace, ServiceAccount, Role, RoleBinding, ConfigMap and Service. The kinds were Deployment, StatefulSet, DaemonSet, CronJob and Pod.

22 images were pullable. The two catalyst images on ghcr are not anonymously pullable and were left out. Three Deployments (grafana, keycloak, harbor-core) were each rolled three times.

**Phases of the load job:**
- **B0:** bare cluster.
- **R:** real reports, after scanning and the rolls.
- **T:** the real report objects cloned (body unchanged, new name, no owner) until both 20,000 objects and 200 MB were exceeded.
- **T2:** the same, up to twice that.

**Measured at each phase:**
- Objects and bytes per resource, read page by page from etcd, and the etcd database size.
- Watch latency for small objects: 300 ConfigMaps created at 20 per second, with a namespace watch.
- Watch latency for report objects: 100 label changes at 5 per second on the largest real vulnerability report (920 KB), with a cluster-wide watch like an informer.
- Full list of each report kind, from the watch cache and paginated from etcd.
- API-server metrics. k3s runs the API server and etcd in one process.
- Memory and restarts of the producers.
- etcd slow-request warnings.

**Inventory job.** It rendered every HelmRelease of the bootstrap kit from its local chart with its inline values, and counted:
- scan subjects, and their containers plus init containers
- other objects the scanner and the engine report on
- CNPG clusters

## Measured values

### Reports from real workloads (phase R)

**Run 1, scanner only:** 223 report objects, 15.77 MB.

| Kind | Objects | Bytes | Average | Median | Largest |
|---|---|---|---|---|---|
| SBOM reports | 27 | 10,094,043 | 373,853 | 274,969 | 1,011,154 |
| Vulnerability reports | 22 | 5,095,286 | 231,603 | 143,019 | 920,514 |
| Config-audit reports | 43 | 249,895 | 5,811 | | |
| Cluster RBAC assessment reports | 74 | 162,090 | 2,190 | | |
| RBAC assessment reports | 28 | 63,397 | 2,264 | | |
| Exposed-secret reports | 22 | 42,801 | 1,945 | | |

**Run 2, both families:** 383 report objects, 13.90 MB.

| Kind | Objects | Bytes | Average | Median |
|---|---|---|---|---|
| SBOM reports | 26 | 8,143,158 | 313,198 | |
| Vulnerability reports | 26 | 4,689,321 | 180,358 | 113,253 |
| Policy reports | 73 | 303,637 | 4,159 | |
| Cluster policy reports | 24 | 38,889 | 1,620 | |

**What the R snapshot shows:**
- SBOM reports are 64 percent (run 1) and 59 percent (run 2) of the bytes. SBOM plus vulnerability reports are 96 and 92 percent.
- The policy engine's reports are 2.5 percent.
- Per scanned container, the vulnerability, SBOM and exposed-secret reports average 0.50 to 0.61 MB, with a median of 0.38 to 0.42 MB.
- The largest single report is 1,011,154 bytes. That is 64 percent of etcd's default 1.5 MiB request limit.
- There is one policy report per Deployment, Pod and Service, and none per ReplicaSet.

### Old revisions

After three rolls of three Deployments, old ReplicaSets kept scanner reports:
- Run 1: an SBOM on 9 of 9 old ReplicaSets, and a vulnerability report on 2 of 9.
- Run 2: both reports on 5 of 9. The other 4 were replaced before their scan ran.

The scanner's source at v0.30.1 explains this. Its TTL controller (`pkg/operator/ttl_report.go`, chart default `scannerReportTTL: 24h`) expires vulnerability, exposed-secret, config-audit, infra, RBAC and cluster-SBOM reports. It never expires namespaced SBOM reports. So an old ReplicaSet's SBOM stays as long as the ReplicaSet does, which is up to `revisionHistoryLimit`, 10 by default.

### At the trigger and at twice the trigger

| | Run 1, T | Run 2, T | Run 1, T2 | Run 2, T2 |
|---|---|---|---|---|
| Report objects | 20,671 | 20,710 | 41,242 | 41,285 |
| Report bytes (MB) | 246.4 | 229.1 | 446.8 | 444.4 |
| etcd database (MB) | 359.5 | 355.9 | 681.1 | 649.3 |
| API server + etcd memory, before list probes (MB) | 4,641 | 4,484 | 7,298 | 6,643 |
| Same, after a full re-list of every report kind (MB) | 7,228 | 6,473 | 9,533 | 10,555 |
| Scanner operator memory (MiB; limit 1,024) | 546 | 508 | 1,015 | 886 |

The etcd quota is 2,147 MB. For comparison, the API server and etcd process used 473 to 477 MB at B0 and 1,131 to 1,208 MB at R.

**Memory scales with report bytes:**
- The API server and etcd process grew by about 13 to 14 MB per MB of reports.
- The scanner operator grew by about 1.6 to 2.0 MiB per MB. At T2 it reached 1,015 MiB against its 1 GiB limit, with no restart yet.
- The policy engine's reports controller used 46, 54 and 71 MiB (limit 256 MiB).
- Clones were written at 318 to 445 objects per second with no failure.

**Watch latency for small objects** is unchanged by load. In every phase of both runs, the event arrived at most 1.5 ms after the write was acknowledged. End-to-end p99 was 4.5 to 5.1 ms at R and 3.0 to 4.0 ms at T and T2.

**Watch latency for report objects** (920 KB events), at R, T and T2:
- Event lag after the write was acknowledged, p99: 33, 39 and 112 ms (run 1); 56, 78 and 104 ms (run 2).
- End-to-end p99: 401, 520 and 1,301 ms (run 1); 544, 729 and 1,457 ms (run 2).
- The write itself dominates. Patch p99 was 383, 496 and 1,284 ms (run 1); 495, 707 and 1,441 ms (run 2).

**List latency.** A full list of SBOM reports from the watch cache took:
- 0.13 s at R
- 2.2 to 2.3 s at T (122 to 141 MB)
- 3.5 to 7.2 s at T2 (236 to 252 MB)

Paginated lists from etcd took 9.8 to 14.6 s at T2. A full list of vulnerability reports at T2 took 2.2 to 11.5 s.

**etcd warnings.** During the T2 bulk write, reads of unrelated keys took over 100 ms: Services 723 ms, HelmReleases 598 ms. Paged SBOM range reads took 1.7 s.

### What the report server would cover

The policy engine's chart (3.8.0) bundles reports-server v0.1.6 with its own PostgreSQL subchart. Its storage switches are `--storereports`, `--storeephemeralreports` and `--storeopenreports`. Those are the engine's three report families only. The scanner's kinds stay in etcd. Installing it would move about 2.5 percent of the bytes at R and 5 percent at T.

The scanner chart (0.32.1) has `operator.sbomGenerationEnabled: true` as default and also offers `alternateReportStorage`, which writes reports as JSON files on a volume instead of CRDs.

### What one region declares (runs 2 and 3)

66 of 68 bootstrap-kit HelmReleases rendered. bp-velero and bp-velero-hcs failed on a schema check. The kit declares:
- 141 scan subjects with 190 containers: 105 Deployments, 21 StatefulSets, 10 DaemonSets, 4 CronJobs and 1 Job.
- 53 more hook Jobs.
- 138 declared Pods, plus 10 DaemonSets on every node.
- 9 CNPG clusters.
- 146 Services, 96 ConfigMaps, 41 Roles and 109 ClusterRoles.

With 6 nodes and the CNPG instances, that gives about 207 Pods. This matches the 257 Pods recorded for one region of a live Sovereign once runtime Jobs and Organization Pods are added.

### Side observations

- Scanning ran at about three workloads per five minutes. Finished scan Jobs are kept for `scanJobTTL` 5m and count against the concurrency limit.
- 20 of 22 containers (run 1) and 13 of 22 (run 2) had reports when R was measured.
- With concurrency 6, the k3s server restarted once during scanning, and etcd slow-request warnings rose from 23 to 2,315.

## Outcome: below

This is a projection, not a measurement of a gate region.

**A fresh region is below both triggers.** It has about 208 scanned containers: 190 rendered plus 9 CNPG instance Pods with two containers each. At 0.38 to 0.61 MB per container that is about 80 to 130 MB. The small kinds add about 5 MB. The count is about 2,000 report objects, roughly 10 percent of 20,000.

**The bytes can still cross 200 MB in a region that has been rolled many times.** Each roll keeps one more SBOM per container (0.31 to 0.37 MB), up to 10 per Deployment. The trigger is reached at about 200 to 370 retained SBOMs. That is about 20 to 35 Deployments at full history, or every Deployment rolled two or three times.

**The trigger is sensibly placed.** At the trigger:
- The API server and etcd use 4.5 GB steady and 6.5 to 7.2 GB after a re-list, on a 16 GB control plane.
- Report watch end-to-end p99 is 0.5 to 0.7 s.

At twice the trigger, the scanner operator reaches its 1 GiB limit.

## What follows

1. Reports stay in etcd for now. Measure again at each train with the read-only etcd reader of this test, and record both numbers per kind. Measure the gate region right after the train, and also a long-lived Sovereign.
2. If the number crosses 200 MB, the policy engine's report server is the wrong package: it moves at most 5 percent of the bytes. Relief has to come from the scanner. In order of effect:
   - switch off in-cluster SBOM reports, which are 59 to 64 percent of the bytes and never expire;
   - lower `revisionHistoryLimit` on frequently rolled Deployments;
   - or use the scanner's alternate report storage.
3. Anything that caches scanner kinds grows with report bytes. The scanner operator grows about 2 MiB per MB, and the API's cache of six scanner kinds is the same kind of cache. Size their memory limits for twice the trigger, or trim cached fields.
4. One SBOM report already reaches 1.01 MB, close to etcd's 1.5 MiB limit. A larger image would fail to write its report and show as not measured.

**Not measured here:**
- A real gate region's report count and bytes.
- The SBOM sizes of the platform's own private images.
- The `revisionHistoryLimit` that each platform chart sets.

The first two need read access to a gate environment, or a pull credential.

## Evidence

Outcome: **below**. The experiment is the branch `spike/CMP-S4` of this repository (never merged). Runs:

- https://github.com/openova-io/openova/actions/runs/43366737256
- https://github.com/openova-io/openova/actions/runs/43377178202
- https://github.com/openova-io/openova/actions/runs/43379178779

## What later tasks must take into account

I could not measure a gate environment from CI: no cluster outside the job is allowed. So the outcome "below" is a projection. It rests on report sizes per scanned container measured in CI with the platform's own charts and pinned images (0.38 to 0.61 MB), times a region inventory rendered from the bootstrap kit (about 208 scanned containers, about 2,000 report objects). That gives about 80 to 130 MB for a fresh region, against 200 MB and 20,000 objects.

The two catalyst images on ghcr are not anonymously pullable, so their report sizes are not in the sample. bp-velero and bp-velero-hcs did not render (schema), so their few containers are missing from the inventory.

What later packages must take into account:

(1) The scanner's reports are 92 to 96 percent of report bytes. SBOM reports alone are 59 to 64 percent.

(2) The policy engine's bundled report server (reports-server v0.1.6 in kyverno chart 3.8.0) stores only policy, ephemeral and open reports, about 2.5 to 5 percent of the bytes. So if the trigger is crossed, the planned package installing it before CMP-091 does not relieve etcd. Relief must be scanner-side:
- turn off in-cluster SBOM reports (upstream default `sbomGenerationEnabled: true`);
- cap revision history;
- or the scanner's `alternateReportStorage`.

(3) The scanner's 24h TTL (operator 0.30.1) never expires namespaced SBOM reports. Every Deployment roll keeps one more SBOM per container until the ReplicaSet is pruned (up to 10). A long-lived, often-rolled region can therefore cross 200 MB. The per-train re-measurement must include a long-lived Sovereign, not only a fresh gate region.

(4) Memory scales with report bytes:
- the API server and etcd process by about 13 to 14 MB per MB of reports: 4.5 GB steady and up to 7.2 GB after a re-list at the trigger, on a 16 GB control plane;
- the scanner operator by about 2 MiB per MB: it reaches its 1 GiB limit at about twice the trigger;
- the API's own cache of scanner kinds (CMP-091, unknown U2) will scale the same way.

(5) Watch latency for ordinary objects is unaffected even at twice the trigger (lag at most 1.5 ms). Report-object writes and events slow to about 1.3 to 1.5 s p99 at twice the trigger.

(6) One SBOM report is already 1.01 MB, against etcd's 1.5 MiB request limit.

(7) Side note for the scan path: finished scan Jobs hold a concurrency slot for scanJobTTL (5m), so throughput was about 3 workloads per 5 minutes. Raising concurrency to 6 on one node restarted k3s under scan I/O.

The branch `spike/CMP-S4` stays in place as evidence. Three of the five allowed pushes were used.
