# ADR-0035 — Observability: one telemetry pipeline per cluster, alerts as events on the bus, objectives as rule objects in Git

Status: Accepted
Date: 2026-11-24
Unfreezes: topology-region-git-layout

Written by work package FND-202, from the result of spike FND-201 (`docs/ledger/plan/spikes/FND-201.md`), measured in both regions of the standing test environment on 2026-11-23. It decides O1 to O9: the one collector of a cluster and the slot that leaves, how it runs, its three pipelines and where each writes, the endpoint that workloads send to, where an alert arises, the event that carries an alert onto the bus with its twelve fields, the component that publishes that event, and the kind and the folder of a service-level objective. The packages FND-203, FND-204 and FND-205 of epic FND-200 build it.

Terms used here:

- **The collector**: the OpenTelemetry Collector of kit slot 20, chart `platform/opentelemetry/chart`, kit file `clusters/_template/bootstrap-kit/20-opentelemetry.yaml`.
- **The three stores**: the metric store Mimir (slot 23, `platform/mimir/chart`), the log store Loki (slot 22, `platform/loki/chart`) and the trace store Tempo (slot 24, `platform/tempo/chart`). Each region has its own three.
- **The bus**: the JetStream stream `CATALYST_ORG` of a region's NATS (slot 07), whose subjects are `catalyst.>` (`platform/nats-jetstream/chart/templates/streams.yaml`). Each region has its own.
- **`<region-id>`**: the canonical id of a region, the value that the region's Flux receives as `SOVEREIGN_REGION_ID`. It is the region's cluster name.

## Context

These facts are from FND-201 unless another source is named.

- Nothing arrives. In both regions the three stores hold no metric, no log line and no trace: no collector writes to any store.
- The kit holds two collectors side by side. Slot 20 runs one pod per region (33 MiB) with the pipelines `logs`, `metrics` and `traces`, OTLP receivers on ports 4317 and 4318, and `debug` as its only exporter. Its chart sets no pipeline and runs `mode: deployment` (`platform/opentelemetry/chart/values.yaml`). Slot 21, Alloy (`platform/alloy/chart`, kit file `clusters/_template/bootstrap-kit/21-alloy.yaml`), runs six pods per region, 552 to 643 MiB in all, with a configuration that has no receiver, no scrape and no writer. The text of `platform/opentelemetry/blueprint.yaml` says that each Sovereign chooses one of the two.
- The default Instrumentation of slot 19c already points workloads at the collector's Service: `exporterEndpoint` in `platform/opentelemetry-operator/chart/values.yaml` is `http://opentelemetry-opentelemetry-collector.opentelemetry.svc:4317`. No workload sends anything today.
- The metric store runs one ingester (`ingester.replicas: 1` in `platform/mimir/chart/values.yaml`) under the upstream replication factor 3, and it refuses every query with `too many unhealthy instances in the ring`.
- The ruler of the metric store is the only rule evaluator of a region. Its rule API is on, it holds no rule group, and it sends to the metric store's own Alertmanager. The tenant `anonymous` has no Alertmanager configuration, so the fallback receiver applies, which has no integration. No region has the `PrometheusRule` CRD, so the rules of `platform/mimir/chart/templates/prometheusrule-compliance.yaml` render nowhere, and nothing loads a rule group into the ruler.
- The metric store has tenants, and Grafana reads it as the tenant `anonymous` (`platform/grafana/chart/values.yaml`). The log store and the trace store run without tenants: `auth_enabled: false` in `platform/loki/chart/values.yaml`, `multitenancyEnabled: false` in `platform/tempo/chart/values.yaml`.
- The event contract holds the subject `catalyst.alert.changed.v1` with envelope `cloudevents`, owner FND, delivery `at-least-once`, and no producer and no consumer yet (`docs/contracts/C3-event-subjects.yaml`, registered by CTL-044). Its payload schema `docs/contracts/C3-alert-changed-v1.schema.json` requires twelve fields. The operations-records service already reads such events (`products/ops-records/internal/signals/signals.go`, AIP-222).
- No kind for objectives exists in either region or in the repository. The platform's entities are fixed (`docs/adr/0033-access-plane.md`, A1).

## Decision

The governing rule: **one collector per cluster writes every signal to the store of its region, every alert arises in the ruler of the metric store and leaves it as one event on the bus, and an objective is a rule object in Git.**

**O1 One collector per cluster.** The OpenTelemetry Collector of slot 20 is the one collector of every cluster of a Sovereign. Slot 21 leaves: FND-203 deletes `platform/alloy/` and `clusters/_template/bootstrap-kit/21-alloy.yaml`, removes the slot from the kit list and from the catalogue, and the Blueprint text no longer offers a choice. No other collector, agent or log shipper runs beside it.

**O2 It runs on every node.** The collector runs in mode `daemonset`: one pod on every node of the cluster, all pods with one configuration. Container logs and the kubelet's metrics are read on the node they come from. Each pod scrapes only targets on its own node, so no target is scraped twice. The Service `opentelemetry-opentelemetry-collector` in namespace `opentelemetry` keeps its name and its OTLP ports.

**O3 Three pipelines, three writers.** One pipeline per signal, each with the processors `memory_limiter` and then `batch`. The exporter `debug` leaves every pipeline. The receivers of the table are the only ones, apart from the collector's scrape of its own metrics.

| Pipeline | Receives | Writes to |
|---|---|---|
| `metrics` | OTLP; the kubelet of its node, with the container metrics (among them `container_memory_working_set_bytes`); the metrics port of the API's pods on its node (the compliance gauges) | the metric store, OTLP over HTTP, at `http://mimir-gateway.mimir.svc.cluster.local:80/otlp` (the gateway passes `/otlp/v1/metrics` to the distributor) |
| `logs` | OTLP; the container log files of its node | the log store, OTLP over HTTP, at `http://loki.loki.svc.cluster.local:3100/otlp` |
| `traces` | OTLP | the trace store, OTLP over gRPC, at `tempo.tempo.svc.cluster.local:4317` |

Every writer sends the header `X-Scope-OrgID: anonymous`. The metric store files the data under that tenant, the one Grafana reads; the log and trace stores, which run without tenants, ignore it.

**O4 The metric store accepts what it is sent.** The replication factor of the metric store's ingesters equals the number of ingesters it runs: 1 today. FND-204, which changes `platform/mimir/chart/values.yaml`, sets it. Under replication factor 3 with one ingester the store refuses every query and, by the same quorum rule, every write.

**O5 One endpoint for workloads.** A workload sends its traces, metrics and logs over OTLP to `opentelemetry-opentelemetry-collector.opentelemetry.svc:4317` (gRPC) or port 4318 (HTTP), the endpoint the default Instrumentation `opentelemetry/default` names. A workload never writes to a store directly.

**O6 Where an alert arises.** An alert arises in the ruler of the metric store of its region and leaves through the metric store's Alertmanager. Neither the collector nor Grafana evaluates an alert rule. The Alertmanager configuration of the tenant `anonymous` has one receiver: a webhook to the alert relay of O8, with `send_resolved: true`. It groups by every label (`group_by: ['...']`), so one notification carries one alert. An alert that still fires is notified again at the Alertmanager's repeat interval, and the relay publishes that as one more firing event with the same `alertId`.

**O7 The alert event.** The subject is `catalyst.alert.changed.v1` of contract C3; it is registered. Each alert of a notification is published as one CloudEvent 1.0 in structured JSON mode: `specversion` `1.0`; `id` unique for each event; `type` `io.openova.catalyst.alert.changed.v1`; `source` `alert-relay/<sovereign-fqdn>/<region-id>`; `subject` the Organization's slug, absent for a platform alert; `time` when it is published, in RFC 3339 and UTC; `datacontenttype` `application/json`. The data holds the twelve fields of `docs/contracts/C3-alert-changed-v1.schema.json`, taken from the alert as the Alertmanager's webhook delivers it:

| Field | Value |
|---|---|
| `alertId` | the alert's `fingerprint`, the same when it fires and when it is resolved |
| `organization` | the label `organization`; the empty string for a platform alert |
| `cluster` | the label `cluster` when the rule sets it, otherwise the `<region-id>` of the relay's region |
| `namespace` | the label `namespace`; empty when absent |
| `entityKind` | the label `entity_kind`; empty when absent, and then the entity is the cluster |
| `entityName` | the label `entity_name`; empty when absent |
| `name` | the label `alertname` |
| `state` | the alert's `status`: `firing` or `resolved` |
| `severity` | the label `severity`; empty when absent |
| `startsAt` | the alert's `startsAt`, in RFC 3339 |
| `summary` | the annotation `summary`; empty when absent |
| `labels` | every label of the alert |

A rule names the entity it is about with the labels `entity_kind` and `entity_name`, and a rule about an Organization carries the label `organization`. This is the format that the operations-records service reads.

**O8 The alert relay.** A small first-party Go service, `alert-relay`, in `core/services/alert-relay`, built and published like `core/services/model-meter` (ADR-0030, M10). The Mimir chart runs one replica of it in the namespace `mimir` of every region, beside the Alertmanager, behind a Service of type ClusterIP. It accepts the Alertmanager's webhook notification, publishes one event of O7 per alert to the bus of its own region at `nats://nats-jetstream.nats-system.svc.cluster.local:4222`, and answers with a 2xx status only after JetStream has acknowledged every event of the notification. Otherwise it answers with a 5xx status, and the Alertmanager sends the notification again. That gives the registered delivery, at least once. The relay creates no stream. When the bus requires a credential, the relay receives it as a mounted file under ADR-0018 (S11), never as a Secret object. FND-204 adds `alert-relay` to the producers of the subject's entry in `docs/contracts/C3-event-subjects.yaml`.

**O9 Objectives are rule objects in Git.** A service-level objective is one object of kind `PrometheusRule` (`monitoring.coreos.com/v1`). Its recording rules compute the indicator, and its alerting rules fire when the error budget burns too fast; those alerts reach the bus by O6 to O8. The platform's own objectives live in the folder `clusters/_template/sovereign-region/observability/`, one object per file, and every `PrometheusRule` in that folder is an objective. Every region applies the folder: a Flux Kustomization of its own applies it, and that Kustomization's file lies in `clusters/_template/sovereign-region/base/`, the folder that the Kustomization `sovereign-region-bootstrap` applies in every region, and is listed in its `kustomization.yaml`. A missing CRD then fails that one Kustomization and never the delivery of the region's path. Each region's ruler evaluates the same objectives on its own data.

## What is open

- **The rule loader.** The CRD `prometheusrules.monitoring.coreos.com` and a loader that writes each `PrometheusRule` of its region into the ruler's rule API are missing in both regions. Both are needed before an objective, or a rule of `platform/mimir/chart/templates/prometheusrule-compliance.yaml`, is evaluated, so before FND-205's check can pass. The plan places them. The loader is no second telemetry collector and receives no credential as a Secret object.
- **Where alert consumers run.** Each region publishes its alerts to its own bus. Whether a consumer in one region reads the alerts of the other is not decided here.
- **The data of the five provisioned dashboards.** Four of them read metrics of kube-state-metrics, of the node exporter and of the Flux controllers, which no component of the kit exports to a store today. This record adds no exporter.

## Alternatives rejected

**O1.** (a) Keep both collectors and let each Sovereign choose. That is two pipelines per cluster, and every check and dashboard would have to know which one runs. (b) Keep Alloy. It has no pipeline today, uses 16 to 20 times the memory of slot 20, and is not the endpoint that the Instrumentation names.

**O2.** (a) One pod per cluster, as today. It cannot read the container logs or the kubelet of other nodes. (b) A gateway tier in front of node agents. That is a second tier of the one collector for stores in the same cluster; nothing measured asks for it.

**O6.** (a) Consumers read the Alertmanager's list of alerts. That is a poll. (b) Grafana's alerting. That is a second rule evaluator beside the ruler.

**O8.** (a) The collector publishes the event. Its contrib release 0.150 has no NATS exporter. (b) The API of the Sovereign receives the webhook. It runs in one region only (FND-201: the second region has no API pod), so the other region's alerts would depend on it. (c) An upstream event router. That is one more component and image in the kit for a mapping of twelve fields, and the subject is registered with envelope `cloudevents`, the envelope of a first-party producer; contract C3 keeps `foreign` for upstream producers.

**O9.** (a) A platform kind for objectives. The platform's entities are fixed. (b) An objective controller such as Pyrra or Sloth. Each writes `PrometheusRule` objects itself, so it needs the same CRD and the same loader, plus one more component. (c) The every-region intent folder of contract C4 (`docs/contracts/C4-git-layout-and-commit-library.md`, section 2.2). That holds intent that a Sovereign writes through its API; the platform's own objectives are the same on every Sovereign and arrive with the platform.

## Consequences

- Once FND-203 has merged, each cluster runs one collector, and slot 21 is gone. Until FND-204 has set the replication factor, the metric store still refuses what the collector sends.
- The collector runs one pod per node, so its cost grows with the number of nodes.
- A workload that sends to the Instrumentation's endpoint keeps working: the endpoint does not change.
- No alert fires before the rule loader exists. Consumers are tested with a synthetic event until then (FND-204 adds it to the acceptance suite).
- Each region's alerts reach that region's bus only.

## What the Unfreezes line permits

The entry `topology-region-git-layout` of the stability register, `docs/ledger/stability.yaml`, holds `clusters/_template/sovereign-region/**`. Record 0035 is cited for one change only: FND-205 adds the folder `clusters/_template/sovereign-region/observability/` with the platform's objectives, and in `clusters/_template/sovereign-region/base/` the file of the Kustomization of O9 and its line in `kustomization.yaml`. A pull request of FND-205 that changes those paths carries the trailer `Decision: ADR-0035`.

## Shipped via

FND-202 writes this decision record. FND-203 builds O1, O2, O3 and O5. FND-204 builds O4, O6, O7 and O8, with the synthetic alert event of the acceptance suite. FND-205 builds O9 and the gate check FND-G19.
