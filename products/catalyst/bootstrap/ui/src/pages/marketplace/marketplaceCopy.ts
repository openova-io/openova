/**
 * marketplaceCopy.ts — long-form marketing + reference copy for the public
 * marketplace surfaces (`/sovereign/marketplace/family/<id>` and
 * `/sovereign/marketplace/product/<id>`).
 *
 * The wizard card grid (StepComponents) renders short labels: card body
 * descriptions are deliberately one-line summaries that fit a 108px tile.
 * The marketplace pages reached by clicking a chip or a card body need
 * full-fat copy: a positioning paragraph, the feature surface, the
 * integration story, and a link to the upstream project. Keeping that copy
 * here (and reading it from a single per-id table) keeps componentGroups.ts
 * lean while letting product/marketing iterate without touching the catalog.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode") this module is
 * the single source of truth for marketplace surface copy and external
 * project URLs. Routes, templates, and tests all read from these maps.
 */

/** Per-family long-form portfolio narrative for the family page. */
export interface FamilyCopy {
  /** Marketing-grade headline shown above the family name. */
  tagline: string
  /** Multi-paragraph overview that introduces the family. */
  overview: string[]
  /** Outcome bullets — what operators get when they install this family. */
  capabilities: string[]
  /** Brand colour for the family chip (background tint + foreground). */
  chip: { bg: string; fg: string; border: string }
}

export const FAMILY_COPY: Record<string, FamilyCopy> = {
  pilot: {
    tagline: 'Continuous delivery, infrastructure as code, virtual cluster isolation',
    overview: [
      'PILOT is the delivery plane of every Sovereign. It turns a Git repository ' +
        'into the only mutable system of record — every change to platform configuration, ' +
        'every Blueprint upgrade, every tenant onboarding flows through a pull request, ' +
        'is reconciled by Flux, and is observable as code.',
      'OpenTofu and Crossplane provision the underlying cloud and Kubernetes ' +
        'primitives, while vCluster carves the cluster into isolated tenant spaces. ' +
        'Gitea hosts the operator-controlled repositories so the Sovereign keeps ' +
        'delivering even when the public internet is unavailable.',
    ],
    capabilities: [
      'GitOps reconciliation from operator-controlled repositories',
      'Crossplane Compositions for cloud and Kubernetes day-2 operations',
      'OpenTofu modules for Phase 0 infrastructure provisioning',
      'vCluster tenant isolation without per-tenant control planes',
      'Self-hosted Gitea for sovereign source-of-truth',
    ],
    chip: { bg: 'rgba(56,189,248,0.16)', fg: '#38BDF8', border: 'rgba(56,189,248,0.35)' },
  },
  spine: {
    tagline: 'Networking, service mesh, DNS, and encrypted connectivity',
    overview: [
      'SPINE is the connective tissue between every workload, every Sovereign, ' +
        'and the operators who run them. Cilium provides eBPF-accelerated CNI and ' +
        'service-mesh primitives; Envoy serves L7 traffic; Coraza filters it.',
      'Authoritative DNS is owned by the Sovereign through PowerDNS — DNSSEC, ' +
        'lua-records, and geographic failover are first-class. NetBird and strongSwan ' +
        'extend that perimeter to private networks and partner sites.',
    ],
    capabilities: [
      'eBPF CNI with native service mesh and L4/L7 policy',
      'Authoritative DNS with DNSSEC and geo-aware lua-records',
      'L7 ingress with WAF protection and external-DNS automation',
      'Mesh VPN and IPsec for workforce and site-to-site connectivity',
      'Reverse-tunnel exposure for hybrid edge deployments',
    ],
    chip: { bg: 'rgba(167,139,250,0.16)', fg: '#A78BFA', border: 'rgba(167,139,250,0.35)' },
  },
  surge: {
    tagline: 'Scaling, resilience, and configuration-aware orchestration',
    overview: [
      'SURGE is the elasticity layer. KEDA and VPA size workloads to demand; ' +
        'Reloader keeps them honest about configuration drift; Continuum ' +
        'orchestrates HA across availability zones.',
      'Operators get a Sovereign that reacts to load — both up and down — without ' +
        'manual intervention, and that recovers from individual node loss without ' +
        'paging anyone.',
    ],
    capabilities: [
      'Vertical pod autoscaling with right-sizing recommendations',
      'Event-driven horizontal autoscaling for queues, streams, and metrics',
      'Configuration-change rollout via watchers on Secrets and ConfigMaps',
      'High-availability orchestration across regions and zones',
    ],
    chip: { bg: 'rgba(245,158,11,0.16)', fg: '#F59E0B', border: 'rgba(245,158,11,0.35)' },
  },
  silo: {
    tagline: 'Multi-protocol storage, backup and disaster recovery, container registry',
    overview: [
      'SILO turns a Sovereign into its own storage substrate. SeaweedFS ' +
        'serves the same dataset over S3, NFS, FUSE, and HDFS — every workload ' +
        'reads and writes against a single, encrypted, replicated pool.',
      'Velero captures namespace-scoped and cluster-scoped state on a schedule ' +
        'and restores it elsewhere. Harbor provides a private OCI registry ' +
        'with image signing, vulnerability scanning, and replication.',
    ],
    capabilities: [
      'Distributed storage exposed as S3, NFS, FUSE, and HDFS',
      'Scheduled backups and cross-cluster restore with Velero',
      'Private OCI registry with replication and content trust',
      'Erasure coding and per-collection encryption at rest',
    ],
    chip: { bg: 'rgba(74,222,128,0.16)', fg: '#4ADE80', border: 'rgba(74,222,128,0.35)' },
  },
  guardian: {
    tagline: 'Policy, identity, secrets, certificates, and supply-chain trust',
    overview: [
      'GUARDIAN is the security plane. Kyverno enforces policy as code; ' +
        'OpenBao stores secrets behind independent Raft quorum; External Secrets ' +
        'syncs them into workloads without leaving the cluster.',
      'Cert-Manager automates X.509 issuance through ACME and internal CAs; ' +
        'Falco surfaces runtime threats; Trivy, Syft, Grype, and Sigstore close ' +
        'the supply-chain loop from build through deploy.',
    ],
    capabilities: [
      'Admission policy and mutation via Kyverno',
      'Secrets vault with sovereign Raft quorum (OpenBao)',
      'Automated X.509 issuance and rotation',
      'Runtime threat detection, SBOM analysis, and signature verification',
      'OIDC/SAML identity federation through Keycloak',
    ],
    chip: { bg: 'rgba(248,113,113,0.16)', fg: '#F87171', border: 'rgba(248,113,113,0.35)' },
  },
  insights: {
    tagline: 'Telemetry, dashboards, and AI-powered operations',
    overview: [
      'INSIGHTS unifies metrics, logs, and traces in a single observability ' +
        'pipeline. OpenTelemetry collects, Alloy ships, Mimir, Loki, and Tempo ' +
        'store, and Grafana visualises.',
      'Specter closes the loop — anomaly detection and correlation over the same ' +
        'telemetry, drawing on the CORTEX inference stack to surface incidents ' +
        'before pagers fire.',
    ],
    capabilities: [
      'Unified metrics, logs, and traces over OpenTelemetry',
      'Long-term storage on the Sovereign’s own SeaweedFS pool',
      'Curated Grafana dashboards for every Catalyst component',
      'AIOps anomaly detection and root-cause correlation',
      'Chaos engineering via Litmus and usage metering via OpenMeter',
    ],
    chip: { bg: 'rgba(56,189,248,0.16)', fg: '#38BDF8', border: 'rgba(56,189,248,0.35)' },
  },
  fabric: {
    tagline: 'Event streaming, change data capture, workflow orchestration, and analytics',
    overview: [
      'FABRIC is the data substrate every higher-order workload composes against. ' +
        'CloudNative PG and Valkey provide the relational and key-value primitives; ' +
        'Strimzi brings managed Kafka; Debezium streams changes from databases into ' +
        'topics in real time.',
      'Flink, ClickHouse, Iceberg, and Superset round out the analytical surface so ' +
        'operators can blend transactional and analytical workloads on the same ' +
        'Sovereign without exporting data.',
    ],
    capabilities: [
      'Operated PostgreSQL with point-in-time recovery (CloudNative PG)',
      'Operated Kafka and Redis-compatible cache',
      'Change-data-capture from PostgreSQL into Kafka topics',
      'Stream processing, lakehouse storage, and OLAP analytics',
      'Workflow orchestration with Temporal and BI with Superset',
    ],
    chip: { bg: 'rgba(99,102,241,0.16)', fg: '#818CF8', border: 'rgba(99,102,241,0.35)' },
  },
  cortex: {
    tagline: 'Model serving, LLM inference, vector search, embeddings, and observability',
    overview: [
      'CORTEX is the AI plane. KServe and vLLM serve models; Knative scales them ' +
        'to zero between requests; Milvus stores vectors backed by SeaweedFS; ' +
        'BGE produces embeddings.',
      'LangFuse traces every prompt, completion, tool call, and cost across the ' +
        'stack. Axon brokers gateway access to upstream LLM providers, and ' +
        'LibreChat ships an end-user interface ready for tenant onboarding.',
    ],
    capabilities: [
      'Inference serving with KServe and vLLM',
      'Vector search at petabyte scale on the Sovereign’s own storage',
      'Embedding generation and retrieval-augmented generation pipelines',
      'End-to-end LLM observability with cost, latency, and lineage',
      'Tenant-facing chat interface with role-based access control',
    ],
    chip: { bg: 'rgba(244,114,182,0.16)', fg: '#F472B6', border: 'rgba(244,114,182,0.35)' },
  },
  relay: {
    tagline: 'Self-hosted email, video, federated messaging, and push notifications',
    overview: [
      'RELAY is the communication plane. Stalwart hosts SMTP, IMAP, and JMAP for ' +
        'sovereign email; LiveKit and STUNner power real-time video and audio ' +
        'with WebRTC; Matrix federates messaging across organisations.',
      'Ntfy distributes push notifications without third-party SDKs. Operators ' +
        'replace consumer-grade SaaS with self-hosted equivalents that share ' +
        'the same identity, telemetry, and storage as every other Sovereign workload.',
    ],
    capabilities: [
      'SMTP, IMAP, and JMAP mail with shared mailboxes and send-as',
      'WebRTC media routing with TURN/STUN for restrictive networks',
      'Federated messaging on the Matrix protocol',
      'Push notifications without third-party SDKs',
    ],
    chip: { bg: 'rgba(34,211,238,0.16)', fg: '#22D3EE', border: 'rgba(34,211,238,0.35)' },
  },
}

/** Default chip palette used when a family id has no explicit FamilyCopy. */
export const DEFAULT_FAMILY_CHIP = {
  bg: 'rgba(148,163,184,0.16)',
  fg: '#94A3B8',
  border: 'rgba(148,163,184,0.35)',
}

/** Resolve the chip palette for a family id, falling back when unknown. */
export function familyChipPalette(familyId: string): { bg: string; fg: string; border: string } {
  return FAMILY_COPY[familyId]?.chip ?? DEFAULT_FAMILY_CHIP
}

/**
 * Per-component long-form copy used by the product detail page. The
 * positioning paragraph is the lead, integration covers how the component
 * fits in with its family and dependencies, and upstream is the canonical
 * project URL the user can follow for source and documentation.
 */
export interface ComponentCopy {
  /** Hero paragraph — what this component is and why it ships in Catalyst. */
  positioning: string
  /** Integration narrative — neighbours, dependencies, and operator surface. */
  integration: string
  /** Outcome / feature bullets surfaced as a list. */
  highlights: string[]
  /** Canonical project URL (vendor or community). */
  upstreamUrl: string
  /** Display label for the upstream link. */
  upstreamLabel: string
}

export const COMPONENT_COPY: Record<string, ComponentCopy> = {
  /* ── PILOT ─────────────────────────────────────────────────────── */
  flux: {
    positioning:
      'Flux is the GitOps reconciler that drives every Sovereign. It watches operator-controlled ' +
      'Git repositories, decodes Kustomize and Helm sources, and applies them to the cluster on a ' +
      'one-minute reconciliation cadence.',
    integration:
      'Flux is the only path through which platform manifests reach the cluster. Crossplane ' +
      'Compositions, Blueprint OCI artifacts, and tenant overlays are all delivered through Flux ' +
      'GitRepository and OCIRepository sources.',
    highlights: [
      'GitRepository and OCIRepository sources reconciled every minute',
      'Kustomize and Helm controllers with drift detection and pruning',
      'Notification controller wired to Sovereign telemetry',
    ],
    upstreamUrl: 'https://fluxcd.io',
    upstreamLabel: 'fluxcd.io',
  },
  crossplane: {
    positioning:
      'Crossplane is the day-2 IaC engine. Compositions expose cloud and Kubernetes APIs as native ' +
      'CRDs, so operators reconcile a managed Postgres, an S3 bucket, or a DNS zone the same way ' +
      'they reconcile a Deployment.',
    integration:
      'Catalyst ships a curated Composition library covering every supported provider (Hetzner, ' +
      'Contabo, Linode, Hostkey, AWS) plus the in-cluster providers (cert-manager, external-dns, ' +
      'helm). Flux delivers the Compositions; Crossplane reconciles them.',
    highlights: [
      'Composite resources for cloud machines, storage, networking, and DNS',
      'Provider-helm and provider-kubernetes for in-cluster reconciliation',
      'CompositionRevisions for safe, versioned upgrades',
    ],
    upstreamUrl: 'https://crossplane.io',
    upstreamLabel: 'crossplane.io',
  },
  gitea: {
    positioning:
      'Gitea is the Sovereign-local Git server that holds the operator-controlled source of truth. ' +
      'Catalyst pre-creates five organisation tenants per Sovereign, each with its own visibility ' +
      'and policy boundary.',
    integration:
      'Gitea backs onto CloudNative PG for metadata and SeaweedFS for LFS objects. Flux pulls ' +
      'from Gitea over service-internal HTTPS, so the Sovereign keeps reconciling even when the ' +
      'public internet is unavailable.',
    highlights: [
      'Five pre-provisioned organisations per Sovereign',
      'LFS-backed storage on the Sovereign’s SeaweedFS pool',
      'OIDC integration with Keycloak for SSO',
    ],
    upstreamUrl: 'https://gitea.io',
    upstreamLabel: 'gitea.io',
  },
  opentofu: {
    positioning:
      'OpenTofu provisions Phase 0 — the cloud machines, networks, and DNS records that exist ' +
      'before the Kubernetes API is reachable. Once the cluster is up, every subsequent change ' +
      'runs through Crossplane.',
    integration:
      'Catalyst ships per-provider OpenTofu modules and a shared module library. The Phase 0 ' +
      'pipeline runs once per Sovereign and then hands the steady state over to Crossplane and Flux.',
    highlights: [
      'Provider-specific modules for every supported cloud',
      'Stateless invocation from the Catalyst CI pipeline',
      'Hand-off contract documented in PROVISIONING-PLAN.md',
    ],
    upstreamUrl: 'https://opentofu.org',
    upstreamLabel: 'opentofu.org',
  },
  vcluster: {
    positioning:
      'vCluster carves a host Kubernetes cluster into isolated virtual clusters. Each tenant gets ' +
      'its own kube-apiserver, controller-manager, and scheduler, while sharing the host’s ' +
      'compute and network underlay.',
    integration:
      'Catalyst exposes vCluster instances as a tenant-onboarding primitive. Tenants get a real ' +
      'kubeconfig, namespace-scoped policy, and storage carved from SeaweedFS pools, all without ' +
      'standing up a separate physical cluster.',
    highlights: [
      'Per-tenant control planes on shared host nodes',
      'Bidirectional service exposure to host workloads',
      'Resource quotas and storage carved from the host’s pools',
    ],
    upstreamUrl: 'https://www.vcluster.com',
    upstreamLabel: 'vcluster.com',
  },

  /* ── SPINE ─────────────────────────────────────────────────────── */
  cilium: {
    positioning:
      'Cilium is the Sovereign’s eBPF-powered CNI and service mesh. It enforces network policy ' +
      'in the kernel, terminates encrypted overlays, and exposes L4/L7 telemetry to the observability ' +
      'stack.',
    integration:
      'Cilium runs as a DaemonSet on every node and configures cluster-mesh between Sovereigns when ' +
      'multi-region topologies opt in. Hubble feeds INSIGHTS with flow telemetry.',
    highlights: [
      'eBPF datapath with kube-proxy replacement',
      'L7 policy and Hubble flow visibility',
      'Cluster-mesh for multi-Sovereign workload topologies',
    ],
    upstreamUrl: 'https://cilium.io',
    upstreamLabel: 'cilium.io',
  },
  coraza: {
    positioning:
      'Coraza is the Sovereign’s L7 web application firewall. It filters request traffic against ' +
      'the OWASP Core Rule Set before it reaches workloads, with rule sets the operator can extend ' +
      'or override per-domain.',
    integration:
      'Coraza runs as an Envoy filter chain in front of every public ingress. Rule decisions and ' +
      'audit logs feed into the observability stack for incident response.',
    highlights: [
      'OWASP Core Rule Set baseline with per-domain overrides',
      'Envoy filter integration with structured audit logging',
      'No external WAF dependency for sovereign deployments',
    ],
    upstreamUrl: 'https://coraza.io',
    upstreamLabel: 'coraza.io',
  },
  powerdns: {
    positioning:
      'PowerDNS is the authoritative DNS server for every Sovereign zone. DNSSEC, lua-records, and ' +
      'geographic failover are first-class — there is no upstream DNS provider in the loop.',
    integration:
      'External-DNS publishes records into PowerDNS; the public web reads them through the public ' +
      'authoritative endpoint. Multi-region failover patterns (ifurlup, pickclosest, ifportup) are ' +
      'documented in MULTI-REGION-DNS.md.',
    highlights: [
      'Authoritative DNS with DNSSEC signing on the Sovereign',
      'lua-records for health-checked geographic failover',
      'API-driven record management via External-DNS',
    ],
    upstreamUrl: 'https://www.powerdns.com',
    upstreamLabel: 'powerdns.com',
  },
  'external-dns': {
    positioning:
      'External-DNS reconciles Kubernetes Service, Ingress, and Gateway objects into authoritative ' +
      'DNS records. It is the bridge between the cluster’s service discovery and the public ' +
      'DNS plane.',
    integration:
      'External-DNS is configured per Sovereign to write into PowerDNS. Its reconciliation cadence ' +
      'and ownership labels keep records in sync with cluster state.',
    highlights: [
      'Automatic record reconciliation for Service, Ingress, and Gateway',
      'PowerDNS provider with TSIG-authenticated updates',
      'Ownership labels prevent cross-cluster record conflicts',
    ],
    upstreamUrl: 'https://kubernetes-sigs.github.io/external-dns/',
    upstreamLabel: 'kubernetes-sigs/external-dns',
  },
  envoy: {
    positioning:
      'Envoy is the Sovereign’s programmable L7 proxy. It terminates TLS, routes HTTP and gRPC, ' +
      'and runs the WAF filter chain that protects every public ingress.',
    integration:
      'Envoy is deployed as a DaemonSet at the edge and as a sidecar inside the service mesh. xDS ' +
      'configuration is delivered through the Cilium control plane and the Catalyst gateway operator.',
    highlights: [
      'L7 routing, TLS termination, and gRPC support',
      'xDS-driven configuration with hot-reload',
      'Filter chain integration with Coraza WAF',
    ],
    upstreamUrl: 'https://www.envoyproxy.io',
    upstreamLabel: 'envoyproxy.io',
  },
  frpc: {
    positioning:
      'frpc is the reverse-tunnel client that lets edge Sovereigns expose services through a ' +
      'central frps endpoint. It is the first-choice option when the edge runs behind NAT or a ' +
      'restrictive firewall.',
    integration:
      'frpc connects out from each Sovereign to a Catalyst-managed frps gateway, terminating TLS ' +
      'inside the Sovereign and forwarding through the tunnel for inbound traffic.',
    highlights: [
      'Outbound-only tunnels for NATed Sovereigns',
      'Multi-protocol forwarding (TCP, UDP, HTTP, HTTPS)',
      'Authenticated, multiplexed control channels',
    ],
    upstreamUrl: 'https://github.com/fatedier/frp',
    upstreamLabel: 'github.com/fatedier/frp',
  },
  netbird: {
    positioning:
      'NetBird is the mesh VPN that connects operators, edge sites, and Sovereigns. It replaces ' +
      'point-to-point VPN configuration with a coordinated overlay derived from identity.',
    integration:
      'NetBird issues identity-bound peer credentials integrated with Keycloak and routes traffic ' +
      'through WireGuard tunnels with policy enforced centrally.',
    highlights: [
      'Identity-bound peer credentials via OIDC',
      'WireGuard datapath with policy enforced at the controller',
      'NAT traversal without third-party relay services',
    ],
    upstreamUrl: 'https://netbird.io',
    upstreamLabel: 'netbird.io',
  },
  strongswan: {
    positioning:
      'strongSwan provides standards-compliant IPsec for site-to-site connectivity. It is the bridge ' +
      'into legacy partner networks that mandate IPsec rather than overlay VPNs.',
    integration:
      'strongSwan tunnels are provisioned through Crossplane Compositions and reconciled into the ' +
      'Sovereign’s routing tables alongside Cilium’s overlay.',
    highlights: [
      'IKEv1/IKEv2 with full RFC compliance',
      'PKI-driven authentication for partner peers',
      'Composable with Cilium mesh routing',
    ],
    upstreamUrl: 'https://www.strongswan.org',
    upstreamLabel: 'strongswan.org',
  },

  /* ── SURGE ─────────────────────────────────────────────────────── */
  vpa: {
    positioning:
      'VPA is the Sovereign’s vertical pod autoscaler. It observes resource usage and recommends ' +
      'or applies right-sized requests so workloads neither hoard nor starve.',
    integration:
      'VPA runs in recommendation mode for stateful workloads and updater mode for stateless ones. ' +
      'Recommendations feed dashboards and alert rules in the observability stack.',
    highlights: [
      'Right-sizing recommendations from real usage data',
      'Updater mode for stateless workloads',
      'Per-workload mode opt-in for safe rollout',
    ],
    upstreamUrl: 'https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler',
    upstreamLabel: 'kubernetes/autoscaler',
  },
  keda: {
    positioning:
      'KEDA is the event-driven horizontal autoscaler. It scales workloads on signals from queues, ' +
      'streams, databases, and external metric sources — not just CPU and memory.',
    integration:
      'KEDA ScaledObjects are delivered through Flux and reconciled against the underlying ' +
      'workload. Scalers cover Kafka, Redis, Prometheus, NATS, and more.',
    highlights: [
      'Scaling triggers across 60+ event sources',
      'Scale-to-zero for idle workloads',
      'External metric server for HPA compatibility',
    ],
    upstreamUrl: 'https://keda.sh',
    upstreamLabel: 'keda.sh',
  },
  reloader: {
    positioning:
      'Reloader watches Secrets and ConfigMaps for changes and triggers rollouts of the workloads ' +
      'that mount them. It is the safety net for configuration changes that don’t emit pod ' +
      'restart events on their own.',
    integration:
      'Reloader is annotation-driven — workloads opt in by tagging their pod template with the ' +
      'configmaps and secrets they care about.',
    highlights: [
      'Annotation-driven rollout on configuration change',
      'Supports Deployment, StatefulSet, DaemonSet, and Argo Rollouts',
      'Selectable rollout strategies per workload',
    ],
    upstreamUrl: 'https://github.com/stakater/Reloader',
    upstreamLabel: 'stakater/Reloader',
  },
  continuum: {
    positioning:
      'Continuum is the high-availability orchestrator for stateful services. It coordinates ' +
      'failover across availability zones and regions for the workloads that need explicit ' +
      'arbitration.',
    integration:
      'Continuum reads health from the same telemetry pipeline as INSIGHTS and writes failover ' +
      'decisions through Crossplane Compositions.',
    highlights: [
      'Cross-zone and cross-region failover orchestration',
      'Telemetry-driven health checks',
      'Crossplane integration for state changes',
    ],
    upstreamUrl: 'https://openova.io/catalyst/components/continuum',
    upstreamLabel: 'openova.io',
  },

  /* ── SILO ─────────────────────────────────────────────────────── */
  seaweedfs: {
    positioning:
      'SeaweedFS is the Sovereign’s distributed storage layer. It exposes the same dataset over ' +
      'S3, NFS, FUSE, and HDFS, with erasure coding and per-collection encryption.',
    integration:
      'Every other Catalyst component that needs storage reads or writes against SeaweedFS — ' +
      'Harbor, Velero, Loki, Mimir, Tempo, Grafana, Iceberg, and Milvus.',
    highlights: [
      'S3, NFS, FUSE, and HDFS over a single replicated pool',
      'Erasure coding with per-collection policy',
      'Encryption at rest with operator-controlled keys',
    ],
    upstreamUrl: 'https://github.com/seaweedfs/seaweedfs',
    upstreamLabel: 'seaweedfs/seaweedfs',
  },
  velero: {
    positioning:
      'Velero captures cluster state — namespace contents, persistent volumes, and CRDs — ' +
      'on a schedule and restores them elsewhere. It is the backup and disaster-recovery primitive ' +
      'for every Sovereign.',
    integration:
      'Velero writes to the Sovereign’s SeaweedFS pool over the S3 API. Restores can target a ' +
      'different namespace, cluster, or Sovereign for cross-region recovery drills.',
    highlights: [
      'Scheduled namespace and cluster backups',
      'Cross-cluster restore with mapping rules',
      'CSI snapshot integration for volume-level capture',
    ],
    upstreamUrl: 'https://velero.io',
    upstreamLabel: 'velero.io',
  },
  harbor: {
    positioning:
      'Harbor is the Sovereign’s private OCI registry. It stores container images, Helm charts, ' +
      'and Blueprint artifacts, with content trust and vulnerability scanning built in.',
    integration:
      'Harbor backs onto CloudNative PG for metadata, SeaweedFS for blob storage, and Valkey for ' +
      'job queues. Trivy provides scanning; Sigstore provides cosign-based signing.',
    highlights: [
      'OCI artifact registry with replication',
      'Cosign-based content trust integrated with Sigstore',
      'Vulnerability scanning powered by Trivy',
    ],
    upstreamUrl: 'https://goharbor.io',
    upstreamLabel: 'goharbor.io',
  },

  /* ── GUARDIAN ──────────────────────────────────────────────────── */
  falco: {
    positioning:
      'Falco is the runtime threat detection engine. It instruments the kernel through eBPF and ' +
      'flags policy-violating syscalls in real time.',
    integration:
      'Falco events are forwarded into the INSIGHTS pipeline and tagged for security incident ' +
      'response. Rule sets are operator-controllable through Flux.',
    highlights: [
      'eBPF-based kernel instrumentation',
      'Built-in rule library for container threats',
      'Pluggable outputs into telemetry and alerting',
    ],
    upstreamUrl: 'https://falco.org',
    upstreamLabel: 'falco.org',
  },
  kyverno: {
    positioning:
      'Kyverno is the policy engine that gates every admission and mutation request to the ' +
      'Sovereign API. Policies are expressed as native Kubernetes resources — no DSL, no ' +
      'separate language to learn.',
    integration:
      'Kyverno policies ship as part of the platform Blueprint set and are versioned alongside ' +
      'every other manifest. Operators extend the catalog through Flux-delivered ClusterPolicy ' +
      'resources.',
    highlights: [
      'Validating, mutating, and generating policies',
      'Native YAML — no separate DSL',
      'Background scans for non-compliant existing resources',
    ],
    upstreamUrl: 'https://kyverno.io',
    upstreamLabel: 'kyverno.io',
  },
  trivy: {
    positioning:
      'Trivy scans container images, infrastructure-as-code, and language dependencies for known ' +
      'vulnerabilities. It is the supply-chain visibility layer that runs both in CI and at ' +
      'admission time.',
    integration:
      'Trivy is wired into Harbor for registry-side scans and into the build pipeline for image ' +
      'gating. Findings flow into the security feed in INSIGHTS.',
    highlights: [
      'Image, IaC, and dependency scanning',
      'Native Harbor integration',
      'CIS benchmarks and misconfiguration detection',
    ],
    upstreamUrl: 'https://trivy.dev',
    upstreamLabel: 'trivy.dev',
  },
  'syft-grype': {
    positioning:
      'Syft generates SBOMs from container images and source trees; Grype analyses them for known ' +
      'vulnerabilities. Together they close the supply-chain inventory loop.',
    integration:
      'Syft runs in the CI pipeline and stores SBOM artifacts in the Sovereign’s SeaweedFS pool. ' +
      'Grype consumes those SBOMs in scheduled scans against the Anchore vulnerability database.',
    highlights: [
      'SBOM generation in SPDX and CycloneDX formats',
      'Continuous CVE matching against new advisories',
      'Diff reporting between artifact versions',
    ],
    upstreamUrl: 'https://github.com/anchore/syft',
    upstreamLabel: 'anchore/syft',
  },
  sigstore: {
    positioning:
      'Sigstore is the keyless signing and verification stack for container images and other ' +
      'artifacts. It uses short-lived OIDC-issued certificates so signing keys never leave the ' +
      'identity boundary.',
    integration:
      'Cosign signs every Catalyst-built image at CI time; Kyverno verifies signatures at admission. ' +
      'Rekor provides the transparency log; Fulcio issues the certificates.',
    highlights: [
      'Keyless signing with OIDC-bound certificates',
      'Transparent audit log of every signature',
      'Kyverno integration for admission-time verification',
    ],
    upstreamUrl: 'https://www.sigstore.dev',
    upstreamLabel: 'sigstore.dev',
  },
  keycloak: {
    positioning:
      'Keycloak is the identity provider for every Sovereign. It federates external IdPs over ' +
      'OIDC and SAML and issues tokens to operators, tenants, and workloads.',
    integration:
      'Keycloak backs onto CloudNative PG and exposes itself behind Envoy. Catalyst pre-configures ' +
      'realms for the platform, the operator console, and tenant onboarding.',
    highlights: [
      'OIDC and SAML identity federation',
      'Realms isolated per tenant or business unit',
      'Token customisation for downstream authorization',
    ],
    upstreamUrl: 'https://www.keycloak.org',
    upstreamLabel: 'keycloak.org',
  },
  openbao: {
    positioning:
      'OpenBao is the secrets vault. It runs on its own Raft quorum so the Sovereign’s secrets ' +
      'plane is independent of the cluster’s etcd — a hard requirement for sovereign ' +
      'deployments.',
    integration:
      'External Secrets reads from OpenBao and writes Kubernetes Secret objects on a polling ' +
      'cadence. Operators interact through the OpenBao API and CLI; cluster workloads see only ' +
      'the synced secrets.',
    highlights: [
      'Independent Raft quorum (not backed by etcd)',
      'Dynamic secrets for databases and cloud providers',
      'Audit logging of every secret access',
    ],
    upstreamUrl: 'https://openbao.org',
    upstreamLabel: 'openbao.org',
  },
  'external-secrets': {
    positioning:
      'External Secrets Operator (ESO) bridges OpenBao and Kubernetes Secret objects. Workloads ' +
      'consume secrets the standard way; the synchronisation, rotation, and audit live in OpenBao.',
    integration:
      'ESO reconciles ExternalSecret resources delivered through Flux. Polling cadence and ' +
      'rotation policy are operator-controllable per secret class.',
    highlights: [
      'OpenBao SecretStore provider',
      'Configurable polling and rotation',
      'Per-namespace ClusterSecretStore scoping',
    ],
    upstreamUrl: 'https://external-secrets.io',
    upstreamLabel: 'external-secrets.io',
  },
  'cert-manager': {
    positioning:
      'cert-manager automates X.509 issuance and rotation. It supports ACME (Let’s Encrypt, ' +
      'ZeroSSL), private CAs, and issuance through HashiCorp Vault and OpenBao.',
    integration:
      'Every public ingress in a Sovereign carries a cert-manager-issued certificate. Renewal is ' +
      'fully automatic; rotations are non-disruptive.',
    highlights: [
      'ACME, internal CA, and Vault/OpenBao issuers',
      'Per-Ingress and per-Gateway certificate provisioning',
      'Automatic renewal with configurable lead time',
    ],
    upstreamUrl: 'https://cert-manager.io',
    upstreamLabel: 'cert-manager.io',
  },

  /* ── INSIGHTS ──────────────────────────────────────────────────── */
  grafana: {
    positioning:
      'Grafana is the observability surface for every Sovereign. Curated dashboards cover every ' +
      'Catalyst component out of the box; operators extend them through Flux-delivered ' +
      'ConfigMaps.',
    integration:
      'Grafana queries Mimir for metrics, Loki for logs, and Tempo for traces. SSO is wired ' +
      'through Keycloak; storage backs onto SeaweedFS.',
    highlights: [
      'Curated dashboards for every Catalyst component',
      'Unified query across metrics, logs, and traces',
      'Keycloak SSO and per-tenant org isolation',
    ],
    upstreamUrl: 'https://grafana.com/oss/grafana/',
    upstreamLabel: 'grafana.com',
  },
  opentelemetry: {
    positioning:
      'OpenTelemetry is the unified telemetry pipeline. Workloads emit traces, metrics, and logs ' +
      'through the OTel SDK; the Collector buffers, enriches, and routes them.',
    integration:
      'The Collector deployment runs as a gateway; agents run as DaemonSets. Telemetry is routed ' +
      'into Mimir, Loki, and Tempo by signal type.',
    highlights: [
      'Vendor-neutral SDKs for every supported language',
      'Gateway and agent topology with multi-pipeline routing',
      'Per-tenant tail-sampling for noisy services',
    ],
    upstreamUrl: 'https://opentelemetry.io',
    upstreamLabel: 'opentelemetry.io',
  },
  alloy: {
    positioning:
      'Alloy is the unified telemetry agent that ships logs, metrics, and traces from each node. ' +
      'It supersedes Promtail and Grafana Agent in a single binary.',
    integration:
      'Alloy runs as a DaemonSet, ships logs to Loki, metrics to Mimir, and traces to Tempo, with ' +
      'per-component pipelines that operators tune through Flux.',
    highlights: [
      'Unified DaemonSet for logs, metrics, and traces',
      'Per-pipeline tuning without redeploying the binary',
      'Native Prometheus, OTLP, and Loki forwarding',
    ],
    upstreamUrl: 'https://grafana.com/docs/alloy/',
    upstreamLabel: 'grafana.com/alloy',
  },
  loki: {
    positioning:
      'Loki is the log aggregation store. It indexes only labels — not log content — so ' +
      'storage cost scales with cardinality, not volume.',
    integration:
      'Loki backs onto the Sovereign’s SeaweedFS pool over the S3 API. Querying flows through ' +
      'Grafana with SSO via Keycloak.',
    highlights: [
      'Label-indexed log store with object-storage backend',
      'LogQL query language',
      'Multi-tenant isolation for shared deployments',
    ],
    upstreamUrl: 'https://grafana.com/oss/loki/',
    upstreamLabel: 'grafana.com/loki',
  },
  mimir: {
    positioning:
      'Mimir is the metrics store. It scales horizontally to billions of active series and provides ' +
      'the query backend for Grafana dashboards.',
    integration:
      'Mimir backs onto SeaweedFS for chunk storage. Recording rules and alert rules ship through ' +
      'Flux as native PrometheusRule resources.',
    highlights: [
      'Horizontal scaling to billions of active series',
      'Object-storage backend on the Sovereign’s SeaweedFS pool',
      'PromQL compatibility for existing dashboards and alerts',
    ],
    upstreamUrl: 'https://grafana.com/oss/mimir/',
    upstreamLabel: 'grafana.com/mimir',
  },
  tempo: {
    positioning:
      'Tempo is the distributed tracing backend. It stores spans in object storage and supports ' +
      'TraceQL for cross-trace analytics.',
    integration:
      'Tempo backs onto SeaweedFS. Service-graph generation feeds Grafana dashboards; metrics ' +
      'derived from spans flow into Mimir.',
    highlights: [
      'Object-storage tracing at petabyte scale',
      'TraceQL for cross-trace analytics',
      'Service-graph and span metrics generation',
    ],
    upstreamUrl: 'https://grafana.com/oss/tempo/',
    upstreamLabel: 'grafana.com/tempo',
  },
  opensearch: {
    positioning:
      'OpenSearch is the search and analytics engine. It backs full-text search for tenant-facing ' +
      'workloads and ad-hoc operational analytics.',
    integration:
      'OpenSearch runs as a stateful workload with its own storage class. Operators consume it ' +
      'directly through the REST API or through the OpenSearch Dashboards UI.',
    highlights: [
      'Full-text search and aggregation engine',
      'OpenSearch Dashboards for visualisation',
      'Vector and hybrid search for AI workloads',
    ],
    upstreamUrl: 'https://opensearch.org',
    upstreamLabel: 'opensearch.org',
  },
  litmus: {
    positioning:
      'Litmus is the chaos engineering platform. It runs experiments on a schedule against ' +
      'workloads the operator opts in, and reports results into the observability pipeline.',
    integration:
      'Litmus experiments are versioned in Git and reconciled through Flux. Findings feed into ' +
      'the security and reliability feeds in INSIGHTS.',
    highlights: [
      'Experiment library covering pod, node, and network failure modes',
      'Cron-driven scheduling',
      'Result reporting into the observability pipeline',
    ],
    upstreamUrl: 'https://litmuschaos.io',
    upstreamLabel: 'litmuschaos.io',
  },
  openmeter: {
    positioning:
      'OpenMeter is the usage metering engine for cloud-native and AI workloads. It records events, ' +
      'aggregates them on configurable windows, and exposes the totals to billing and analytics.',
    integration:
      'OpenMeter backs onto CloudNative PG and ClickHouse. Events arrive through the OpenMeter ' +
      'SDKs or by Kafka topic subscription.',
    highlights: [
      'High-throughput event ingestion',
      'Configurable aggregation windows',
      'Native Kafka and HTTP ingestion paths',
    ],
    upstreamUrl: 'https://openmeter.io',
    upstreamLabel: 'openmeter.io',
  },
  specter: {
    positioning:
      'Specter is the AIOps brain. It correlates metrics, logs, and traces with the embeddings and ' +
      'inference services in CORTEX to surface incidents before they manifest as outages.',
    integration:
      'Specter consumes the observability pipeline and uses the CORTEX stack (BGE for embeddings, ' +
      'Milvus for vector search, vLLM for inference, LangFuse for traces) to score anomalies and ' +
      'attribute root causes.',
    highlights: [
      'Anomaly detection across metrics, logs, and traces',
      'Root-cause correlation with vector retrieval',
      'Native integration with CORTEX inference and observability',
    ],
    upstreamUrl: 'https://openova.io/catalyst/components/specter',
    upstreamLabel: 'openova.io',
  },

  /* ── FABRIC ────────────────────────────────────────────────────── */
  cnpg: {
    positioning:
      'CloudNative PG is the operated PostgreSQL stack. It manages clusters with synchronous and ' +
      'asynchronous replicas, point-in-time recovery, and connection pooling out of the box.',
    integration:
      'Catalyst components that need a relational database (Gitea, Harbor, Keycloak, LangFuse, ' +
      'OpenMeter, Temporal, Superset, FerretDB, Matrix, LibreChat) read and write through ' +
      'CloudNative PG-managed clusters.',
    highlights: [
      'Operated PostgreSQL with synchronous and async replicas',
      'Point-in-time recovery to SeaweedFS',
      'Connection pooling with PgBouncer',
    ],
    upstreamUrl: 'https://cloudnative-pg.io',
    upstreamLabel: 'cloudnative-pg.io',
  },
  valkey: {
    positioning:
      'Valkey is the Redis-compatible in-memory data store that ships with Catalyst. Workloads ' +
      'that previously depended on Redis run unmodified.',
    integration:
      'Harbor and other Catalyst components use Valkey for queues and cache. Operators provision ' +
      'instances through Crossplane Compositions.',
    highlights: [
      'Drop-in Redis API compatibility',
      'Operated deployment with persistence and replication',
      'Composition-driven provisioning',
    ],
    upstreamUrl: 'https://valkey.io',
    upstreamLabel: 'valkey.io',
  },
  strimzi: {
    positioning:
      'Strimzi is the Kafka operator. It manages Kafka, Kafka Connect, MirrorMaker, and Cruise ' +
      'Control as Kubernetes-native resources.',
    integration:
      'Strimzi clusters serve every Catalyst event-streaming workload. Schemas are managed ' +
      'separately; Debezium is the canonical CDC source.',
    highlights: [
      'Operated Kafka with TLS and SCRAM authentication',
      'Cruise Control for partition rebalancing',
      'Kafka Connect for streaming integrations',
    ],
    upstreamUrl: 'https://strimzi.io',
    upstreamLabel: 'strimzi.io',
  },
  debezium: {
    positioning:
      'Debezium is the change-data-capture platform. It streams row-level changes from PostgreSQL ' +
      'and other databases into Kafka topics in real time.',
    integration:
      'Debezium runs as Kafka Connect connectors on top of the Strimzi cluster. Operators define ' +
      'capture scope and topic naming through Flux-delivered KafkaConnector resources.',
    highlights: [
      'Row-level CDC with at-least-once delivery',
      'Built-in connectors for PostgreSQL, MySQL, MongoDB, and more',
      'Schema registry integration',
    ],
    upstreamUrl: 'https://debezium.io',
    upstreamLabel: 'debezium.io',
  },
  flink: {
    positioning:
      'Apache Flink is the stream-processing engine. It runs continuous SQL and Java/Python jobs ' +
      'with exactly-once semantics over Kafka and other event sources.',
    integration:
      'Flink is deployed via the Flink Kubernetes Operator. Jobs are versioned in Git and ' +
      'reconciled through Flux as FlinkDeployment resources.',
    highlights: [
      'Exactly-once stream processing',
      'Continuous SQL and Java/Python APIs',
      'Native Kubernetes operator',
    ],
    upstreamUrl: 'https://flink.apache.org',
    upstreamLabel: 'flink.apache.org',
  },
  temporal: {
    positioning:
      'Temporal is the workflow orchestration engine. It runs durable, code-defined workflows that ' +
      'survive process restarts and can span hours, days, or weeks.',
    integration:
      'Temporal backs onto CloudNative PG. Worker pools are deployed per business capability and ' +
      'consume tasks from the same Temporal cluster.',
    highlights: [
      'Durable execution with deterministic replay',
      'Polyglot SDKs (Go, Java, Python, TypeScript)',
      'Visibility through the Temporal Web UI',
    ],
    upstreamUrl: 'https://temporal.io',
    upstreamLabel: 'temporal.io',
  },
  clickhouse: {
    positioning:
      'ClickHouse is the columnar analytics database. It serves OLAP queries over billions of ' +
      'rows with sub-second latency.',
    integration:
      'ClickHouse hosts the analytical layer for OpenMeter, security events, and operator-defined ' +
      'data marts. Backups land on SeaweedFS.',
    highlights: [
      'Columnar storage with vectorised execution',
      'Distributed tables for horizontal scale',
      'Native Kafka engine for streaming ingestion',
    ],
    upstreamUrl: 'https://clickhouse.com',
    upstreamLabel: 'clickhouse.com',
  },
  ferretdb: {
    positioning:
      'FerretDB is a MongoDB-compatible database that uses PostgreSQL as the storage engine. ' +
      'Workloads written for MongoDB run unmodified against the Sovereign’s relational core.',
    integration:
      'FerretDB connects to a CloudNative PG cluster. Existing MongoDB drivers and tooling work ' +
      'against the FerretDB endpoint without code changes.',
    highlights: [
      'MongoDB wire protocol compatibility',
      'PostgreSQL-backed storage',
      'Operator-controlled provisioning',
    ],
    upstreamUrl: 'https://www.ferretdb.com',
    upstreamLabel: 'ferretdb.com',
  },
  iceberg: {
    positioning:
      'Apache Iceberg is the table format for data lakehouses. It provides ACID transactions, time ' +
      'travel, and schema evolution over object storage.',
    integration:
      'Iceberg tables live in SeaweedFS. Trino, Flink, and Spark consume them through the Iceberg ' +
      'REST catalog.',
    highlights: [
      'ACID transactions over object storage',
      'Time travel and schema evolution',
      'Multi-engine catalog (Trino, Flink, Spark)',
    ],
    upstreamUrl: 'https://iceberg.apache.org',
    upstreamLabel: 'iceberg.apache.org',
  },
  superset: {
    positioning:
      'Apache Superset is the open-source BI platform. It serves dashboards, ad-hoc exploration, ' +
      'and scheduled reports against the Sovereign’s analytical databases.',
    integration:
      'Superset backs onto CloudNative PG for metadata. Connections to ClickHouse, PostgreSQL, ' +
      'OpenSearch, and Iceberg are pre-configured.',
    highlights: [
      'Drag-and-drop dashboard builder',
      'SQL Lab for ad-hoc analysis',
      'Pre-configured connectors to every Catalyst data store',
    ],
    upstreamUrl: 'https://superset.apache.org',
    upstreamLabel: 'superset.apache.org',
  },

  /* ── CORTEX ────────────────────────────────────────────────────── */
  kserve: {
    positioning:
      'KServe is the Kubernetes-native model serving platform. It exposes ML and LLM endpoints with ' +
      'autoscaling, canary rollouts, and explainability hooks.',
    integration:
      'KServe runs on Knative when scale-to-zero is desired and on raw Deployments otherwise. ' +
      'Storage URIs resolve into the Sovereign’s SeaweedFS pool.',
    highlights: [
      'Standard inference protocol across runtimes',
      'Autoscaling on request volume and latency',
      'Canary rollouts for safe model updates',
    ],
    upstreamUrl: 'https://kserve.github.io/website/',
    upstreamLabel: 'kserve.github.io',
  },
  knative: {
    positioning:
      'Knative is the serverless runtime. It powers scale-to-zero for KServe model endpoints and ' +
      'event-driven workloads that activate only when traffic arrives.',
    integration:
      'Knative Serving runs the data plane; Knative Eventing wires triggers and brokers. The ' +
      'Sovereign’s Kafka stack is a first-class event source.',
    highlights: [
      'Scale-to-zero for HTTP and event-driven workloads',
      'Knative Eventing with Kafka source/sink',
      'Revision-based rollouts with traffic splitting',
    ],
    upstreamUrl: 'https://knative.dev',
    upstreamLabel: 'knative.dev',
  },
  axon: {
    positioning:
      'Axon is the LLM gateway. It brokers access to upstream providers, normalises pricing, and ' +
      'ships tenant-scoped quota controls so platform owners can ration tokens.',
    integration:
      'Axon runs in front of every CORTEX endpoint that consumes upstream APIs. Telemetry feeds ' +
      'LangFuse for cost and latency tracking.',
    highlights: [
      'Provider-agnostic LLM API',
      'Per-tenant quota and rate limiting',
      'Cost-attribution telemetry into LangFuse',
    ],
    upstreamUrl: 'https://openova.io/products/axon',
    upstreamLabel: 'openova.io/axon',
  },
  neo4j: {
    positioning:
      'Neo4j is the graph database for relationship-heavy workloads. It powers fraud detection, ' +
      'identity graphs, and knowledge-graph augmented retrieval for the AI plane.',
    integration:
      'Neo4j runs as a stateful workload backed by a dedicated SeaweedFS-derived storage class. ' +
      'Cypher endpoints are exposed to AI workflows through the Catalyst gateway.',
    highlights: [
      'Cypher query language for graph traversal',
      'Native graph storage and indexing',
      'GraphQL and REST endpoints for application integration',
    ],
    upstreamUrl: 'https://neo4j.com',
    upstreamLabel: 'neo4j.com',
  },
  vllm: {
    positioning:
      'vLLM is the inference engine for large language models. It uses PagedAttention to serve ' +
      'high-throughput requests at low latency.',
    integration:
      'vLLM runs behind KServe with GPU node selectors. Model artifacts load from SeaweedFS; ' +
      'requests arrive through the Catalyst gateway.',
    highlights: [
      'PagedAttention for memory-efficient serving',
      'Continuous batching for high throughput',
      'OpenAI-compatible API surface',
    ],
    upstreamUrl: 'https://docs.vllm.ai',
    upstreamLabel: 'docs.vllm.ai',
  },
  milvus: {
    positioning:
      'Milvus is the vector database that anchors retrieval-augmented generation pipelines. It ' +
      'scales to billions of vectors with sub-second similarity search.',
    integration:
      'Milvus stores indexes on the Sovereign’s SeaweedFS pool. BGE produces embeddings; ' +
      'vLLM consumes retrievals during generation.',
    highlights: [
      'Hybrid search (vector plus filter)',
      'Multiple index types (HNSW, IVF, DiskANN)',
      'Object-storage-backed indexes',
    ],
    upstreamUrl: 'https://milvus.io',
    upstreamLabel: 'milvus.io',
  },
  bge: {
    positioning:
      'BGE is the embedding model server. It converts documents and queries into vectors that ' +
      'Milvus stores and ranks.',
    integration:
      'BGE runs behind KServe with GPU acceleration where available and CPU fallback elsewhere. ' +
      'It is the first hop of every RAG pipeline in the Sovereign.',
    highlights: [
      'Multilingual embedding models',
      'KServe-served with autoscaling',
      'CPU and GPU runtime variants',
    ],
    upstreamUrl: 'https://huggingface.co/BAAI',
    upstreamLabel: 'huggingface.co/BAAI',
  },
  langfuse: {
    positioning:
      'LangFuse is the LLM observability platform. It records every prompt, completion, tool call, ' +
      'and cost across the AI plane and surfaces them through dashboards and a session viewer.',
    integration:
      'LangFuse backs onto CloudNative PG. Application SDKs send traces directly; Axon pipes ' +
      'gateway telemetry through.',
    highlights: [
      'Prompt and completion tracing',
      'Cost attribution per model and tenant',
      'Session viewer for end-to-end RAG debugging',
    ],
    upstreamUrl: 'https://langfuse.com',
    upstreamLabel: 'langfuse.com',
  },
  librechat: {
    positioning:
      'LibreChat is the AI chat interface. It ships with multi-model support, tenant onboarding, ' +
      'and RBAC out of the box, so operators can replace third-party chat services with a ' +
      'self-hosted equivalent.',
    integration:
      'LibreChat backs onto CloudNative PG and consumes the CORTEX inference plane through Axon. ' +
      'SSO is wired through Keycloak.',
    highlights: [
      'Multi-model chat with conversation history',
      'Tenant onboarding and RBAC',
      'Plugin and agent integration',
    ],
    upstreamUrl: 'https://librechat.ai',
    upstreamLabel: 'librechat.ai',
  },

  /* ── RELAY ─────────────────────────────────────────────────────── */
  stalwart: {
    positioning:
      'Stalwart is the all-in-one mail server. It implements SMTP, IMAP, and JMAP with shared ' +
      'mailboxes, send-as, and built-in spam filtering on a single binary.',
    integration:
      'Stalwart binds to the Sovereign’s public domain and authenticates against Keycloak. ' +
      'Storage is on SeaweedFS; submission and IMAP are Envoy-fronted.',
    highlights: [
      'SMTP, IMAP, and JMAP on one binary',
      'Shared mailboxes and per-user send-as',
      'Native sieve filters',
    ],
    upstreamUrl: 'https://stalw.art',
    upstreamLabel: 'stalw.art',
  },
  livekit: {
    positioning:
      'LiveKit is the WebRTC SFU for real-time video and audio. It powers tenant conferencing ' +
      'workloads with end-to-end encryption and recording.',
    integration:
      'LiveKit pairs with STUNner for NAT traversal. Recording lands on the Sovereign’s ' +
      'SeaweedFS pool; signalling is fronted by Envoy.',
    highlights: [
      'WebRTC SFU with end-to-end encryption',
      'Active speaker detection and simulcast',
      'Recording into the Sovereign storage pool',
    ],
    upstreamUrl: 'https://livekit.io',
    upstreamLabel: 'livekit.io',
  },
  stunner: {
    positioning:
      'STUNner is the Kubernetes-native TURN/STUN gateway. It is the missing piece that lets ' +
      'WebRTC media traverse cluster network boundaries reliably.',
    integration:
      'STUNner pairs with LiveKit and other WebRTC workloads. Configuration is delivered through ' +
      'CRDs reconciled by Flux.',
    highlights: [
      'TURN/STUN as a Kubernetes-native workload',
      'Operator-controlled relay policy',
      'Telemetry into the observability pipeline',
    ],
    upstreamUrl: 'https://github.com/l7mp/stunner',
    upstreamLabel: 'l7mp/stunner',
  },
  matrix: {
    positioning:
      'Matrix is the federated messaging protocol. The Sovereign’s homeserver federates with ' +
      'partner organisations while keeping every conversation’s storage local.',
    integration:
      'Matrix backs onto CloudNative PG and uses SeaweedFS for media. Bridges to other protocols ' +
      '(IRC, Slack, XMPP) ship as optional add-ons.',
    highlights: [
      'Federated messaging on the Matrix protocol',
      'End-to-end encryption with Olm/Megolm',
      'Bridges for IRC, Slack, XMPP, and more',
    ],
    upstreamUrl: 'https://matrix.org',
    upstreamLabel: 'matrix.org',
  },
  ntfy: {
    positioning:
      'Ntfy is the push-notification server. Operators publish through HTTP; subscribers receive ' +
      'through the Ntfy app, browser, or webhook.',
    integration:
      'Ntfy is exposed behind Envoy with per-topic ACLs. Catalyst workloads use it as the default ' +
      'channel for non-critical alerts.',
    highlights: [
      'Topic-based publish / subscribe',
      'Mobile, web, and webhook subscribers',
      'Per-topic ACLs and rate limiting',
    ],
    upstreamUrl: 'https://ntfy.sh',
    upstreamLabel: 'ntfy.sh',
  },
}

/** Default copy used when a component id has no entry in COMPONENT_COPY. */
export const DEFAULT_COMPONENT_COPY: ComponentCopy = {
  positioning:
    'This component is part of the OpenOva Catalyst platform and ships through the Blueprint pipeline. ' +
    'Detailed marketing copy is being prepared; the wizard short description above summarises its role.',
  integration:
    'See the dependency graph above and the upstream project for integration details.',
  highlights: [],
  upstreamUrl: 'https://openova.io',
  upstreamLabel: 'openova.io',
}

/** Resolve component copy with a graceful default fallback. */
export function componentCopy(componentId: string): ComponentCopy {
  return COMPONENT_COPY[componentId] ?? DEFAULT_COMPONENT_COPY
}
