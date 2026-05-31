# All wizard inputs, as OpenTofu variables. The catalyst-api provisioner
# package writes these as tofu.auto.tfvars.json before running tofu apply.
#
# Per docs/INVIOLABLE-PRINCIPLES.md principle #4: nothing is hardcoded. Every
# value the wizard captures or the operator chose at provisioning time is a
# variable here. Defaults below describe the COMMON case (solo Sovereign on
# Hetzner) — see infra/hetzner/README.md for the rationale behind each default.

# ── Identity ──────────────────────────────────────────────────────────────

variable "sovereign_fqdn" {
  type        = string
  description = "Fully-qualified domain for this Sovereign — e.g. omantel.omani.works"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]*(\\.[a-z][a-z0-9-]*)+$", var.sovereign_fqdn))
    error_message = "Sovereign FQDN must be a valid lowercase domain (RFC 1035)."
  }
}

variable "sovereign_subdomain" {
  type        = string
  description = "Subdomain portion when domain_mode=pool — e.g. 'omantel' for omantel.omani.works. Empty when BYO."
  default     = ""
}

# OpenovaFlow integration (Agent #3, PR #1389/#1390 follow-up). The
# bp-openova-flow-emitter chart's HelmRelease reads SOVEREIGN_DEPLOYMENT_ID
# from the bootstrap-kit Kustomization's postBuild.substitute env. catalyst-
# api writes it via writeTfvars() from req.DeploymentID. Empty default keeps
# the tofu module callable from `tofu test` mocks that don't supply a
# deployment id; in that case the emitter's FlowID degrades to the literal
# string "" which the openova-flow-server treats as a missing key.
variable "sovereign_deployment_id" {
  type        = string
  description = "Catalyst-api per-deployment 16-char hex id, used as the OpenovaFlow FlowID for this Sovereign. Empty when the tofu module is exercised outside catalyst-api (CI mocks, manual `tofu plan`)."
  default     = ""
}

variable "marketplace_enabled" {
  type        = string
  description = "When 'true', bp-catalyst-platform 1.3.0+ renders the marketplace + tenant-wildcard HTTPRoutes exposing marketplace.<sov> + *.<sov>. Default 'true' as of TBD-V4 (issue #1968, 2026-05-19) — franchised Sovereigns provision marketplace-enabled out of the box; operator opts OUT via the wizard's StepMarketplace toggle. Paired with the chart-side $${MARKETPLACE_ENABLED:-true} slot fallback shipped in PR #1967."
  default     = "true"
  validation {
    condition     = contains(["true", "false"], var.marketplace_enabled)
    error_message = "marketplace_enabled must be the string 'true' or 'false'."
  }
}

# ── BCP topology (Refs #2666 G93.1) ──────────────────────────────────────
# Business-Continuity-Planning topology the operator chose at provision
# time. The canonical seam between the Catalyst console's wizard
# StepProvider (the operator picks "Single region" vs "Active hot-standby
# (2 regions)" vs "Active-active") and the chart-side
# `SOVEREIGN_ENABLE_HOT_STANDBY` envsubst the bp-catalyst-platform chart
# slot 13 + the sme_tenant_gitops writer + every bp-* multi-region chart
# read at render time.
#
# Pre-G93.1 the cloud-init template HARDCODED `SOVEREIGN_ENABLE_HOT_STANDBY:
# ""` regardless of len(var.regions); the Huawei port omitted the key
# entirely. Both meant every multi-region Sovereign silently landed
# single-Cluster CNPG on every tenant Application — Pillar 3
# zero-transactions-lost was impossible to honour without per-overlay
# operator action. catalyst-api's provisioner.Request.BcpTopology field
# (G93.1) is the declarative source-of-truth that this var carries into
# tofu.auto.tfvars.json. provisioner.deriveBcpTopology applies the auto-
# derivation rule (empty + len(regions)>=2 → active-hotstandby; else
# single-region) so a zero-touch multi-region prov lands on the target-
# state shape without an operator opt-in.
variable "bcp_topology" {
  type        = string
  description = "BCP topology: 'single-region', 'active-hotstandby' (primary+replica CNPG pair across two regions), or 'active-active' (symmetric multi-region; today renders as active-hotstandby at the cnpg-pair layer with the G93.4 routing knob a separate workstream)."
  default     = "single-region"
  validation {
    condition     = contains(["single-region", "active-hotstandby", "active-active"], var.bcp_topology)
    error_message = "bcp_topology must be one of: single-region, active-hotstandby, active-active."
  }
}

# enable_hot_standby — derived companion of var.bcp_topology, kept as
# its own var so the cloud-init template can interpolate the
# already-stringified "true"/"false" verbatim into the Kustomization
# postBuild.substitute map without an HCL conditional at render time.
# provisioner.bcpTopologyEnableHotStandby maps the topology enum →
# this string. Cross-validation rule: bcp_topology="single-region" with
# enable_hot_standby="true" is rejected at plan time — this is the seam
# that catches a hand-edited tfvars from drifting out of lockstep with
# what catalyst-api emitted (or a future migration script forgetting to
# update both keys in tandem).
variable "enable_hot_standby" {
  type        = string
  description = "When 'true', the new Sovereign's bp-catalyst-platform chart renders the active-hotstandby CNPG shape on every CNPG-backed tenant Application (Pillar 3 zero-tx-loss). Derived from var.bcp_topology by catalyst-api; lockstep enforced at plan time."
  default     = "false"
  validation {
    condition     = contains(["true", "false"], var.enable_hot_standby)
    error_message = "enable_hot_standby must be the string 'true' or 'false'."
  }
}

# ── QA fixtures auto-enable (Fix #73 — qa-loop bounded-cycle iter-16) ──
# When set, bp-catalyst-platform's qaFixtures stack (qa-<sov> namespace +
# qa-wp Application + Continuum CR + CNPGPair + PDM CRs + ScheduledBackup
# + status-seeder Jobs + tier-bound UserAccess seeder) renders so the
# qa-loop matrix Test Executor finds every Sovereign-side fixture it
# asserts on. Customer-facing Sovereigns provision with the default
# 'false' → no fixture artifacts. QA Sovereigns (omantel.biz, qa.<x>)
# provision with 'true' (set by catalyst-api from
# Request.QATestEnabled). Threaded into bootstrap-kit Kustomization
# postBuild.substitute as QA_FIXTURES_ENABLED — the chart reads via
# `${QA_FIXTURES_ENABLED:-false}` so this var is the canonical
# operator-controlled seam.
variable "qa_fixtures_enabled" {
  type        = string
  description = "When 'true', the Sovereign provisions with the bp-catalyst-platform qaFixtures stack rendered (qa-loop matrix consumers). Default 'false' for customer Sovereigns. Set 'true' only on QA Sovereigns."
  default     = "false"
  validation {
    condition     = contains(["true", "false"], var.qa_fixtures_enabled)
    error_message = "qa_fixtures_enabled must be the string 'true' or 'false'."
  }
}

# Tier-scoped test-session minting endpoint (qa-loop iter-11 Cluster-A).
# Companion to qa_fixtures_enabled — defaults 'true' inside the chart
# already (because every Sovereign that has qaFixtures.enabled is by
# definition a QA Sovereign), but we still thread the toggle so an
# operator can disable the endpoint on an otherwise-QA Sovereign that
# wants only the resource fixtures. Defaults 'false' here (NOT 'true')
# because catalyst-api stamps 'true' alongside qa_fixtures_enabled when
# QATestEnabled fires; leaving the default 'false' ensures a Sovereign
# that omits qa_fixtures_enabled entirely (production) cannot accidentally
# expose the test-session endpoint.
variable "qa_test_session_enabled" {
  type        = string
  description = "When 'true', catalyst-api on the Sovereign exposes POST /api/v1/auth/test-session for tier-scoped session minting (qa-loop matrix executor needs this for tier-boundary 403/200 contracts). Auto-set to match qa_fixtures_enabled on QA Sovereigns; default 'false' for customer Sovereigns."
  default     = "false"
  validation {
    condition     = contains(["true", "false"], var.qa_test_session_enabled)
    error_message = "qa_test_session_enabled must be the string 'true' or 'false'."
  }
}

# Wildcard cert ACME directory selector (Fix #123 — qa-loop iter-1 LE
# rate-limit unblock). When 'true', bp-catalyst-platform 1.4.136+
# sovereign-wildcard-tls Certificate(s) reference the staging
# ClusterIssuer (`letsencrypt-dns01-staging-powerdns`, shipped by
# bp-cert-manager-powerdns-webhook 1.1.0+) instead of production. The
# staging ACME directory has separate, generous rate limits — the
# production 5-certs/168h ceiling per registered domain is wholly
# bypassed. Staging certs are signed by Fake LE Intermediate X1; browsers
# reject without an explicit exception, but `curl -sk` and Playwright
# (ignoreHTTPSErrors:true) accept them. Auto-set to match
# qa_fixtures_enabled on QA Sovereigns by catalyst-api; default 'false'
# here so a Sovereign that omits the QA flags entirely (production)
# cannot accidentally issue staging certs and break browser TLS.
# Threaded into bootstrap-kit Kustomization postBuild.substitute as
# WILDCARD_CERT_USE_STAGING — the chart reads via
# `${WILDCARD_CERT_USE_STAGING:-false}` so this var is the canonical
# operator-controlled seam per docs/INVIOLABLE-PRINCIPLES.md #4
# (never hardcode).
variable "wildcard_cert_use_staging" {
  type        = string
  description = "When 'true', bp-catalyst-platform issues sovereign wildcard certs from the staging Let's Encrypt issuer (separate generous rate limits, certs signed by Fake LE Intermediate X1 — browsers reject, curl -sk + Playwright ignoreHTTPSErrors:true accept). Auto-set by catalyst-api to match qa_fixtures_enabled on QA Sovereigns. Default 'false' for customer Sovereigns."
  default     = "false"
  validation {
    condition     = contains(["true", "false"], var.wildcard_cert_use_staging)
    error_message = "wildcard_cert_use_staging must be the string 'true' or 'false'."
  }
}

# qa-fixtures namespace + Organization names. Default to derivation-friendly
# fallbacks that survive when qa_fixtures_enabled='false' (the chart
# short-circuits before materialising them). When qa_fixtures_enabled='true',
# catalyst-api derives "qa-<first-FQDN-label>" + "<first-FQDN-label>-platform"
# from Request.SovereignFQDN per docs/INVIOLABLE-PRINCIPLES.md #4 (never
# hardcode) so a non-omantel QA Sovereign doesn't inherit "qa-omantel" /
# "omantel-platform" from the chart's bootstrapping defaults.
variable "qa_fixtures_namespace" {
  type        = string
  description = "QA fixtures namespace name. Inert when qa_fixtures_enabled='false'. catalyst-api derives 'qa-<first-FQDN-label>' from SovereignFQDN at provision time."
  default     = "qa-default"
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", var.qa_fixtures_namespace))
    error_message = "qa_fixtures_namespace must be a valid DNS-1123 label."
  }
}

variable "qa_organization" {
  type        = string
  description = "qa-fixtures Organization CR name. Inert when qa_fixtures_enabled='false'. catalyst-api derives '<first-FQDN-label>-platform' from SovereignFQDN at provision time."
  default     = "default-platform"
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", var.qa_organization))
    error_message = "qa_organization must be a valid DNS-1123 label."
  }
}

# ── Multi-domain Sovereign (issue #827, parent epic #825) ─────────────────
#
# The Sovereign supports N parent zones, NOT one. The wizard captures the
# operator's parent-domain list (one for own use, optionally one per SME
# pool, etc.) and serialises it as a YAML inline-array literal. The
# string is interpolated into Flux's postBuild.substitute as
# PARENT_DOMAINS_YAML, then consumed by:
#   - bootstrap-kit slot 11 (bp-powerdns)        — values.zones
#   - bootstrap-kit slot 13 (bp-catalyst-platform) — values.parentZones
# in lockstep so the two slots agree on what the Sovereign considers a
# parent zone.
#
# The default below renders a single-entry array derived from
# sovereign_fqdn so legacy single-zone provisioning paths keep working
# without per-overlay edits. The wizard / catalyst-api populates this
# explicitly when the operator brings 2+ parent zones at signup.
variable "parent_domains_yaml" {
  type        = string
  description = "Parent-domain list for the Sovereign as a YAML inline-array literal. Each entry: {name: <apex>, role: <primary|sme-pool>, ...}. Empty = single-zone fallback derived from sovereign_fqdn."
  default     = ""
}

# Cilium ClusterMesh per-Sovereign anchors (#1101 EPIC-6 multi-region DR).
# Empty + 0 = not joined to any mesh (single-cluster Sovereign — the chart
# still installs the clustermesh-apiserver Pod but no peer connects). When
# the operator joins this Sovereign to a multi-region mesh (e.g. omantel-fsn
# + omantel-hel), set both to the registered values from
# docs/CLUSTERMESH-CLUSTER-IDS.md. Per docs/INVIOLABLE-PRINCIPLES.md #4
# (never hardcode), the values flow operator-request → catalyst-api
# Request.ClusterMeshName/ClusterMeshID → tofu vars → cloudinit
# postBuild.substitute → bootstrap-kit slot 01-cilium.yaml.
variable "cluster_mesh_name" {
  type        = string
  description = "Cilium ClusterMesh peer name for this Sovereign (e.g. omantel-fsn). Empty = not in a mesh. Convention: <sovereign-stem>-<region-code>. Allocated via docs/CLUSTERMESH-CLUSTER-IDS.md."
  default     = ""
}

variable "cluster_mesh_id" {
  type        = number
  description = "Cilium ClusterMesh peer id (1-255 unique within a mesh; 0 reserved for not-in-mesh). Allocated via docs/CLUSTERMESH-CLUSTER-IDS.md — every PR adding a new peer MUST claim a row in that registry."
  default     = 0
  validation {
    condition     = var.cluster_mesh_id >= 0 && var.cluster_mesh_id <= 255
    error_message = "cluster_mesh_id must be 0 (not-in-mesh) or 1-255 (peer id)."
  }
}

variable "org_name" {
  type        = string
  description = "Organisation name for resource labels + initial sovereign-admin Org name"
}

variable "org_email" {
  type        = string
  description = "Initial sovereign-admin email — becomes the first user in Keycloak's catalyst-admin realm"
  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.[^@]+$", var.org_email))
    error_message = "Email must be a syntactically valid address."
  }
}

# ── Hetzner ───────────────────────────────────────────────────────────────

variable "hcloud_token" {
  type        = string
  description = "Hetzner Cloud API token (read+write). Never logged. Never committed to git."
  sensitive   = true
}

variable "hcloud_project_id" {
  type        = string
  description = "Hetzner project ID for resource attribution + audit log"
}

variable "region" {
  type        = string
  description = "Hetzner location (region). Runtime parameter — never hardcoded."
  validation {
    # Authoritative list of Hetzner Cloud locations as of 2026-04-28.
    # Update when Hetzner adds a new location AND the operator wants to
    # provision there. The local.network_zone lookup in main.tf must be
    # updated in the same PR.
    condition     = contains(["fsn1", "nbg1", "hel1", "ash", "hil"], var.region)
    error_message = "Region must be a valid Hetzner location: fsn1 (Falkenstein), nbg1 (Nuremberg), hel1 (Helsinki), ash (Ashburn), hil (Hillsboro)."
  }
}

# ── Topology ──────────────────────────────────────────────────────────────

variable "control_plane_size" {
  type        = string
  description = <<-EOT
    Hetzner server type for the control plane node.

    Default cpx22 (2 vCPU / 4 GB AMD shared) — cost-optimised default
    for the Phase-8a CP working set. The control plane carries ONLY
    k3s (apiserver/etcd/scheduler/controller-manager) + cilium-operator
    + flux controllers + cert-manager + sealed-secrets. Heavy stack
    (bp-keycloak / bp-cnpg / bp-harbor / bp-openbao / bp-grafana)
    schedules to workers because the bootstrap-kit explicitly tolerates
    away from the CP taint. RAM budget: etcd ~512 MB + control plane
    ~1.5 GB + cilium/flux/cert-manager/sealed-secrets ~1 GB + OS ~512
    MB = ~3.5 GB on CPX22's 4 GB.

    Smaller SKUs in the cpx family (cpx21 — 3 vCPU / 4 GB / €10.99/mo)
    are LISTED in /v1/server_types with EU prices but POST /v1/servers
    returns {"error":{"code":"invalid_input","message":"unsupported
    location for server type"}} for cpx11/cpx21/cpx31/cpx41 in any of
    fsn1/nbg1/hel1 (verified 2026-05-04, see issue #752 + the README
    §"Why cpx21/cpx31 are NOT the default" for the curl reproducer).
    cpx22 is the smallest orderable AMD shared SKU with ≥ 4 GB RAM in
    EU DCs.

    Operators picking SOLO mode (worker_count=0) should still pick
    CPX52 explicitly so all Blueprints can fit on a single node.
    Operators picking large/HA topologies still pick larger SKUs
    (cax41/ccx33) for dedicated-vCPU control planes.

    If a Sovereign experiences CP RAM pressure with this default,
    the next step UP is cpx32 (4 vCPU / 8 GB, ~€16.49/mo).
  EOT
  default     = "cpx22"
  validation {
    # Accepted families per Hetzner Cloud (https://www.hetzner.com/cloud/):
    #   cx*   — shared-vCPU Intel
    #   cpx*  — shared-vCPU AMD (the wizard's recommended CPX22 is here)
    #   ccx*  — dedicated-vCPU Intel
    #   cax*  — Ampere Arm
    # Earlier rule omitted the CPX family entirely, which rejected the
    # wizard's default selection at plan-time before the operator could
    # ever provision.
    condition     = can(regex("^(cx[0-9]+|cpx[0-9]+|ccx[0-9]+|cax[0-9]+)$", var.control_plane_size))
    error_message = "control_plane_size must match Hetzner server-type naming (cxNN | cpxNN | ccxNN | caxNN). Minimum orderable in EU DCs (2026-05): cpx22 (4 GB AMD) for the Phase-8a CP working set; cpx32 (8 GB AMD) when the CP exhibits RAM pressure."
  }
}

variable "worker_size" {
  type        = string
  description = <<-EOT
    Hetzner server type for worker nodes.

    Default cpx32 (4 vCPU / 8 GB AMD shared) — the smallest AMD shared
    SKU with 8 GB RAM that is orderable for new servers in fsn1/nbg1/
    hel1 as of 2026-05-04. RAM is the binding constraint for the
    bootstrap-kit's worker pods (cnpg, harbor, keycloak, openbao,
    grafana stack); 8 GB per worker is the sweet spot. The smaller
    cpx31 (also 4 vCPU / 8 GB at ~€20.49/mo published) is LISTED in
    /v1/server_types with EU prices but POST /v1/servers rejects every
    cpx11/cpx21/cpx31/cpx41 order in fsn1/nbg1/hel1 with "unsupported
    location for server type" (issue #752 — see infra/hetzner/README.md
    §"Why cpx21/cpx31 are NOT the default" for the curl reproducer).

    Per docs/INVIOLABLE-PRINCIPLES.md #4 every workload pod is
    reschedulable across nodes; once worker_count ≥ 2 the per-host
    overhead is amortised across nodes. Solo Sovereigns set
    worker_count=0 explicitly and run all workloads on the control
    plane — in that mode this variable is unused.

    If a worker exhibits CPU pressure under load, scale by adding a
    third worker (worker_count=3) before bumping the SKU.
  EOT
  default     = "cpx32"
  validation {
    # Empty string is valid — solo Sovereigns set worker_count = 0 and
    # never read worker_size; the wizard surfaces the empty-SKU state as
    # "no workers" in the review screen. Non-empty values must match the
    # same Hetzner server-type families control_plane_size accepts.
    condition     = var.worker_size == "" || can(regex("^(cx[0-9]+|cpx[0-9]+|ccx[0-9]+|cax[0-9]+)$", var.worker_size))
    error_message = "worker_size must be empty (solo Sovereign, worker_count=0) or match Hetzner server-type naming (cxNN | cpxNN | ccxNN | caxNN)."
  }
}

# ── QA-mode node sizing overrides (Fix #157 — qa-loop bounded-cycle root-cause) ──
#
# Production defaults (`control_plane_size = cpx22`, `worker_size = cpx32`) are
# the documented cost-optimised baseline for customer-facing Sovereigns
# (SME / marketplace / admin / console) — those Sovereigns provision a
# narrower workload set and amortise heavy stacks across `worker_count >= 2`
# nodes plus deliberate scheduling tolerations.
#
# QA-mode Sovereigns (qa_fixtures_enabled='true') provision a SUPERSET:
# qa-loop iter-N matrix runs install bp-keycloak (2Gi JVM limit) +
# bp-harbor (6 sub-components ≈ 2.5Gi req) + bp-cnpg (WAL+DB) + bp-openbao
# (3-replica Raft) AND additionally the qaFixtures stack (qa-<sov>
# namespace + qa-wp Application + Continuum CR + CNPGPair + status-seeder
# Jobs). Per `memory/session_2026_05_10_bounded_cycle_handover.md`
# (entry: "provision #5 cpx22 OOM"), 12 of 12 fresh QA Sovereign provisions
# in the bounded-cycle session wedged with the production defaults — the
# CP's ~3.5GB k3s+cilium+flux+cert-manager+sealed-secrets working set
# leaves zero budget for Flux source-controller's burst (~700MB during
# the 44-slot apply) and worker_count=2 × cpx32 (8GB each) is too tight
# for the keycloak+harbor+cnpg+openbao race.
#
# These two variables auto-flip in main.tf when `qa_fixtures_enabled='true'`
# (the QA-mode gate already wired by Fix #123 via wildcard_cert_use_staging).
# Production Sovereigns NEVER read these defaults — `coalesce(...)` in
# main.tf falls back to `var.control_plane_size` / `var.worker_size`. The
# operator can still pin smaller QA SKUs by setting these explicitly to
# match the production defaults — runtime configuration, never hardcoded
# (per docs/INVIOLABLE-PRINCIPLES.md #4).
variable "qa_control_plane_size" {
  type        = string
  description = <<-EOT
    Hetzner control-plane SKU used when `qa_fixtures_enabled='true'`.
    Default cpx32 (4 vCPU / 8 GB AMD shared) — one tier above the
    production cpx22 default. Justification: QA Sovereigns carry the
    full bp-* stack PLUS the qaFixtures stack (Continuum CR + CNPGPair
    + status-seeder Jobs + ScheduledBackup + tier-bound UserAccess
    seeder). The 3.5GB CP working set documented for cpx22 leaves
    zero RAM headroom for Flux source-controller's ~700MB burst
    during the 44-slot bootstrap-kit apply, which OOM-cascades all
    flux-system Pods on Sovereign provision #5..#12 of the
    2026-05-10 bounded-cycle session. cpx32 doubles the RAM ceiling
    so the burst lands within budget.

    When qa_fixtures_enabled='false' (every customer Sovereign) the
    coalesce() in main.tf bypasses this var entirely and falls back
    to var.control_plane_size — production paths are unaffected.
  EOT
  default     = "cpx32"
  validation {
    condition     = can(regex("^(cx[0-9]+|cpx[0-9]+|ccx[0-9]+|cax[0-9]+)$", var.qa_control_plane_size))
    error_message = "qa_control_plane_size must match Hetzner server-type naming (cxNN | cpxNN | ccxNN | caxNN)."
  }
}

variable "qa_worker_size" {
  type        = string
  description = <<-EOT
    Hetzner worker SKU used when `qa_fixtures_enabled='true'`. Default
    cpx42 (8 vCPU / 16 GB AMD shared) — one tier above the production
    cpx32 default. Justification: bp-keycloak (2Gi JVM limit) +
    bp-harbor (6 sub-components ≈ 2.5Gi req across registry, core,
    portal, jobservice, trivy, redis) + bp-cnpg primary (1Gi req +
    WAL spill) + bp-openbao 3-replica Raft (3 × 512Mi req) all race
    for the 8GB worker budget on cpx32. With worker_count=2, two
    workers × 8GB cannot satisfy the simultaneous request set
    without eviction loops once the qaFixtures Continuum + CNPGPair
    Jobs queue. cpx42 (16GB) doubles the per-worker headroom so the
    full QA matrix lands without OOM.

    When qa_fixtures_enabled='false' (every customer Sovereign) the
    coalesce() in main.tf bypasses this var entirely and falls back
    to var.worker_size — production paths are unaffected.
  EOT
  default     = "cpx42"
  validation {
    # Match worker_size's empty-string allowance so a solo QA Sovereign
    # (worker_count=0) can still validate.
    condition     = var.qa_worker_size == "" || can(regex("^(cx[0-9]+|cpx[0-9]+|ccx[0-9]+|cax[0-9]+)$", var.qa_worker_size))
    error_message = "qa_worker_size must be empty (solo QA Sovereign, worker_count=0) or match Hetzner server-type naming (cxNN | cpxNN | ccxNN | caxNN)."
  }
}

variable "worker_count" {
  type        = number
  description = <<-EOT
    Number of worker nodes joined to the k3s control plane.

    Default 2 — restores the horizontal-scale agreement (issue #733):
    every Sovereign should land with at least 1 CP + 2 workers so the
    operator sees a TRULY multi-node cluster from handover. Workloads
    requiring `replicas: 2` (catalyst-api, catalyst-ui, marketplace-api)
    can spread across nodes; node failure no longer takes the whole
    Sovereign down.

    0 = single-node solo Sovereign (control plane handles all workloads;
    used for dev/POC). Operators opt into solo mode explicitly via the
    wizard's worker count picker.
  EOT
  default     = 2
  validation {
    condition     = var.worker_count >= 0 && var.worker_count <= 50
    error_message = "Worker count must be between 0 and 50."
  }
}

variable "ha_enabled" {
  type        = bool
  description = "When true, provisions 3 control-plane nodes for HA. When false, single control-plane node."
  default     = false
}

# ── Per-region SKU payload ────────────────────────────────────────────────
#
# The wizard captures sizing per-region (each region has its own provider,
# its own cloud-region, and its own control-plane + worker SKUs). The
# canonical request shape carries one entry per topology slot via this
# variable; the legacy singular control_plane_size / worker_size /
# worker_count above mirror regions[0] for the single-region apply path
# main.tf currently drives.
#
# Multi-region tofu wiring is structural-correct (variables.tf accepts the
# list, the catalyst-api provisioner emits it to tofu.auto.tfvars.json),
# but only regions[0] is end-to-end exercised today against a real Hetzner
# project. The for_each iteration that activates the rest will replace
# main.tf's single-server hcloud_server resources with one per-region
# block — at that point this variable becomes the source of truth and the
# legacy singular fields drop out. The door is open structurally so that
# activation is a follow-up commit, not a redesign.
variable "regions" {
  type = list(object({
    provider         = string
    cloudRegion      = string
    controlPlaneSize = string
    workerSize       = string
    workerCount      = number
  }))
  description = <<-EOT
    Per-region SKU payload from the wizard's StepProvider. One entry per
    topology slot (plus 1 for AIR-GAP when enabled). SKU strings are the
    provider's NATIVE instance-type identifier (cx32, m6i.xlarge,
    Standard_D4s_v5, ...) — passed verbatim to that provider's API.

    When empty, main.tf falls back to the singular control_plane_size /
    worker_size / worker_count variables (the back-compat path used by
    handler/load_test.go and any pre-rework wizard payload).
  EOT
  default     = []
  validation {
    condition = alltrue([
      for r in var.regions :
      contains(["hetzner", "huawei", "oci", "aws", "azure"], r.provider)
    ])
    error_message = "Each regions[].provider must be one of: hetzner, huawei, oci, aws, azure."
  }
}

# ── k3s ───────────────────────────────────────────────────────────────────

variable "k3s_version" {
  type        = string
  description = <<-EOT
    k3s release pinned for both control-plane and workers. Must match the
    INSTALL_K3S_VERSION format (e.g. v1.31.4+k3s1). Pinned so a Sovereign
    provisioned today and one provisioned next month land on the same
    Kubernetes minor — required for blueprint compatibility guarantees
    documented in docs/PLATFORM-TECH-STACK.md §8.1.
  EOT
  default     = "v1.31.4+k3s1"
  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+\\+k3s[0-9]+$", var.k3s_version))
    error_message = "k3s_version must match the INSTALL_K3S_VERSION format vMAJOR.MINOR.PATCH+k3sN (e.g. v1.31.4+k3s1)."
  }
}

# ── SSH ───────────────────────────────────────────────────────────────────

variable "ssh_public_key" {
  type        = string
  description = <<-EOT
    Public SSH key (OpenSSH format) attached to all servers for
    sovereign-admin break-glass access.

    The key MUST come from the operator's Hetzner project / SSO-linked
    identity — never auto-generated by this module. See
    infra/hetzner/README.md §"SSH key management" for why ephemeral keys
    are rejected (break-glass + audit-trail requirements).
  EOT
  validation {
    condition     = can(regex("^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp256) ", var.ssh_public_key))
    error_message = "SSH public key must be in OpenSSH format starting with ssh-rsa, ssh-ed25519, or ecdsa-sha2-nistp256."
  }
}

# ── DNS ───────────────────────────────────────────────────────────────────

variable "domain_mode" {
  type        = string
  description = "How DNS is managed: 'pool' (Catalyst writes records via Dynadot), 'byo' (customer manages own DNS)"
  default     = "pool"
  validation {
    condition     = contains(["pool", "byo"], var.domain_mode)
    error_message = "Domain mode must be 'pool' or 'byo'."
  }
}

variable "pool_domain" {
  type        = string
  description = "Pool domain when domain_mode=pool — e.g. 'omani.works'"
  default     = ""
}

variable "dynadot_key" {
  type        = string
  description = "Dynadot API key (required when domain_mode=pool)"
  default     = ""
  sensitive   = true
}

variable "dynadot_secret" {
  type        = string
  description = "Dynadot API secret (required when domain_mode=pool)"
  default     = ""
  sensitive   = true
}

variable "dynadot_managed_domains" {
  type        = string
  description = "Comma-separated list of pool domains the Dynadot webhook is permitted to mutate. Defaults to the parent zone of sovereign_fqdn when blank (e.g. 'omani.works' for 'console.otech22.omani.works')."
  default     = ""
}

variable "powerdns_api_key" {
  type        = string
  description = "Contabo PowerDNS API key. Interpolated by cloudinit-control-plane.tftpl into the Sovereign's cert-manager/powerdns-api-credentials Secret so bp-cert-manager-powerdns-webhook can write DNS-01 challenge TXT records to contabo's authoritative omani.works zone (PR #681 followup). Required when domain_mode=pool."
  default     = ""
  sensitive   = true
}

# ── GHCR pull token ───────────────────────────────────────────────────────
#
# Long-lived GHCR token (GitHub PAT or fine-grained token, scope
# `packages:read` on `openova-io`) that the new Sovereign's Flux
# source-controller uses to pull the private bp-* OCI artifacts from
# `ghcr.io/openova-io/`. Cloud-init writes this into the
# flux-system/ghcr-pull Secret on the freshly-installed k3s control
# plane BEFORE applying the GitRepository + Kustomization that wires up
# clusters/<sovereign-fqdn>/.
#
# Without this, every HelmRepository CR in
# clusters/<sovereign-fqdn>/bootstrap-kit/ (each carrying
# `secretRef: name: ghcr-pull`) errors with:
#   failed to get authentication secret 'flux-system/ghcr-pull':
#     secrets "ghcr-pull" not found
# Phase 1 stalls at bp-cilium and the bootstrap kit never lands. The
# operator-applied workaround (kubectl apply the secret by hand) is not
# durable across reprovisioning of the same Sovereign.
#
# Source: catalyst-api Pod mounts this from the
# `catalyst-ghcr-pull-token` Kubernetes Secret in the catalyst namespace
# as the env var CATALYST_GHCR_PULL_TOKEN. Rotation policy + storage:
# docs/SECRET-ROTATION.md.
variable "ghcr_pull_token" {
  type        = string
  description = <<-EOT
    GHCR pull token (GitHub PAT or fine-grained token, scope `packages:read`
    on openova-io). Written to flux-system/ghcr-pull at cloud-init time so
    Flux source-controller can pull private bp-* OCI artifacts.

    Empty default exists so the OpenTofu module renders for BYO
    catalyst-api Pods that have not yet adopted the
    `catalyst-ghcr-pull-token` Secret; provisioner.Validate() in
    products/catalyst/bootstrap/api/internal/provisioner enforces
    non-empty for managed-pool deployments where Phase 1 absolutely
    needs the token. Sensitive — never logged, never committed to git.

    Rotation policy: yearly, stored in 1Password — see
    docs/SECRET-ROTATION.md.
  EOT
  sensitive   = true
  default     = ""
}

# ── Cloud-init kubeconfig postback (issue #183, Option D) ────────────────

variable "deployment_id" {
  type        = string
  description = <<-EOT
    catalyst-api's per-deployment 16-char hex identifier. Templated
    into the new Sovereign's cloud-init runcmd so the new control
    plane PUTs its rewritten kubeconfig to the correct deployment
    record:

      PUT $${var.catalyst_api_url}/api/v1/deployments/$${var.deployment_id}/kubeconfig

    Empty when the catalyst-api caller is using the legacy
    out-of-band kubeconfig fetch path; cloud-init then skips the PUT
    runcmd entirely.
  EOT
  default     = ""
}

variable "kubeconfig_bearer_token" {
  type        = string
  description = <<-EOT
    32-byte cryptographic-random bearer token the new Sovereign's
    cloud-init attaches as `Authorization: Bearer <token>` when
    PUTting back its kubeconfig (issue #183, Option D). Consumed
    once. The catalyst-api persists ONLY the SHA-256 hash on the
    deployment record; the plaintext lives in this tfvars file
    (file mode 0600 on the catalyst-api PVC) until `tofu destroy`
    removes the workdir.

    Empty when deployment_id is empty (legacy out-of-band fetch
    path); cloud-init then skips the PUT runcmd. Sensitive — never
    logged by OpenTofu, never committed to git.
  EOT
  sensitive   = true
  default     = ""
}

variable "catalyst_api_url" {
  type        = string
  description = <<-EOT
    Public origin the new Sovereign's cloud-init PUTs its kubeconfig
    back to. The full URL is

      $${var.catalyst_api_url}/api/v1/deployments/$${var.deployment_id}/kubeconfig

    Defaults to the OpenOva-hosted franchise console; air-gapped
    franchises override this with their own catalyst-api ingress
    via the CATALYST_API_PUBLIC_URL env var on the catalyst-api
    Pod. Per docs/INVIOLABLE-PRINCIPLES.md #4 this is runtime
    configuration, not code.
  EOT
  default     = "https://console.openova.io/sovereign"
}

# ── GitOps source for Flux bootstrap ──────────────────────────────────────

variable "gitops_repo_url" {
  type        = string
  description = "Git URL Flux on the new cluster watches for clusters/<sovereign-fqdn>/. Defaults to public OpenOva monorepo."
  default     = "https://github.com/openova-io/openova"
}

variable "gitops_branch" {
  type        = string
  description = "Branch Flux watches"
  default     = "main"
}

# ── OS hardening ──────────────────────────────────────────────────────────

variable "ssh_allowed_cidrs" {
  type        = list(string)
  description = <<-EOT
    Source CIDRs allowed to reach SSH (port 22). Default empty list = SSH
    is NOT exposed at the firewall and break-glass requires an out-of-band
    path (Hetzner console / VNC). Operators tighten/widen this via
    Crossplane Composition once the cluster is up; the firewall rule below
    is the Phase 0 fallback only.
  EOT
  default     = []
  validation {
    condition     = alltrue([for c in var.ssh_allowed_cidrs : can(cidrnetmask(c))])
    error_message = "Each entry in ssh_allowed_cidrs must be a valid CIDR (e.g. 203.0.113.7/32)."
  }
}

variable "enable_unattended_upgrades" {
  type        = bool
  description = "Install + enable unattended-upgrades for security patches on Ubuntu. Default true; disable only for short-lived test sovereigns."
  default     = true
}

variable "enable_fail2ban" {
  type        = bool
  description = "Install + enable fail2ban with the sshd jail. Default true; disable only when an upstream WAF/IDS already covers the same surface."
  default     = true
}

# ── Hetzner Object Storage (Phase 0b — issue #371) ────────────────────────
#
# Hetzner Object Storage is the canonical S3 backing for Harbor (#383) and
# Velero (#384) on Hetzner Sovereigns per the omantel handover WBS §3 and
# the ADR-0001-derived "S3 vs SeaweedFS" rule (S3-aware apps write to the
# cloud-provider's native S3; only POSIX-only apps go through SeaweedFS as
# a buffer). For Hetzner that native S3 is Object Storage.
#
# Constraints baked into the rest of this module:
#   1. No native `hcloud_object_storage_*` Terraform resource exists today
#      (see versions.tf for the upstream provider audit). Bucket creation
#      is delegated to the `hashicorp/aws` provider configured against
#      Hetzner's S3 endpoint (`<region>.your-objectstorage.com`). We
#      moved off `aminueza/minio` in fix #133 because that provider's
#      Create handler called DeleteBucketPolicy post-create, which
#      Hetzner credentials cannot grant — see versions.tf for the full
#      root-cause writeup.
#   2. Hetzner does NOT expose a Cloud API to create S3 access keys
#      programmatically — the operator issues them once in the Hetzner
#      Console (Object Storage → Manage Credentials, secret half shown
#      exactly once and irretrievable thereafter). The wizard collects
#      both halves; the catalyst-api validates them via S3 ListBuckets;
#      this module receives them as variables and uses them for both
#      bucket creation AND interpolation into the Sovereign cloud-init's
#      `flux-system/object-storage` Kubernetes Secret (vendor-agnostic
#      name since #425).
#   3. Object Storage is available only in fsn1/nbg1/hel1 today. For
#      ash/hil compute Sovereigns the operator picks a European Object
#      Storage region — Velero/Harbor are latency-tolerant and the
#      backup path is asynchronous.

variable "object_storage_region" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage region — one of fsn1 / nbg1 / hel1 (the
    European-only availability zones for Object Storage as of 2026-04).
    The endpoint URL is derived as `<region>.your-objectstorage.com` per
    https://docs.hetzner.com/storage/object-storage/getting-started/
    using-s3-api-tools/. Per docs/INVIOLABLE-PRINCIPLES.md #4 this is a
    runtime variable, never hardcoded — every Sovereign picks its own
    Object Storage region in the wizard.
  EOT
  validation {
    # Authoritative list of Hetzner Object Storage regions as of 2026-04-30.
    # Update when Hetzner adds a new Object Storage region (NOT the same
    # as Cloud regions — Cloud has ash/hil but Object Storage does not).
    condition     = contains(["fsn1", "nbg1", "hel1"], var.object_storage_region)
    error_message = "Object Storage region must be one of: fsn1 (Falkenstein), nbg1 (Nuremberg), hel1 (Helsinki). Object Storage is European-only as of 2026-04."
  }
}

variable "object_storage_access_key" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage S3 access key — operator-issued once in the
    Hetzner Console (Object Storage → Manage Credentials). The
    catalyst-api validates this against the chosen region's S3 endpoint
    via ListBuckets BEFORE `tofu apply` runs, so a typo'd key surfaces
    at the wizard credential step, not 5 minutes into provisioning.
    Sensitive — never logged. Lives only in the per-deployment OpenTofu
    workdir (encrypted PVC, mode 0600) and in the Sovereign's cloud-init
    user_data; wiped on `tofu destroy`.
  EOT
  sensitive   = true
  validation {
    # Hetzner S3 access keys are 20-character ASCII per the AWS S3 v4
    # signing convention they emulate. We accept the broad shape rather
    # than the precise length so future Hetzner format changes don't
    # bounce off this validator with a stale literal.
    condition     = length(var.object_storage_access_key) >= 16 && length(var.object_storage_access_key) <= 64
    error_message = "Object Storage access key must be 16–64 characters."
  }
}

variable "object_storage_secret_key" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage S3 secret key — operator-issued alongside the
    access key in the Hetzner Console. Per Hetzner's docs the secret is
    shown EXACTLY ONCE at issue time; if the operator loses it they must
    rotate. Sensitive — never logged. Same persistence boundary as the
    access key: per-deployment encrypted workdir + Sovereign cloud-init
    only; wiped on `tofu destroy`.
  EOT
  sensitive   = true
  validation {
    # Hetzner S3 secret keys are typically 40 base64 characters (AWS-style)
    # but the public spec does not pin a length and rotations may emit
    # different lengths in the future. 32–128 is the resilient range.
    condition     = length(var.object_storage_secret_key) >= 32 && length(var.object_storage_secret_key) <= 128
    error_message = "Object Storage secret key must be 32–128 characters."
  }
}

variable "harbor_robot_token" {
  type        = string
  description = <<-EOT
    Harbor robot account token for `robot$openova-bot` on harbor.openova.io.
    Written into the Sovereign's /etc/rancher/k3s/registries.yaml at
    cloud-init time so containerd can authenticate against the central
    Harbor proxy-cache projects (proxy-dockerhub, proxy-gcr, proxy-quay,
    proxy-k8s, proxy-ghcr) when pulling images on fresh Hetzner IPs.

    The token is issued on harbor.openova.io via Harbor's robot account API
    after the central Harbor instance stands up (issue #557 Step 2). The
    catalyst-api provisioner reads it from the `harbor-robot-token` K8s
    Secret in the openova-harbor namespace on contabo and forwards it here
    at provisioning time. Sensitive — never logged, never committed to git.

    Default empty: existing test scripts and pre-#557 provisioner builds
    that do not pass this variable still render a valid cloud-init (the
    registries.yaml password field will be blank, causing containerd to
    attempt anonymous pulls on harbor.openova.io which are allowed for
    Public proxy projects). Non-empty is enforced by the provisioner for
    production Sovereign deployments once harbor.openova.io is live.
  EOT
  sensitive   = true
  default     = ""
}

variable "pdm_basic_auth_user" {
  type        = string
  description = <<-EOT
    Username for the Pool Domain Manager (PDM) public ingress at
    `pool.openova.io`. The Sovereign-side catalyst-api uses this
    value (paired with `pdm_basic_auth_pass`) to authenticate
    every PDM call (Day-2 multi-domain "Add another parent
    domain" flow — issue #879). Cloud-init writes the value into
    a `pdm-basicauth` Secret in the `flux-system` namespace with
    Reflector annotations so the Secret mirrors into
    `catalyst-system` where catalyst-api reads it via secretKeyRef.

    Source on contabo: `openova-system/pool-domain-manager-basicauth`
    Secret (operator-managed). The catalyst-api provisioner forwards
    plaintext at provisioning time — never logged, never committed.

    Default empty: when unset, the cloud-init still renders the
    `pdm-basicauth` Secret with empty values. The Sovereign-side
    pdmFlipNS skips SetBasicAuth when the env value is empty, so
    older Sovereigns that pre-date this variable degrade to a
    clear PDM 401 instead of a panic. Once the operator fills
    this in, a re-provision (or a Secret rotation via cloud-init
    re-render) supplies real credentials.
  EOT
  sensitive   = true
  default     = ""
}

variable "pdm_basic_auth_pass" {
  type        = string
  description = <<-EOT
    Password for the Pool Domain Manager (PDM) public ingress.
    See `pdm_basic_auth_user` for the full lifecycle. Sensitive.
  EOT
  sensitive   = true
  default     = ""
}

variable "object_storage_bucket_name" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage bucket name. Bucket names share a global
    namespace across ALL Hetzner Object Storage tenants per
    https://docs.hetzner.com/storage/object-storage/getting-started/
    creating-a-bucket/, so we derive a deterministic per-Sovereign name
    from the FQDN slug (catalyst-api computes this; the wizard never
    surfaces a free-form bucket-name input to the operator). Pattern:
    `catalyst-<sovereign-fqdn-with-dots-replaced-by-dashes>`.

    The bucket is created idempotently via the `hashicorp/aws` provider
    in main.tf (Hetzner-pointed via versions.tf). Existing buckets with
    a matching name in the same tenant return BucketAlreadyOwnedByYou
    on Create, which the AWS SDK normalises into a no-op — re-running
    `tofu apply` against a previously-provisioned Sovereign is therefore
    a no-op, never an error.
  EOT
  validation {
    # S3 bucket naming rules:
    #   - 3-63 chars
    #   - lowercase letters, digits, hyphens
    #   - must start and end with alphanumeric
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.object_storage_bucket_name))
    error_message = "Object Storage bucket name must be 3-63 chars, lowercase alphanumeric + hyphens, starting and ending with alphanumeric (RFC-compliant S3 bucket naming)."
  }
}

# ── Handover JWT public key (issue #605, Phase-8b) ────────────────────────
#
# RFC 7517 JWK JSON bytes of the Catalyst-Zero RS256 public key. Written to
# /var/lib/catalyst/handover-jwt-public.jwk (mode 0600) on the new Sovereign
# control-plane by cloud-init. The Sovereign-side Agent-C (auth_handover.go)
# reads this file to verify the one-time handover JWT without a cross-cluster
# RPC to Catalyst-Zero.
#
# Source: the catalyst-api provisioner reads the live Signer's PublicJWK()
# and stamps it onto provisioner.Request.HandoverJWTPublicKey before writing
# tofu.auto.tfvars.json. The field carries json:"-" so the wizard POST body
# can never inject it — it always comes from the live Signer.
#
# Default empty: pre-#605 provisioner builds that do not pass this variable
# write an empty file; auth/handover returns 503 (key unavailable) on any
# Sovereign provisioned without it until a subsequent reprovisioning run.
variable "handover_jwt_public_key" {
  type        = string
  description = <<-EOT
    RFC 7517 JWK JSON of the Catalyst-Zero RS256 handover-JWT public key.
    Written to /var/lib/catalyst/handover-jwt-public.jwk (mode 0600) on
    the new Sovereign control-plane by cloud-init so Agent-C can verify
    the one-time JWT without a cross-cluster network call to Catalyst-Zero.
    Supplied by the catalyst-api provisioner from h.handoverSigner.PublicJWK().
    Empty when the provisioner has no signer (CATALYST_HANDOVER_KEY_PATH unset).
  EOT
  sensitive   = true
  default     = ""
}
