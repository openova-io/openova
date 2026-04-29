# Component Logos

The OpenOva Catalyst wizard's component picker (Step 5: Components,
`StepComponents.tsx`) renders a 3-column card grid that pixel-mirrors the
SME marketplace (`core/marketplace/src/components/AppsStep.svelte`). Each
card displays the component's brand mark from a vendored SVG file under
`products/catalyst/bootstrap/ui/public/component-logos/<id>.svg`.

This doc tracks the source and licence of each logo SVG so the
asset library can be audited, re-vendored, or swapped for canonical
upstream art when permission/license is verified.

## How it works

`componentGroups.ts` declares each component with an optional `logoUrl`
field. The default value (when omitted) is `/component-logos/<id>.svg`,
which Vite serves from the wizard's `public/` directory. To override:

- **Use a vendored upstream SVG**: replace the file at
  `public/component-logos/<id>.svg`. No code change required —
  the URL is data, not source (per
  [INVIOLABLE-PRINCIPLES.md](INVIOLABLE-PRINCIPLES.md) #4 "never
  hardcode").
- **Suppress the logo entirely**: set `logoUrl: null` in the component
  definition. The card will render the letter-mark fallback
  (hue-derived from the component name).

## Current asset status

The 63 SVG files currently in `public/component-logos/` are **stylised
brand-color marks** authored in-house, not copies of the upstream
projects' official logo files. They preserve each project's brand colour
(taken from the project's documented brand pages or in-product palette)
and use a recognisable shape mark — not the trademarked logotype.

This avoids the licence ambiguity of vendoring third-party art into a
public repository while still producing a visually distinctive grid. The
table below records the canonical upstream logo source for each
component so a future pass can swap in the official asset where the
licence permits.

| Component slug      | Upstream project      | Canonical logo source                                                       | Notes |
|---------------------|-----------------------|-----------------------------------------------------------------------------|-------|
| flux                | Flux CD               | https://github.com/cncf/artwork/tree/main/projects/flux                     | CNCF graduated, Apache-2.0, brand-guidelines apply |
| crossplane          | Crossplane            | https://github.com/cncf/artwork/tree/main/projects/crossplane               | CNCF incubating |
| gitea               | Gitea                 | https://about.gitea.com/                                                     | MIT |
| opentofu            | OpenTofu              | https://opentofu.org/                                                        | Linux Foundation, MPL-2.0 |
| vcluster            | vCluster (Loft Labs)  | https://www.vcluster.com/brand                                               | Apache-2.0 |
| cilium              | Cilium                | https://github.com/cncf/artwork/tree/main/projects/cilium                    | CNCF graduated |
| coraza              | Coraza WAF            | https://coraza.io/                                                            | OWASP, Apache-2.0 |
| external-dns        | ExternalDNS           | https://github.com/kubernetes-sigs/external-dns                              | Kubernetes-sigs |
| envoy               | Envoy Proxy           | https://github.com/cncf/artwork/tree/main/projects/envoy                     | CNCF graduated |
| frpc                | frp / frpc            | https://github.com/fatedier/frp                                              | Apache-2.0 |
| netbird             | NetBird               | https://netbird.io/brand                                                     | BSD-3-Clause |
| strongswan          | strongSwan            | https://www.strongswan.org/                                                  | GPL-2.0 |
| vpa                 | Kubernetes VPA        | https://github.com/kubernetes/autoscaler                                     | Apache-2.0 — Kubernetes wheel mark |
| keda                | KEDA                  | https://github.com/cncf/artwork/tree/main/projects/keda                      | CNCF graduated |
| reloader            | Reloader (Stakater)   | https://github.com/stakater/Reloader                                         | Apache-2.0 |
| continuum           | Continuum (in-house)  | OpenOva platform-curated                                                     | No upstream — text-mark fallback |
| seaweedfs           | SeaweedFS             | https://github.com/seaweedfs/seaweedfs                                       | Apache-2.0 |
| velero              | Velero                | https://github.com/cncf/artwork/tree/main/projects/velero                    | CNCF |
| harbor              | Harbor                | https://github.com/cncf/artwork/tree/main/projects/harbor                    | CNCF graduated |
| falco               | Falco                 | https://github.com/cncf/artwork/tree/main/projects/falco                     | CNCF graduated |
| kyverno             | Kyverno               | https://github.com/cncf/artwork/tree/main/projects/kyverno                   | CNCF graduated |
| trivy               | Trivy (Aqua)          | https://github.com/aquasecurity/trivy                                        | Apache-2.0 |
| syft-grype          | Syft + Grype          | https://github.com/anchore/syft, https://github.com/anchore/grype            | Apache-2.0 (Anchore) |
| sigstore            | Sigstore              | https://www.sigstore.dev/                                                    | Linux Foundation |
| keycloak            | Keycloak              | https://www.keycloak.org/                                                    | CNCF, Apache-2.0 |
| openbao             | OpenBao (Vault fork)  | https://openbao.org/img/openbao-icon.svg                                     | MPL-2.0 — use OpenBao's mark, **NOT** HashiCorp Vault's |
| external-secrets    | External Secrets Op.  | https://github.com/external-secrets/external-secrets                         | Apache-2.0 |
| cert-manager        | cert-manager          | https://github.com/cncf/artwork/tree/main/projects/cert-manager              | CNCF graduated |
| grafana             | Grafana               | https://grafana.com/brand                                                    | AGPL-3.0; brand-guidelines apply |
| opentelemetry       | OpenTelemetry         | https://github.com/cncf/artwork/tree/main/projects/opentelemetry             | CNCF graduated |
| alloy               | Grafana Alloy         | https://grafana.com/brand                                                    | AGPL-3.0 |
| loki                | Grafana Loki          | https://grafana.com/brand                                                    | AGPL-3.0 |
| mimir               | Grafana Mimir         | https://grafana.com/brand                                                    | AGPL-3.0 |
| tempo               | Grafana Tempo         | https://grafana.com/brand                                                    | AGPL-3.0 |
| opensearch          | OpenSearch            | https://opensearch.org/brand-guidelines/                                     | Apache-2.0 |
| litmus              | LitmusChaos           | https://github.com/cncf/artwork/tree/main/projects/litmus                    | CNCF |
| openmeter           | OpenMeter             | https://openmeter.io/                                                        | Apache-2.0 |
| specter             | Specter (in-house)    | OpenOva platform-curated                                                     | No upstream — text-mark fallback |
| cnpg                | CloudNativePG         | https://cloudnative-pg.io/                                                   | Apache-2.0 |
| valkey              | Valkey                | https://valkey.io/                                                           | Linux Foundation, BSD-3 |
| strimzi             | Strimzi               | https://github.com/cncf/artwork/tree/main/projects/strimzi                   | CNCF |
| debezium            | Debezium              | https://debezium.io/                                                          | Apache-2.0 |
| flink               | Apache Flink          | https://flink.apache.org/                                                     | Apache-2.0 — Apache trademark policy applies |
| temporal            | Temporal              | https://temporal.io/brand                                                     | MIT |
| clickhouse          | ClickHouse            | https://clickhouse.com/                                                       | Apache-2.0 |
| ferretdb            | FerretDB              | https://www.ferretdb.io/                                                      | Apache-2.0 |
| iceberg             | Apache Iceberg        | https://iceberg.apache.org/                                                   | Apache-2.0 |
| superset            | Apache Superset       | https://superset.apache.org/                                                  | Apache-2.0 |
| kserve              | KServe                | https://github.com/cncf/artwork/tree/main/projects/kserve                    | CNCF |
| knative             | Knative               | https://github.com/cncf/artwork/tree/main/projects/knative                   | CNCF |
| axon                | Axon (OpenOva)        | OpenOva platform-curated                                                     | OpenOva product mark |
| neo4j               | Neo4j                 | https://neo4j.com/brand                                                       | GPL-3.0 (community) |
| vllm                | vLLM                  | https://github.com/vllm-project/vllm                                         | Apache-2.0 |
| milvus              | Milvus                | https://github.com/cncf/artwork/tree/main/projects/milvus                    | CNCF |
| bge                 | BGE (BAAI)            | BAAI / FlagEmbedding                                                          | MIT |
| langfuse            | Langfuse              | https://github.com/langfuse/langfuse                                         | MIT |
| librechat           | LibreChat             | https://github.com/danny-avila/LibreChat                                     | MIT |
| stalwart            | Stalwart              | https://stalw.art/                                                            | AGPL-3.0 |
| livekit             | LiveKit               | https://livekit.io/                                                           | Apache-2.0 |
| stunner             | STUNner               | https://github.com/l7mp/stunner                                               | MIT |
| matrix              | Matrix                | https://matrix.org/foundation/                                                | Apache-2.0 |
| ntfy                | ntfy                  | https://ntfy.sh/                                                              | Apache-2.0 |

## Replacement procedure

To replace an in-house mark with the upstream's official SVG:

1. Download the canonical asset from the source URL above.
2. Confirm licence permits redistribution in this public repo (most
   CNCF projects allow brand-asset use with attribution; some require
   permission for derivative works).
3. Convert to a square viewBox if not already; size to 64x64.
4. Save as `products/catalyst/bootstrap/ui/public/component-logos/<id>.svg`,
   overwriting the existing file.
5. Run `npm test` in `products/catalyst/bootstrap/ui/` to confirm tests
   still pass (logos are loaded by URL, so no test-fixture change is
   needed).
6. Commit under `area/platform`, mention the licence in the commit
   message.

## License note

OpenOva does not claim ownership of any third-party project's brand or
logo. The marks vendored under `public/component-logos/` are
**stylised brand-color silhouettes** generated for use in OpenOva's own
console UI. Each upstream project's name and brand-color reference
remains the property of its respective owner. When you ship the
Catalyst wizard to a customer, ensure the licence terms of any
canonical upstream logos you swap in still permit redistribution in
your build.

For OpenOva-curated components (Continuum, Specter, Axon) there is no
upstream — the marks are wholly OpenOva's.
