# FND-201 result: what the telemetry pipeline delivers today, and which collector stays

- Date: 2026-11-23, 23:17 to 23:36 UTC
- Environment: hw307, regions A and B, about nine hours after its fresh install
- **Outcome: nothing arrives.** In both regions the metric, log and trace stores hold no data. Neither collector writes to any store, and the metric store cannot answer a query.

Collector that stays: 20-opentelemetry. It already runs the three pipelines (logs, metrics, traces) with OTLP receivers, it is the endpoint that the default Instrumentation of slot 19c names, and it uses 33 MiB per region; slot 21-alloy runs a configuration with no receiver, no scrape and no writer, on 6 pods per region that use 552 to 643 MiB.
Alert source: the Mimir ruler of slot 23-mimir, through Mimir's own Alertmanager. It is the only rule evaluator in each region and is already pointed at that Alertmanager; today it holds 0 rule groups and the Alertmanager's only receiver has no integration, so no alert arises and none would leave.
Objective object kind: PrometheusRule (monitoring.coreos.com/v1). It is the kind the repository already writes rules in (`platform/mimir/chart/templates/prometheusrule-compliance.yaml`), no kind for objectives exists in either region or in the repository, and a platform kind is excluded because the platform's entities are fixed (`docs/adr/0033-access-plane.md` line 81); its CRD and a loader into the ruler are missing in both regions.

## Question

1. Which collector pods run in each region, and which pipelines does each have?
2. Does each region's metric, log and trace store hold one metric, one log line and one trace younger than five minutes? Three named needs of the compliance area: the metric `container_memory_working_set_bytes` in every region, the API's compliance gauges, and data in each of the five provisioned dashboards.
3. How does an alert leave the metric store's ruler today?
4. Which collector stays, what is the alert source, and which kind declares an objective?

## What was run

Read only. Nothing was installed, and no object was created or changed on either cluster.

1. `kubectl get` and `kubectl logs` in both regions, each with that region's kubeconfig named in the command: nodes, the HelmReleases of slots 19c to 25, the pods of the telemetry namespaces, the CRDs, the OpenTelemetry Collector and Instrumentation objects, ServiceMonitors and PodMonitors, the ConfigMaps of both collectors, of the stores and of Grafana, and the Services. `kubectl top pod` for the two collectors.
2. HTTP `GET` requests to the stores. The API server's service proxy did not reach any store (every request timed out after 25 s), so the requests were sent with `wget` from inside the Grafana pod that already runs in each region, to the same Service URLs and with the same tenant header (`X-Scope-OrgID: anonymous`) that Grafana's datasources use.
3. The API's `/metrics` was read with `wget` from inside the running API pod.
4. The repository at the commit of this change: the charts of slots 19c to 25 and their kit files.

## Measured values

### 1. Collectors

| | Region A | Region B |
|---|---|---|
| Slot 19c, the OpenTelemetry Operator | 1 pod, Ready | 1 pod, Ready |
| Instrumentation `opentelemetry/default` | endpoint `http://opentelemetry-opentelemetry-collector.opentelemetry.svc:4317`, sampler `parentbased_traceidratio` 0.25 | the same |
| Slot 20, the OpenTelemetry Collector (contrib 0.150.1) | Deployment, 1 pod, 33 MiB | Deployment, 1 pod, 34 MiB |
| Slot 21, Alloy v1.16.0 | DaemonSet, 6 of 6 pods, 96 to 112 MiB each, 643 MiB in all | DaemonSet, 6 of 6 pods, 90 to 94 MiB each, 552 MiB in all |
| `OpenTelemetryCollector` objects | none | none |

- **Slot 20's pipelines**, read from its ConfigMap, the same in both regions:
  - `logs`: receiver `otlp`, exporter `debug`.
  - `metrics`: receivers `otlp` and `prometheus` (a scrape of its own port 8888 every 10 s), exporter `debug`.
  - `traces`: receivers `otlp`, `jaeger` and `zipkin`, exporter `debug`.
  - Processors `memory_limiter` and `batch`. The only exporter is `debug`, which writes to the pod's log. Its Service listens on 4317 and 4318 (OTLP), 14250, 14268 and 6831 (Jaeger) and 9411 (Zipkin).
  - The chart sets no `config:` (`platform/opentelemetry/chart/values.yaml`), and the kit file `clusters/_template/bootstrap-kit/20-opentelemetry.yaml` passes no values, so the upstream chart's default applies.
- **What slot 20 received.** Its log since the pod started (region A at 18:55 UTC, region B at 13:58 UTC) holds 1,659 (A) and 3,433 (B) lines `Metrics`, all from its own scrape, and 0 lines `Traces` or `Logs` in either region.
- **Slot 21's configuration**, the same in both regions: a `logging` block and six `discovery.kubernetes` blocks (pods, nodes, services, endpoints, endpointslices, ingresses). No scrape, no log source, no OTLP receiver, no writer. The chart sets `content: ''` (`platform/alloy/chart/values.yaml` line 87), and the kit file passes no values.
- **Workloads that send.** Region A has 4 pods of `chargeback` annotated `instrumentation.opentelemetry.io/inject-go: opentelemetry/default`; none of them has an `OTEL_` variable or an added container, so nothing was injected. One pod of `sso-bridge` in each region carries `inject-sdk: "false"`. No Deployment, StatefulSet or DaemonSet in either region sets an `OTEL_EXPORTER_` variable.

### 2. Stores

| | Region A | Region B |
|---|---|---|
| Metric store, Mimir 3.0.4 | ingester and store-gateway rings: 1 instance each, `ACTIVE`; `cortex_ingester_memory_series 0`, `cortex_ingester_memory_users 0` | the same |
| Query `up` and `container_memory_working_set_bytes` | `500`, `too many unhealthy instances in the ring` | `500`, the same reason |
| Log store, Loki 3.6.7 | no label in the last 5 minutes or 24 hours; `{namespace=~".+"}` over the last 5 minutes: 0 streams | the same |
| Trace store, Tempo 2.9.0 | search over the last 5 minutes and 24 hours: 0 traces; no tag name | the same |

- The metric store runs one ingester (`platform/mimir/chart/values.yaml` line 120, `replicas: 1`) with the upstream replication factor 3 (`replication_factor: 3` in its running configuration). Every query fails with the error above, read from the query frontend's and the querier's logs. Multi-tenancy is on, and Grafana uses the tenant `anonymous`.
- So no store holds one metric, one log line or one trace younger than five minutes, in either region.

### 3. The three needs of the compliance area

- **`container_memory_working_set_bytes`: missing in both regions.** The store answers no query, holds 0 series, and no collector scrapes the kubelet.
- **The API's compliance gauges: exported, never stored.** The API runs only in region A; region B has its Service and no Deployment. In region A its `/metrics` holds 1,340 series of `catalyst_compliance_score`, 28 of `catalyst_compliance_violations_total` and 1 of `catalyst_compliance_nats_publish_failures_total`. `catalyst_compliance_policy_mode` has no series. No ServiceMonitor or PodMonitor exists, and no collector scrapes the API, so none of them reaches the store.
- **The five provisioned dashboards: no data in any of them, in either region.** The ConfigMap `grafana-starter-dashboards` holds the five: `cluster-overview`, `flux-reconcile-health`, `kube-state-summary`, `node-exporter-summary`, `openbao-audit`.
  - Four read the metric store, with metric names of kube-state-metrics (`kube_pod_status_phase`, `kube_node_status_condition`, `kube_namespace_created`, `kube_deployment_status_replicas_ready`), of the node exporter (`node_cpu_seconds_total`) and of the Flux controllers (`gotk_reconcile_condition`). No kube-state-metrics and no node exporter runs in either region, and the store answers no query.
  - `openbao-audit` reads the log store: `sum by (type) (rate({namespace="openbao"} |= "audit" [1m]))` returns an empty vector in both regions.

### 4. How an alert leaves the ruler today

- **Ruler**: 1 pod per region, its rule API on (`ruler_config_api: true` in its build information). `GET /prometheus/api/v1/rules` returns `{"groups":[]}`, `GET /prometheus/config/v1/rules` returns `{}`, and `cortex_ruler_managers_total` is 0. It sends to `dnssrvnoa+http://_http-metrics._tcp.mimir-alertmanager-headless.mimir.svc.cluster.local./alertmanager`; that Service has the port `http-metrics` (8080).
- **Alertmanager**: 1 pod per region, status `ready`, no peer. `cortex_alertmanager_tenants_owned 0`. The tenant `anonymous` has no configuration (`alertmanager_config: ""`), so the fallback applies: one route to one receiver, `default-receiver`, which has no integration.
- **No rule object reaches the ruler.** The cluster has no `PrometheusRule` CRD; the CRDs of that group that exist are `ServiceMonitor`, `PodMonitor` and `Probe`, put there by `bp-loki` (region A) and `bp-mimir` (region B). So the chart's compliance rules, gated on that CRD (`platform/mimir/chart/templates/prometheusrule-compliance.yaml` line 36), never render. Nothing loads rule groups into the ruler's API.
- Grafana's Alertmanager datasource is off (`platform/grafana/chart/values.yaml` line 434). Neither region has a Flux `Alert` or `Provider`.
- NATS JetStream runs in `nats-system` in both regions (three server pods each).
- **Today no alert leaves.** There is no rule, and an alert that fired would reach a receiver with no integration and stop there.

## The three answers

- **Collector that stays: 20-opentelemetry.** Neither collector delivers today, so the choice rests on what each has. Slot 20 has the three pipelines and the OTLP receivers, the default Instrumentation already points workloads at its Service, and the three stores accept OTLP at the measured releases (Tempo's Service listens on 4317 and 4318; Loki's configuration has an `otlp_config`; Mimir's has its `otel_` settings). Slot 21 has no pipeline at all, and its 6 pods per region cost 16 to 20 times the memory of slot 20's one pod. Slot 21 leaves.
- **Alert source: the Mimir ruler of slot 23-mimir, through Mimir's Alertmanager.** It is the only rule evaluator in a region, its rule API is on, and its link to the Alertmanager is in place. An Alertmanager sends both firing and resolved notifications to a webhook receiver, so a receiver beside it sees each change of state (by the upstream documentation; no alert fired here). The collector evaluates no rules.
- **Objective object kind: PrometheusRule (monitoring.coreos.com/v1).** No objective kind exists in either region (no CRD of OpenSLO, Pyrra or Sloth) or in the repository. A kind of the platform's own group is excluded: the platform's entities are fixed (`docs/adr/0033-access-plane.md` line 81). An upstream objective controller such as Pyrra or Sloth writes `PrometheusRule` objects itself, so it would need the same CRD and the same loader, plus one more component. The repository already writes its rules in this kind.

## What later tasks must take into account

1. **FND-202** (decision record 0035): the three lines at the top of this file. The slot that leaves is `21-alloy`. The endpoint for traces from workloads is the one already in the Instrumentation, `opentelemetry-opentelemetry-collector.opentelemetry.svc:4317`.
2. **FND-203** (one collector pipeline per cluster):
   - Replace the `debug` exporter in all three pipelines by writers to Mimir, Loki and Tempo, with the tenant header.
   - The metric store must accept data first: one ingester with replication factor 3 refuses every query, and by Mimir's documented quorum rule it refuses writes too. Either the replication factor becomes 1 or the ingesters become 3. That is a change to `platform/mimir/chart/`, which lies outside FND-203's paths: the architect places it.
   - Container logs and `container_memory_working_set_bytes` (from the kubelet's cAdvisor) need a collector on every node or a scrape of every kubelet. Slot 20 runs as one Deployment today.
   - Scrape the API's `/metrics` for the compliance gauges (region A only, where the API runs).
   - Removing slot 21 removes no CRD that other slots need: the `monitoring.coreos.com` CRDs come from `bp-loki` and `bp-mimir`.
   - The `inject-go` annotation on the `chargeback` pods injects nothing today.
3. **FND-204** (alerts as events on the bus): the alert source is the ruler, so its paths in the Mimir chart hold. It needs a webhook receiver in the tenant's Alertmanager configuration and the component that turns each notification into the event on NATS. No rule can fire before FND-203 makes the metric store accept data.
4. **FND-205** (declared objectives and FND-G19): objectives are `PrometheusRule` objects. Both regions lack the CRD and a loader that writes these objects into the ruler's rule API; that loader also brings in the chart's compliance rules that render nowhere today. The dashboards need kube-state-metrics, node exporter and Flux controller metrics, which nothing collects today.

## Not measured

- Grafana's own panels after a sign-in. The dashboards' queries were sent to the stores directly, with Grafana's URLs and tenant.
- A write into any store, a fired alert, a notification: nothing was written.
- The collectors' own metrics ports and the API's `/metrics` from the Grafana pod: those requests timed out, as did the service proxy of the API server.
- Grafana Alloy's `mimir.rules.kubernetes` component, which loads `PrometheusRule` objects into a Mimir ruler, was not tried: it belongs to the collector that leaves.

## Evidence

Outcome: **nothing arrives**. All values come from read-only `kubectl get`, `kubectl logs`, `kubectl top` and `wget` requests made by hand on 23 November 2026 from the agents' host, with the kubeconfig of each region named in the command. There is no CI run: it needs the environment's credentials, which CI does not hold.

- 23:17:35 UTC: inventory of both regions (nodes, HelmReleases, pods, CRDs, collector objects, Services, ConfigMaps). All seven HelmReleases of slots 19c to 25 are Ready in both regions.
- 23:18:04 UTC: both collectors' ConfigMaps, arguments and logs.
- 23:18:41 UTC: the service proxy of the API server to the stores, every request timed out.
- 23:25:55 UTC: query `up` in region A, `500`; the reason read from the query frontend's and the querier's logs.
- 23:26:34 UTC (A) and 23:27:51 UTC (B): rings, ingester series, ruler, Alertmanager, Loki and Tempo from the Grafana pod.
- 23:31:20 UTC (A) and 23:32:13 UTC (B): store releases and configuration, collector memory, collector log line counts, injected pods, CRD owners, NATS.
- 23:33:30 UTC: the API's `/metrics` in region A; region B runs no API pod.
- 23:35:46 UTC: query `container_memory_working_set_bytes` in region B, `500`, the same reason.

Resources the test created, and their end state:

- nothing was created
- the temporary working folders on the host were deleted at the end
