/**
 * Canonical phases the wizard's progress UI renders.
 *
 * Source of truth: docs/PROVISIONING-PLAN.md §"Phase 5 — Bootstrap kit"
 * + docs/SOVEREIGN-PROVISIONING.md §3 (Phase 0) + §4 (Phase 1 hand-off).
 *
 * Two layers, in real chronological order:
 *
 *   Layer A — OpenTofu (Phase 0 cloud provisioning, runs in catalyst-api)
 *     a1. tofu-init
 *     a2. tofu-plan
 *     a3. tofu-apply           (Hetzner network, firewall, server, LB, DNS)
 *     a4. tofu-output          (capture control_plane_ip, lb_ip, kubeconfig)
 *     a5. flux-bootstrap       (cloud-init bootstrapped Flux + Crossplane on
 *                              the new cluster; control hands off to it)
 *
 *   Layer B — bootstrap-kit (Phase 1, reconciled by Flux INSIDE the new
 *                            cluster, in dependency order; surfaced to the
 *                            wizard as Flux Kustomization status events)
 *     b1.  cilium             (CNI + Hubble — required before any pod can
 *                              schedule)
 *     b2.  cert-manager       (TLS issuer for everything below)
 *     b3.  flux               (the reconciler itself, present from a5 — but
 *                              we surface its Kustomization-controller
 *                              becoming healthy as an explicit phase)
 *     b4.  crossplane         (day-2 IaC, adopts OpenTofu state)
 *     b5.  sealed-secrets     (in-Git encrypted secrets primitive)
 *     b6.  spire              (workload identity, prerequisite for OpenBao)
 *     b7.  jetstream          (NATS event bus for control-plane components)
 *     b8.  openbao            (secrets backend; uses SPIRE for auth)
 *     b9.  keycloak           (IdP; uses OpenBao for SQL credentials)
 *     b10. gitea              (GitOps repo for Sovereign-internal config)
 *     b11. bp-catalyst-platform (umbrella Blueprint: console, marketplace,
 *                                admin, lifecycle manager — the actual
 *                                Sovereign control plane)
 *
 * The backend SSE stream emits {phase, level, message} events. The `phase`
 * string in each event maps 1:1 to a `BootstrapPhase.id` here. The wizard
 * progress UI uses this list to (a) render a step-by-step indicator with one
 * checkpoint per phase and (b) place each incoming event into the right
 * phase's log.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode"), this is the only
 * place the phase list lives. Both the wizard's step indicator widget and
 * the SSE log-stream widget import from here. If a new component is added to
 * the bootstrap kit (e.g. SPIFFE → SPIRE-Bridge), update this file and every
 * dependent renders the new phase automatically.
 */

export type BootstrapLayer = 'opentofu' | 'bootstrap-kit'

/**
 * One bootstrap phase. The id field is what the catalyst-api SSE backend
 * sends in {phase} fields and what the new cluster's Flux Kustomization
 * controller emits as the kustomization name.
 *
 * label: human-readable progress label (one line in the indicator)
 * description: subtitle shown when the indicator is expanded
 * upstream: upstream OSS component name (renders next to label as a chip)
 * layer: which bootstrap layer this phase belongs to
 */
export interface BootstrapPhase {
  id: string
  label: string
  description: string
  upstream: string
  layer: BootstrapLayer
}

/**
 * Phase A — OpenTofu Phase 0 cloud provisioning.
 * Each id matches `emit("<id>", ...)` calls in
 * products/catalyst/bootstrap/api/internal/provisioner/provisioner.go.
 */
export const OPENTOFU_PHASES: BootstrapPhase[] = [
  {
    id: 'tofu-init',
    label: 'Initialise OpenTofu',
    description: 'Download providers, prepare working directory',
    upstream: 'OpenTofu',
    layer: 'opentofu',
  },
  {
    id: 'tofu-plan',
    label: 'Plan cloud resources',
    description: 'Compute network, firewall, servers, load balancer, DNS',
    upstream: 'OpenTofu',
    layer: 'opentofu',
  },
  {
    id: 'tofu-apply',
    label: 'Apply infrastructure',
    description: 'Create real cloud resources in your provider account',
    upstream: 'OpenTofu',
    layer: 'opentofu',
  },
  {
    id: 'tofu-output',
    label: 'Capture outputs',
    description: 'Read control-plane IP, load-balancer IP, kubeconfig',
    upstream: 'OpenTofu',
    layer: 'opentofu',
  },
  {
    id: 'flux-bootstrap',
    label: 'Hand off to Flux',
    description: 'Cloud-init bootstrapped Flux on the new cluster — control passes inside',
    upstream: 'Flux',
    layer: 'opentofu',
  },
]

/**
 * Phase B — 11-component bootstrap-kit, reconciled by Flux from inside the
 * new cluster. Order is dependency order, NOT installation order: Flux may
 * apply several in parallel where the dependency graph allows. The ids match
 * the Flux Kustomization names in clusters/<sovereign-fqdn>/.
 */
export const BOOTSTRAP_KIT_PHASES: BootstrapPhase[] = [
  {
    id: 'cilium',
    label: 'Cilium',
    description: 'eBPF networking + Hubble observability — the CNI',
    upstream: 'Cilium',
    layer: 'bootstrap-kit',
  },
  {
    id: 'cert-manager',
    label: 'cert-manager',
    description: 'TLS issuer used by every subsequent component',
    upstream: 'cert-manager',
    layer: 'bootstrap-kit',
  },
  {
    id: 'flux',
    label: 'Flux CD',
    description: 'GitOps reconciler — source of truth = Git',
    upstream: 'Flux',
    layer: 'bootstrap-kit',
  },
  {
    id: 'crossplane',
    label: 'Crossplane',
    description: 'Day-2 IaC — adopts OpenTofu state, manages further infra',
    upstream: 'Crossplane',
    layer: 'bootstrap-kit',
  },
  {
    id: 'sealed-secrets',
    label: 'Sealed Secrets',
    description: 'In-Git encrypted secrets primitive',
    upstream: 'Sealed Secrets',
    layer: 'bootstrap-kit',
  },
  {
    id: 'spire',
    label: 'SPIRE',
    description: 'Workload identity — prerequisite for OpenBao auth',
    upstream: 'SPIRE',
    layer: 'bootstrap-kit',
  },
  {
    id: 'jetstream',
    label: 'JetStream',
    description: 'NATS event bus for control-plane components',
    upstream: 'NATS JetStream',
    layer: 'bootstrap-kit',
  },
  {
    id: 'openbao',
    label: 'OpenBao',
    description: 'Secrets backend — uses SPIRE for workload auth',
    upstream: 'OpenBao',
    layer: 'bootstrap-kit',
  },
  {
    id: 'keycloak',
    label: 'Keycloak',
    description: 'Identity provider — uses OpenBao for DB credentials',
    upstream: 'Keycloak',
    layer: 'bootstrap-kit',
  },
  {
    id: 'gitea',
    label: 'Gitea',
    description: 'Sovereign-internal GitOps repository',
    upstream: 'Gitea',
    layer: 'bootstrap-kit',
  },
  {
    id: 'bp-catalyst-platform',
    label: 'Catalyst Platform',
    description: 'Umbrella Blueprint — console, marketplace, admin',
    upstream: 'bp-catalyst-platform',
    layer: 'bootstrap-kit',
  },
]

/**
 * Combined phase list in chronological order — what the wizard's progress
 * indicator iterates over.
 */
export const ALL_PHASES: BootstrapPhase[] = [
  ...OPENTOFU_PHASES,
  ...BOOTSTRAP_KIT_PHASES,
]

/** Total phase count — used by the percentage progress bar. */
export const TOTAL_PHASES: number = ALL_PHASES.length

/** Find a phase by id; returns undefined for unknown ids (defensive). */
export function findPhase(id: string): BootstrapPhase | undefined {
  return ALL_PHASES.find((p) => p.id === id)
}

/**
 * State of a single phase as the wizard sees it.
 *
 *   - pending: not yet started
 *   - running: currently emitting events
 *   - done:    finished successfully (received a "done" event or the next
 *              phase started)
 *   - failed:  received an event with level="error" — Sovereign sits at
 *              `failed_at_<phase.id>` and the user can retry just this
 *              phase or rollback the whole provisioning run
 *   - skipped: backend explicitly emitted a skip (e.g. component opted out)
 */
export type PhaseStatus = 'pending' | 'running' | 'done' | 'failed' | 'skipped'

/** Sovereign-state string the backend uses for failed_at_<phase> markers. */
export function failedAtSovereignState(phaseId: string): string {
  return `failed_at_${phaseId}`
}
