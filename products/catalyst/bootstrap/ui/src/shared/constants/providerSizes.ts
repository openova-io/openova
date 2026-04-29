/**
 * providerSizes.ts — per-provider node-size catalog.
 *
 * The wizard captures Sizing per-region (each region has its own provider,
 * its own cloud-region, and its own control-plane + worker SKUs). Every SKU
 * read in the UI MUST go through PROVIDER_NODE_SIZES[provider] — no SKU
 * literal lives anywhere else. This is the single source of truth, replacing
 * the legacy Hetzner-only HETZNER_NODE_SIZES table that pre-dated the
 * five-provider rework.
 *
 * Each entry carries:
 *   • id        — provider's native instance-type identifier (cx32, c7.large.4,
 *                 VM.Standard.E5.Flex, m6i.large, Standard_D4s_v5, ...)
 *   • label     — display name (usually upper-cased id with the family hint)
 *   • vcpu      — count
 *   • ram       — GB (integer)
 *   • priceHour — euro per hour (used by cost rollup)
 *   • category  — coarse positioning bucket so the UI can sort/group
 *   • description — single-line guidance string
 *   • recommended — true for the SKU each provider's "default starter"
 *
 * Pricing snapshots are intentionally stored as static numbers — they do NOT
 * pretend to be the live spot price. The Review step labels them "estimated"
 * and the OpenTofu module is the system that talks to the cloud API at apply
 * time. Per docs/INVIOLABLE-PRINCIPLES.md #4 (no hardcoded URLs), provider
 * pricing endpoints belong in a runtime config layer when one is wired; until
 * then this static table is the runtime-config equivalent that operators can
 * override by editing one file.
 *
 * Hetzner: cx32 / cpx41 / ccx33 / cax41 — official 2026 catalog.
 * Huawei:  s7n / c7 / m7 — General Computing-plus families per Huawei Cloud.
 * OCI:     VM.Standard.E5.Flex / VM.Standard.A1.Flex / VM.Standard.E4.Flex.
 * AWS:     m6i / c6i / r6i.
 * Azure:   Standard_D{2,4,8}s_v5 / E{4,8}s_v5 — current "v5" generation.
 */

import type { CloudProvider } from '@/entities/deployment/model'

export type SkuCategory = 'standard' | 'compute-optimized' | 'memory-optimized' | 'arm'

export interface NodeSize {
  /** Native provider instance-type id — passed verbatim to the OpenTofu module. */
  id: string
  /** Display label shown in the wizard. */
  label: string
  vcpu: number
  /** RAM in GB. */
  ram: number
  /** Estimated euro per hour — drives the wizard's cost rollup. */
  priceHour: number
  category: SkuCategory
  description: string
  /** Marked as the "starter default" within its provider's catalog. */
  recommended?: boolean
}

export const PROVIDER_NODE_SIZES: Record<CloudProvider, NodeSize[]> = {
  hetzner: [
    {
      id: 'cx32',
      label: 'CX32 — Standard',
      vcpu: 4,
      ram: 8,
      priceHour: 0.014,
      category: 'standard',
      description: 'Shared vCPU x86 — small workloads',
    },
    {
      id: 'cx42',
      label: 'CX42 — Performance',
      vcpu: 8,
      ram: 16,
      priceHour: 0.025,
      category: 'standard',
      description: 'Shared vCPU x86 — solo Sovereign default',
      recommended: true,
    },
    {
      id: 'cpx41',
      label: 'CPX41 — Compute',
      vcpu: 8,
      ram: 16,
      priceHour: 0.039,
      category: 'compute-optimized',
      description: 'AMD EPYC dedicated vCPU — CPU-heavy workloads',
    },
    {
      id: 'ccx33',
      label: 'CCX33 — Dedicated',
      vcpu: 8,
      ram: 32,
      priceHour: 0.099,
      category: 'memory-optimized',
      description: 'Dedicated vCPU x86 — production HA control planes',
    },
    {
      id: 'cax41',
      label: 'CAX41 — Arm',
      vcpu: 16,
      ram: 32,
      priceHour: 0.034,
      category: 'arm',
      description: 'Ampere Altra Arm — best price/perf for k3s',
    },
  ],
  huawei: [
    {
      id: 's7n.large.4',
      label: 's7n.large.4 — Standard',
      vcpu: 2,
      ram: 8,
      priceHour: 0.045,
      category: 'standard',
      description: 'General Computing — Kunpeng, balanced',
    },
    {
      id: 'c7.xlarge.2',
      label: 'c7.xlarge.2 — Compute',
      vcpu: 4,
      ram: 8,
      priceHour: 0.080,
      category: 'compute-optimized',
      description: 'Compute-Plus — control-plane recommended',
      recommended: true,
    },
    {
      id: 'c7.2xlarge.2',
      label: 'c7.2xlarge.2 — Compute',
      vcpu: 8,
      ram: 16,
      priceHour: 0.160,
      category: 'compute-optimized',
      description: 'Compute-Plus — production worker',
    },
    {
      id: 'm7.xlarge.8',
      label: 'm7.xlarge.8 — Memory',
      vcpu: 4,
      ram: 32,
      priceHour: 0.190,
      category: 'memory-optimized',
      description: 'Memory-Plus — JVM, in-memory caches',
    },
    {
      id: 'm7.2xlarge.8',
      label: 'm7.2xlarge.8 — Memory',
      vcpu: 8,
      ram: 64,
      priceHour: 0.380,
      category: 'memory-optimized',
      description: 'Memory-Plus — heavy data plane',
    },
  ],
  oci: [
    {
      id: 'VM.Standard.E5.Flex.2.16',
      label: 'E5.Flex 2/16 — Standard',
      vcpu: 2,
      ram: 16,
      priceHour: 0.036,
      category: 'standard',
      description: 'AMD EPYC Flex shape — minimum viable',
    },
    {
      id: 'VM.Standard.E5.Flex.4.32',
      label: 'E5.Flex 4/32 — Standard',
      vcpu: 4,
      ram: 32,
      priceHour: 0.072,
      category: 'standard',
      description: 'AMD EPYC Flex — control-plane default',
      recommended: true,
    },
    {
      id: 'VM.Standard.E5.Flex.8.64',
      label: 'E5.Flex 8/64 — Standard',
      vcpu: 8,
      ram: 64,
      priceHour: 0.144,
      category: 'standard',
      description: 'AMD EPYC Flex — production worker',
    },
    {
      id: 'VM.Standard.A1.Flex.4.24',
      label: 'A1.Flex 4/24 — Arm',
      vcpu: 4,
      ram: 24,
      priceHour: 0.040,
      category: 'arm',
      description: 'Ampere Altra Arm — Always-Free-tier eligible',
    },
    {
      id: 'VM.Standard.E4.Flex.8.64',
      label: 'E4.Flex 8/64 — Legacy',
      vcpu: 8,
      ram: 64,
      priceHour: 0.130,
      category: 'standard',
      description: 'AMD EPYC previous generation — broad region coverage',
    },
  ],
  aws: [
    {
      id: 'm6i.large',
      label: 'm6i.large — General',
      vcpu: 2,
      ram: 8,
      priceHour: 0.106,
      category: 'standard',
      description: 'Intel Ice Lake — small workloads',
    },
    {
      id: 'm6i.xlarge',
      label: 'm6i.xlarge — General',
      vcpu: 4,
      ram: 16,
      priceHour: 0.211,
      category: 'standard',
      description: 'Intel Ice Lake — control-plane default',
      recommended: true,
    },
    {
      id: 'm6i.2xlarge',
      label: 'm6i.2xlarge — General',
      vcpu: 8,
      ram: 32,
      priceHour: 0.422,
      category: 'standard',
      description: 'Intel Ice Lake — production worker',
    },
    {
      id: 'c6i.2xlarge',
      label: 'c6i.2xlarge — Compute',
      vcpu: 8,
      ram: 16,
      priceHour: 0.378,
      category: 'compute-optimized',
      description: 'Intel Ice Lake compute — CPU-heavy workloads',
    },
    {
      id: 'r6i.2xlarge',
      label: 'r6i.2xlarge — Memory',
      vcpu: 8,
      ram: 64,
      priceHour: 0.554,
      category: 'memory-optimized',
      description: 'Intel Ice Lake memory — JVM, in-memory caches',
    },
  ],
  azure: [
    {
      id: 'Standard_D2s_v5',
      label: 'D2s v5 — General',
      vcpu: 2,
      ram: 8,
      priceHour: 0.106,
      category: 'standard',
      description: 'Intel Ice Lake — small workloads',
    },
    {
      id: 'Standard_D4s_v5',
      label: 'D4s v5 — General',
      vcpu: 4,
      ram: 16,
      priceHour: 0.211,
      category: 'standard',
      description: 'Intel Ice Lake — control-plane default',
      recommended: true,
    },
    {
      id: 'Standard_D8s_v5',
      label: 'D8s v5 — General',
      vcpu: 8,
      ram: 32,
      priceHour: 0.422,
      category: 'standard',
      description: 'Intel Ice Lake — production worker',
    },
    {
      id: 'Standard_E4s_v5',
      label: 'E4s v5 — Memory',
      vcpu: 4,
      ram: 32,
      priceHour: 0.282,
      category: 'memory-optimized',
      description: 'Intel Ice Lake memory — JVM, in-memory caches',
    },
    {
      id: 'Standard_E8s_v5',
      label: 'E8s v5 — Memory',
      vcpu: 8,
      ram: 64,
      priceHour: 0.564,
      category: 'memory-optimized',
      description: 'Intel Ice Lake memory — heavy data plane',
    },
  ],
}

/**
 * Locate a SKU by provider+id. Returns undefined if the operator has a
 * persisted selection that no longer exists in the catalog (e.g. they
 * upgraded the wizard after the provider deprecated an old generation).
 */
export function findNodeSize(provider: CloudProvider, id: string): NodeSize | undefined {
  return PROVIDER_NODE_SIZES[provider].find((s) => s.id === id)
}

/**
 * The "starter default" SKU for a provider — first one flagged
 * `recommended: true`, falling back to the first entry.
 */
export function defaultNodeSizeId(provider: CloudProvider): string {
  const list = PROVIDER_NODE_SIZES[provider]
  const recommended = list.find((s) => s.recommended)
  return (recommended ?? list[0]).id
}
